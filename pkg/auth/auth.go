// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/harness/harness-cli/pkg/config"
	"github.com/harness/harness-cli/pkg/hbase"
)

// AuthType is re-exported from pkg/config for callers that only import pkg/auth.
type AuthType = config.AuthType

const (
	AuthTypePAT = config.AuthTypePAT
	AuthTypeSSO = config.AuthTypeSSO
)

const SourceEnv = "env"

// ResolvedAuth is the result of auth resolution — the active credentials for a command invocation.
// Credential fields are never printed; callers that display auth context must omit them.
type ResolvedAuth struct {
	Source      string   // "profile:<name>" or SourceEnv
	AuthType    AuthType // AuthTypePAT or AuthTypeSSO
	APIUrl      string
	AccountID   string
	OrgID       string
	ProjectID   string
	RegistryURL string

	// Exactly one of these is set depending on AuthType.
	PATToken     string // set when AuthType == AuthTypePAT
	SSOToken     string // set when AuthType == AuthTypeSSO
	RefreshToken string // set when AuthType == AuthTypeSSO
}

// Load populates a ResolvedAuth following the 4-step resolution order from auth.md.
// It never errors on missing optional fields — callers get whatever could be populated.
// Use Validate to check that the result is complete enough to make API calls.
func Load(profileFlag string) (*ResolvedAuth, error) {
	// 1. --profile flag wins entirely; all auth env vars are ignored
	if profileFlag != "" {
		return resolveProfile(profileFlag)
	}
	// 2. HARNESS_API_KEY → env var mode, no config file read
	if key := os.Getenv(hbase.EnvAPIKey); key != "" {
		apiURL := os.Getenv(hbase.EnvAPIURL)
		if apiURL == "" {
			apiURL = hbase.DefaultAPIURL
		}
		registryURL := os.Getenv(hbase.EnvRegistryURL)
		if registryURL == "" {
			registryURL = hbase.DefaultRegistryURL
		}
		acct := os.Getenv(hbase.EnvAccount)
		if acct == "" {
			acct = AccountIDFromToken(key)
		}
		return &ResolvedAuth{
			Source:      SourceEnv,
			PATToken:    key,
			AccountID:   acct,
			OrgID:       os.Getenv(hbase.EnvOrg),
			ProjectID:   os.Getenv(hbase.EnvProject),
			APIUrl:      apiURL,
			RegistryURL: registryURL,
		}, nil
	}
	// 3. HARNESS_PROFILE env var → named profile from config
	if name := os.Getenv(hbase.EnvProfile); name != "" {
		return resolveProfile(name)
	}
	// 4. default profile
	return resolveProfile("default")
}

// Validate checks that a ResolvedAuth is complete enough to make API calls.
func Validate(r *ResolvedAuth) error {
	if r.AuthType == AuthTypeSSO {
		if r.SSOToken == "" {
			return fmt.Errorf("no token found for profile — run 'harness auth loginsso' to re-authenticate")
		}
	} else {
		if r.PATToken == "" {
			return fmt.Errorf("no token found for profile — run 'harness auth login' to re-authenticate")
		}
		if err := ValidatePATFormat(r.PATToken); err != nil {
			if r.Source == SourceEnv {
				return fmt.Errorf("%s is invalid: %w", hbase.EnvAPIKey, err)
			}
			return fmt.Errorf("stored token is invalid — run 'harness auth login' to re-authenticate: %w", err)
		}
		if tokenAcct := AccountIDFromToken(r.PATToken); tokenAcct != "" && r.AccountID != tokenAcct {
			if r.Source == SourceEnv {
				return fmt.Errorf("%s %q does not match account in token %q", hbase.EnvAccount, r.AccountID, tokenAcct)
			}
			return fmt.Errorf("stored account %q does not match token — run 'harness auth login' to re-authenticate", r.AccountID)
		}
	}
	if r.OrgID == "" {
		if r.Source == SourceEnv {
			return fmt.Errorf("org is required in env mode — set %s", hbase.EnvOrg)
		}
		return fmt.Errorf("profile has no org — run 'harness auth setscope' to configure it")
	}
	if r.ProjectID == "" {
		if r.Source == SourceEnv {
			return fmt.Errorf("project is required in env mode — set %s", hbase.EnvProject)
		}
		return fmt.Errorf("profile has no project — run 'harness auth setscope' to configure it")
	}
	return nil
}

// Resolve loads and validates credentials. Used by all normal commands.
func Resolve(profileFlag string) (*ResolvedAuth, error) {
	r, err := Load(profileFlag)
	if err != nil {
		return nil, err
	}
	if err := Validate(r); err != nil {
		return nil, err
	}
	return r, nil
}

