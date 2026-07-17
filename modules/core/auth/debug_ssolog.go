// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/harness/cli/pkg/auth"
	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/hbase"
	"github.com/harness/cli/pkg/logstream"
)

// debugSSOLogBodyLimit caps how much of each response body we print inline.
// The curl command is the deliverable; the inline output is just a preview.
const debugSSOLogBodyLimit = 2000

// DebugSSOLogHandler force-refreshes the SSO token, then for every log key of an
// execution prints a ready-to-paste curl command followed by a truncated preview
// of firing that request in-process. Because refresh and fetch happen back-to-back
// the inline calls never hit the ~2min token expiry; the printed curl is minted
// fresh each run so it can be copy/pasted and retried by hand.
func DebugSSOLogHandler(ctx *cmdctx.Ctx) error {
	if ctx.Id == "" {
		return fmt.Errorf("missing required argument <[pipeline/]execId>")
	}
	execId := logstream.ExecIdFromArg([]string{ctx.Id})
	if execId == "" {
		return fmt.Errorf("could not parse execId from %q", ctx.Id)
	}

	profileFlag := cmdctx.GetString(ctx.FlagValues, "profile")

	// Try a quiet refresh first (fast path when the refresh token is still valid).
	// If the profile is missing/PAT or the refresh fails (expired refresh token),
	// fall back to the full interactive SSO login, which creates or updates the
	// profile (org/project wizard) and saves credentials.
	resolved, err := auth.Load(profileFlag)
	needLogin := err != nil || resolved.AuthType != auth.AuthTypeSSO || !strings.HasPrefix(resolved.Source, "profile:")

	if !needLogin {
		profileName := strings.TrimPrefix(resolved.Source, "profile:")
		fmt.Fprintln(os.Stderr, "Refreshing SSO token...")
		newAccess, newRefresh, refErr := auth.RefreshSSOToken(resolved.RefreshToken)
		if refErr != nil {
			fmt.Fprintf(os.Stderr, "Refresh failed (%v) — starting interactive SSO login...\n", refErr)
			needLogin = true
		} else if err := auth.SetSSOCredentials(profileName, newAccess, newRefresh); err != nil {
			return fmt.Errorf("saving refreshed credentials: %w", err)
		} else {
			resolved.SSOToken = newAccess
			resolved.RefreshToken = newRefresh
		}
	}

	if needLogin {
		// LoginSSOHandler owns profile creation/update, the org/project wizard, and
		// credential save. Force overwrite so it doesn't prompt for an existing profile.
		ctx.FlagValues["overwrite"] = true
		if err := LoginSSOHandler(ctx); err != nil {
			return err
		}
		resolved, err = auth.Load(profileFlag)
		if err != nil {
			return fmt.Errorf("reloading profile after login: %w", err)
		}
		if resolved.AuthType != auth.AuthTypeSSO {
			return fmt.Errorf("profile %q does not use SSO after login", resolved.Source)
		}
	}

	// HARNESS_SSO_BASE_URL is baked into the profile at login time; re-apply it here
	// so the emitted URLs match the override used in the repro even for profiles
	// created without it.
	if v := os.Getenv(hbase.EnvSSOBaseURL); v != "" {
		resolved.APIUrl = v
	}

	// FetchLogKeys and the graph fetch read ctx.Auth (this command is no_auth).
	ctx.Auth = resolved

	fmt.Fprintln(os.Stderr, "WARNING: output below contains a live bearer token.")
	fmt.Fprintln(os.Stderr)

	entries, _, err := logstream.FetchLogKeys(ctx, execId)
	if err != nil {
		return fmt.Errorf("fetching log keys: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no log keys found for execution %q", execId)
	}

	hc := &http.Client{Timeout: 30 * time.Second}
	for _, e := range entries {
		label := e.FQN
		if label == "" {
			label = e.Name
		}
		blobURL := buildBlobURL(resolved, e.LogKey)

		fmt.Printf("== %s ==\n", label)
		fmt.Printf("curl -H \"Authorization: Bearer %s\" \"%s\"\n\n", resolved.SSOToken, blobURL)

		status, body, err := fireBlobRequest(hc, resolved, blobURL)
		if err != nil {
			fmt.Printf("(request error: %v)\n\n", err)
			continue
		}
		fmt.Printf("HTTP %d, %d bytes\n", status, len(body))
		if len(body) > 0 {
			fmt.Println(truncateBody(body, debugSSOLogBodyLimit))
		}
		fmt.Println()
	}

	fmt.Println("Token expiry:")
	printTokenExpiry(resolved.SSOToken, resolved.RefreshToken)
	return nil
}

// buildBlobURL constructs the log-service blob URL exactly as FetchAndPrintLog does.
func buildBlobURL(a *auth.ResolvedAuth, logKey string) string {
	u, err := url.Parse(a.APIUrl + "/gateway/log-service/blob")
	if err != nil {
		return a.APIUrl + "/gateway/log-service/blob?accountID=" + url.QueryEscape(a.AccountID) + "&key=" + url.QueryEscape(logKey)
	}
	q := u.Query()
	q.Set("accountID", a.AccountID)
	q.Set("key", logKey)
	u.RawQuery = q.Encode()
	return u.String()
}

func fireBlobRequest(hc *http.Client, a *auth.ResolvedAuth, blobURL string) (int, string, error) {
	req, err := http.NewRequest("GET", blobURL, nil)
	if err != nil {
		return 0, "", err
	}
	a.SetAuthHeader(req)
	resp, err := hc.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}

func truncateBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... (truncated, %d bytes total)", len(s))
}
