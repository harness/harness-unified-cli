// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/harness/harness-cli/pkg/hlog"
)

// ErrSSOSessionExpired is returned when the refresh token is expired and the
// user must run 'harness auth loginsso' to obtain a new session.
var ErrSSOSessionExpired = fmt.Errorf("SSO session expired — run 'harness auth loginsso' to log in again")

const (
	SSOAuthServerBase      = "https://id.harness.io"
	ssoMetadataPath        = "/.well-known/oauth-authorization-server"
	SSOClientID            = "harness-cli-client"
	ssoDiscoverTimeout     = 10 * time.Second
	ssoTokenTimeout        = 30 * time.Second
	AccessTokenGracePeriod = 15 * time.Second
)

// AuthServerMeta holds the endpoints from OAuth2 authorization server discovery.
type AuthServerMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// FetchAuthServerMeta retrieves OAuth2 authorization server metadata via discovery.
func FetchAuthServerMeta(c *http.Client, authServerBaseURL string) (*AuthServerMeta, error) {
	metaURL := authServerBaseURL + ssoMetadataPath
	resp, err := c.Get(metaURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, metaURL)
	}
	var meta AuthServerMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("parsing authorization server metadata: %w", err)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, fmt.Errorf("authorization server metadata missing required endpoints")
	}
	// If the issuer is a sub-path (e.g. a Keycloak realm), re-fetch metadata from
	// the issuer's own discovery doc so we get the real realm endpoints.
	if meta.Issuer != "" && meta.Issuer != authServerBaseURL {
		return FetchAuthServerMeta(c, meta.Issuer)
	}
	return &meta, nil
}

// ExchangeCode exchanges an authorization code for access and refresh tokens.
func ExchangeCode(tokenEndpoint, clientID, code, verifier, redirectURI string) (accessToken, refreshToken string, err error) {
	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("redirect_uri", redirectURI)
	params.Set("client_id", clientID)
	params.Set("code_verifier", verifier)
	return doTokenRequest(tokenEndpoint, params)
}

// RefreshSSOToken exchanges a refresh token for a new access token (and possibly
// a new refresh token). If the server does not return a new refresh token, the
// original is returned unchanged.
func RefreshSSOToken(oldRefreshToken string) (accessToken, refreshToken string, err error) {
	meta, err := FetchAuthServerMeta(&http.Client{Timeout: ssoDiscoverTimeout}, SSOAuthServerBase)
	if err != nil {
		return "", "", fmt.Errorf("SSO discovery failed: %w", err)
	}
	params := url.Values{}
	params.Set("grant_type", "refresh_token")
	params.Set("refresh_token", oldRefreshToken)
	params.Set("client_id", SSOClientID)
	newAccess, newRefresh, err := doTokenRequest(meta.TokenEndpoint, params)
	if err != nil {
		return "", "", fmt.Errorf("token refresh failed: %w", err)
	}
	if newRefresh == "" {
		newRefresh = oldRefreshToken
	}
	return newAccess, newRefresh, nil
}

// CheckAndUpdateAccessToken checks whether the SSO access token in r is expiring
// soon and, if so, refreshes it. On a successful refresh the credentials file and
// r are both updated in place. No-ops for PAT profiles or env-sourced auth.
func CheckAndUpdateAccessToken(r *ResolvedAuth, now time.Time) error {
	if r.AuthType != AuthTypeSSO {
		return nil
	}
	if !strings.HasPrefix(r.Source, "profile:") {
		return nil
	}
	if !IsAccessTokenExpiringSoon(r.SSOToken, now) {
		return nil
	}

	// Check refresh token expiry before attempting the network round-trip.
	if IsAccessTokenExpiringSoon(r.RefreshToken, now) {
		return ErrSSOSessionExpired
	}

	newAccess, newRefresh, err := RefreshSSOToken(r.RefreshToken)
	if err != nil {
		return fmt.Errorf("SSO access token is expired and token refresh failed: %w", err)
	}

	profileName := strings.TrimPrefix(r.Source, "profile:")
	if err := SetSSOCredentials(profileName, newAccess, newRefresh); err != nil {
		return fmt.Errorf("saving refreshed credentials: %w", err)
	}

	r.SSOToken = newAccess
	r.RefreshToken = newRefresh
	return nil
}

// AccessTokenExpiry returns the expiration time embedded in the JWT's "exp" claim.
func AccessTokenExpiry(rawToken string) (time.Time, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT (expected 3 segments)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var raw struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return time.Time{}, fmt.Errorf("parsing JWT claims: %w", err)
	}
	if raw.Exp == 0 {
		return time.Time{}, fmt.Errorf("JWT has no exp claim")
	}
	return time.Unix(int64(raw.Exp), 0), nil
}

// IsAccessTokenExpiringSoon reports whether rawToken expires within AccessTokenGracePeriod.
// Returns true (treat as expired) if the expiry cannot be determined.
func IsAccessTokenExpiringSoon(rawToken string, now time.Time) bool {
	exp, err := AccessTokenExpiry(rawToken)
	if err != nil {
		return true
	}
	return now.After(exp.Add(-AccessTokenGracePeriod))
}

// GenerateCodeVerifier generates a random PKCE code verifier.
func GenerateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CodeChallenge derives the S256 PKCE challenge from a verifier.
func CodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// RandomState generates a random OAuth2 state parameter.
func RandomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func doTokenRequest(tokenEndpoint string, params url.Values) (accessToken, refreshToken string, err error) {
	c := &http.Client{Timeout: ssoTokenTimeout}
	resp, err := c.Post(tokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(params.Encode()))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", "", fmt.Errorf("parsing token response: %w", err)
	}
	if tok.Error != "" {
		return "", "", fmt.Errorf("%s: %s", tok.Error, tok.ErrorDesc)
	}
	if tok.AccessToken == "" {
		return "", "", fmt.Errorf("token response missing access_token")
	}

	hlog.Debug("token exchange", "grant", params.Get("grant_type"), "has_refresh_token", tok.RefreshToken != "")
	return tok.AccessToken, tok.RefreshToken, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
