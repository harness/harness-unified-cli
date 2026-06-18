// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/harness/harness-cli/modules/core/auth/assets"
	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/hlog"
)

const (
	mcpBaseURL         = "https://mcp.harness.io"
	ssoCallbackPath    = "/oauth/callback"
	ssoPort            = 57380
	ssoCallbackTimeout = 5 * time.Minute
)

// LoginSSOHandler implements `harness auth loginsso`.
// It performs the full OAuth2 PKCE flow via browser:
//  1. Fetch authorization server metadata from id.harness.io
//  2. Launch browser with PKCE authorization URL + local callback server on port 57380
//  3. Exchange code for token, extract account ID from JWT claims
//  4. Drop into existing org/project picker wizard, then save profile
func LoginSSOHandler(ctx *cmdctx.Ctx) error {
	overwrite := cmdctx.GetBool(ctx.FlagValues, "overwrite")
	noOverwrite := cmdctx.GetBool(ctx.FlagValues, "no-overwrite")
	if overwrite && noOverwrite {
		return fmt.Errorf("--overwrite and --no-overwrite are mutually exclusive")
	}

	profileName := cmdctx.GetString(ctx.FlagValues, "profile")
	if profileName == "" {
		profileName = "default"
	}
	if !profileNameRe.MatchString(profileName) {
		return fmt.Errorf("invalid profile name %q: must match ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$", profileName)
	}

	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	if _, exists := cfg.Profiles[profileName]; exists {
		switch {
		case noOverwrite:
			return fmt.Errorf("profile %q already exists (use --overwrite to replace it)", profileName)
		case !overwrite:
			fmt.Fprintf(os.Stderr, "WARNING: profile %q already exists, continuing will overwrite it\n\n", profileName)
			if !console.PromptYesNo("Overwrite?") {
				return fmt.Errorf("canceled by user — config not written")
			}
			fmt.Fprintln(os.Stderr)
		}
	}

	meta, err := auth.FetchAuthServerMeta(&http.Client{Timeout: 10 * time.Second}, auth.SSOAuthServerBase)
	if err != nil {
		return fmt.Errorf("SSO discovery failed: %w", err)
	}

	token, refreshToken, accountID, subdomain, err := runPKCEFlow(meta)
	if err != nil {
		return fmt.Errorf("SSO login failed: %w", err)
	}

	apiURL, err := resolveAPIURL(token, accountID, subdomain)
	if err != nil {
		return err
	}

	// Reuse the existing set-wizard to pick org/project.
	var orgID, projectID string
	if console.IsBothTTY() {
		result, werr := RunSetWizard(ctx, &SetWizardInput{
			APIURL:    apiURL,
			Token:     token,
			AccountID: accountID,
			AuthType:  auth.AuthTypeSSO,
		})
		if werr != nil {
			return werr
		}
		if result == nil {
			return fmt.Errorf("canceled by user — config not written")
		}
		orgID = result.OrgID
		projectID = result.Project
	}

	cfg.Profiles[profileName] = &auth.Profile{
		APIUrl:    apiURL,
		AccountID: accountID,
		OrgID:     orgID,
		ProjectID: projectID,
		AuthType:  auth.AuthTypeSSO,
	}
	if err := auth.SaveConfig(cfg); err != nil {
		return fmt.Errorf("saving profile: %w", err)
	}
	if err := auth.SetSSOCredentials(profileName, token, refreshToken); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Printf("Logged in via SSO. Profile %q written.\n\n", profileName)
	printStatus(runStatusChecks(profileName))
	return nil
}

// resolveAPIURL returns the MCP gateway URL for SSO-authenticated requests.
// All SSO traffic is routed through mcp.harness.io regardless of the per-cluster
// subdomain in the JWT; the gateway handles cluster routing internally.
func resolveAPIURL(token, accountID, subdomain string) (string, error) {
	return mcpBaseURL, nil
}

// --- PKCE flow ---