func resolveProfile(name string) (*ResolvedAuth, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, err
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		if name == "default" {
			return nil, errors.New("not logged in — run 'harness auth login' to get started")
		}
		return nil, fmt.Errorf("profile %q not found", name)
	}
	creds, err := LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}
	profileCreds := creds[name]
	if profileCreds == nil || profileCreds.Token == "" {
		return nil, fmt.Errorf("no token found for profile %q — run 'harness auth login' to re-authenticate", name)
	}
	apiURL := p.APIUrl
	if apiURL == "" {
		apiURL = hbase.DefaultAPIURL
	}
	registryURL := p.RegistryURL
	if registryURL == "" {
		registryURL = hbase.DefaultRegistryURL
	}
	authType := p.AuthType
	if authType == "" {
		authType = AuthTypePAT
	}
	r := &ResolvedAuth{
		Source:      "profile:" + name,
		AuthType:    authType,
		APIUrl:      apiURL,
		AccountID:   p.AccountID,
		OrgID:       p.OrgID,
		ProjectID:   p.ProjectID,
		RegistryURL: registryURL,
	}
	if authType == AuthTypeSSO {
		r.SSOToken = profileCreds.Token
		r.RefreshToken = profileCreds.RefreshToken
	} else {
		r.PATToken = profileCreds.Token
	}
	return r, nil
}

var (
	hostLabelRE   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`)
	harnessHostRE = regexp.MustCompile(`^https://([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)+harness\.io$`)
	harnessNameRE = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)+harness\.io$`)
)

// NormalizeAPIURL recognizes two shorthand forms and expands them:
//   - bare label (e.g. "harness0")          → "https://harness0.harness.io"
//   - FQDN under harness.io (e.g. "app.harness.io") → "https://app.harness.io"
//
// Any other input is returned unchanged; ValidateAPIURL will reject it.
func NormalizeAPIURL(s string) string {
	s = strings.TrimSpace(s)
	if hostLabelRE.MatchString(s) {
		return "https://" + s + ".harness.io"
	}
	if harnessNameRE.MatchString(s) {
		return "https://" + s
	}
	return s
}

// ValidateAPIURL returns an error if apiURL is not a valid Harness API URL
// of the form https://<host>.harness.io (no path, no trailing slash).
func ValidateAPIURL(apiURL string) error {
	if !harnessHostRE.MatchString(apiURL) {
		return fmt.Errorf("%q is not a valid Harness API URL — expected https://<host>.harness.io", apiURL)
	}
	return nil
}

// TokenKind identifies the type of a Harness token.
type TokenKind string

const (
	TokenKindPAT     TokenKind = "pat"
	TokenKindSAT     TokenKind = "sat"
	TokenKindJWT     TokenKind = "jwt"
	TokenKindUnknown TokenKind = ""
)

// jwtSegment matches a single base64url-encoded JWT segment (header, payload, or signature).
var jwtSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TokenType returns the kind of token (PAT, SAT, JWT, or unknown) by inspecting its structure.
// JWT detection is structural only: exactly 3 non-empty base64url segments separated by dots.
func TokenType(token string) TokenKind {
	switch {
	case strings.HasPrefix(token, "pat."):
		return TokenKindPAT
	case strings.HasPrefix(token, "sat."):
		return TokenKindSAT
	default:
		parts := strings.Split(token, ".")
		if len(parts) == 3 {
			isJWT := true
			for _, p := range parts {
				if len(p) == 0 || !jwtSegment.MatchString(p) {
					isJWT = false
					break
				}
			}
			if isJWT {
				return TokenKindJWT
			}
		}
		return TokenKindUnknown
	}
}

var patSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidatePATFormat returns an error if token does not match pat.<accountId>.<tokenId>.<secret>
// or sat.<accountId>.<tokenId>.<secret>.
func ValidatePATFormat(token string) error {
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 4 || (parts[0] != "pat" && parts[0] != "sat") {
		return fmt.Errorf("invalid PAT/SAT format — expected pat.<accountId>.<tokenId>.<secret> or sat.<accountId>.<tokenId>.<secret>")
	}
	for _, p := range parts[1:] {
		if !patSegment.MatchString(p) {
			return fmt.Errorf("invalid PAT/SAT format — segments must match [A-Za-z0-9_-]+")
		}
	}
	return nil
}

// AccountIDFromToken extracts the account ID from a valid PAT/SAT of the form pat.{AccountID}.x.y
// or sat.{AccountID}.x.y. Callers must validate the token with ValidatePATFormat before calling this.
func AccountIDFromToken(token string) string {
	if TokenType(token) == TokenKindUnknown {
		return ""
	}
	parts := strings.SplitN(token, ".", 4)
	if len(parts) == 4 {
		return parts[1]
	}
	return ""
}

// MaskedToken returns a display-safe representation of a token.
// For PAT/SAT tokens it reveals <prefix>.<accountID> and masks the remaining segments.
// For anything else it falls back to Masked.
func MaskedToken(s string) string {
	kind := TokenType(s)
	if kind != TokenKindPAT && kind != TokenKindSAT {
		return Masked(s)
	}
	parts := strings.SplitN(s, ".", 4)
	if len(parts) == 4 {
		return parts[0] + "." + parts[1] + "." + strings.Repeat("•", len(parts[2])) + "." + strings.Repeat("•", len(parts[3]))
	}
	return Masked(s)
}

// Masked returns a display-safe representation of an arbitrary secret string
// by replacing every character with a bullet.
func Masked(s string) string {
	return strings.Repeat("•", len(s))
}