func runPKCEFlow(meta *auth.AuthServerMeta) (token, refreshToken, accountID, subdomain string, err error) {
	verifier, err := auth.GenerateCodeVerifier()
	if err != nil {
		return "", "", "", "", fmt.Errorf("generating PKCE verifier: %w", err)
	}
	challenge := auth.CodeChallenge(verifier)

	state, err := auth.RandomState()
	if err != nil {
		return "", "", "", "", err
	}

	redirectURI := fmt.Sprintf("http://localhost:%d%s", ssoPort, ssoCallbackPath)
	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", ssoPort))
	if err != nil {
		return "", "", "", "", fmt.Errorf("starting local callback server on port %d: %w", ssoPort, err)
	}

	authURL := buildAuthURL(meta.AuthorizationEndpoint, auth.SSOClientID, redirectURI, challenge, state)
	fmt.Fprintf(os.Stderr, "\nOpening browser for SSO login…\n%s\n\n", authURL)
	_ = console.OpenBrowser(authURL)

	code, err := waitForCallback(ln, state)
	if err != nil {
		return "", "", "", "", fmt.Errorf("callback failed: %w", err)
	}

	rawToken, rawRefreshToken, err := auth.ExchangeCode(meta.TokenEndpoint, auth.SSOClientID, code, verifier, redirectURI)
	if err != nil {
		return "", "", "", "", fmt.Errorf("token exchange failed: %w", err)
	}

	claims, err := parseJWT(rawToken)
	if err != nil {
		return "", "", "", "", fmt.Errorf("extracting claims from token: %w", err)
	}

	return rawToken, rawRefreshToken, claims.AccountID, claims.Subdomain, nil
}

func buildAuthURL(endpoint, clientID, redirectURI, challenge, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("scope", "openid profile email")
	return endpoint + "?" + params.Encode()
}

// waitForCallback runs a one-shot HTTP server on ln, waits for the OAuth callback,
// and returns the authorization code.
func waitForCallback(ln net.Listener, expectedState string) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	callbackTmpl := template.Must(template.New("callback").Parse(assets.CallbackHTML))
	renderPage := func(w http.ResponseWriter, data callbackPageData) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var buf bytes.Buffer
		if err := callbackTmpl.Execute(&buf, data); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Write(buf.Bytes()) //nolint:errcheck
	}

	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ssoCallbackPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			desc := q.Get("error_description")
			renderPage(w, callbackPageData{
				Title:        "Login failed",
				LogoSVG:      template.HTML(assets.LogoSVG),
				Success:      false,
				ErrorMessage: "Authorization was denied or an error occurred.",
				ErrorDetail:  errParam + ": " + desc,
			})
			errCh <- fmt.Errorf("authorization error: %s — %s", errParam, desc)
			return
		}
		if q.Get("state") != expectedState {
			http.Error(w, "invalid state", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch — possible CSRF")
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- fmt.Errorf("no authorization code in callback")
			return
		}
		renderPage(w, callbackPageData{
			Title:   "Login successful",
			LogoSVG: template.HTML(assets.LogoSVG),
			Success: true,
		})
		codeCh <- code
	})

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), ssoCallbackTimeout)
	defer cancel()

	shutdown := func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		srv.Shutdown(shutCtx) //nolint:errcheck
	}

	select {
	case code := <-codeCh:
		shutdown()
		return code, nil
	case err := <-errCh:
		shutdown()
		return "", err
	case <-ctx.Done():
		shutdown()
		return "", fmt.Errorf("timed out waiting for browser login (%.0f min)", ssoCallbackTimeout.Minutes())
	}
}

type callbackPageData struct {
	Title        string
	LogoSVG      template.HTML
	Success      bool
	ErrorMessage string
	ErrorDetail  string
}

type jwtClaims struct {
	AccountID string // from account_id claim
	Subdomain string // from account_metadata.<accountId>.subdomain (may be empty)
}

// parseJWT extracts claims from the JWT payload (no signature verification —
// the server will reject an invalid token; this is just for local display/storage).
//
// Confirmed claims from id.harness.io/idp/realms/HarnessIDP:
//
//	account_id          — Harness account ID
//	email               — user email
//	name / given_name   — display name
//	preferred_username  — login username
//	sub                 — Keycloak user UUID (not the Harness account ID)
//	scope               — includes "organization:<accountId>" as well
//	account_metadata.<accountId>.subdomain   — vanity subdomain (e.g. "prod2.harness.io"), may be empty during platform transition
//	account_metadata.<accountId>.clusterId   — e.g. "prod2"
//	account_metadata.<accountId>.accountName — human-readable account name
func parseJWT(rawToken string) (*jwtClaims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT (expected 3 segments)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("parsing JWT claims: %w", err)
	}
	hlog.Debug("JWT claims", "payload", string(payload))

	var accountID string
	for _, key := range []string{"accountID", "account_id", "accountId"} {
		if v, ok := raw[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				accountID = s
				break
			}
		}
	}
	if accountID == "" {
		return nil, fmt.Errorf("JWT does not contain an accountID claim — contact your Harness administrator")
	}

	var subdomain string
	if meta, ok := raw["account_metadata"].(map[string]any); ok {
		if acctMeta, ok := meta[accountID].(map[string]any); ok {
			if vals, ok := acctMeta["subdomain"].([]any); ok && len(vals) > 0 {
				subdomain, _ = vals[0].(string)
			}
		}
	}

	return &jwtClaims{AccountID: accountID, Subdomain: subdomain}, nil
}
