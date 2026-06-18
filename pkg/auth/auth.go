// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/harness/harness-cli/pkg/hbase"
)

// AuthType identifies how the token in the credentials file was obtained.
// Empty string (existing profiles) is treated as AuthTypePAT.
type AuthType = string

const (
	AuthTypePAT = "pat" // default; omitted from YAML for existing profiles
	AuthTypeSSO = "sso" // OAuth2 JWT obtained via browser login
)

type Profile struct {
	APIUrl      string   `yaml:"api_url"`
	AccountID   string   `yaml:"account_id"`
	OrgID       string   `yaml:"org_id,omitempty"`
	ProjectID   string   `yaml:"project_id,omitempty"`
	RegistryURL string   `yaml:"registry_url,omitempty"`
	AuthType    AuthType `yaml:"auth_type,omitempty"` // omitted for existing PAT profiles
}

type Config struct {
	Profiles map[string]*Profile `yaml:"profiles"`
}

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

func LoadConfig() (*Config, error) {
	path := hbase.GetConfigFilePath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Profiles: make(map[string]*Profile)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]*Profile)
	}
	return &cfg, nil
}

func SaveConfig(cfg *Config) error {
	path := hbase.GetConfigFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(path, data, 0600)
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
	cfg, err := LoadConfig()
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

// ValidateAPIURL returns an error if apiURL is not a parseable URL with a host.
func ValidateAPIURL(apiURL string) error {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a valid URL", apiURL)
	}
	return nil
}

var patSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidatePATFormat returns an error if token does not match pat.<accountId>.<tokenId>.<secret>.
func ValidatePATFormat(token string) error {
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 4 || parts[0] != "pat" {
		return fmt.Errorf("invalid PAT format — expected pat.<accountId>.<tokenId>.<secret>")
	}
	for _, p := range parts[1:] {
		if !patSegment.MatchString(p) {
			return fmt.Errorf("invalid PAT format — segments must match [A-Za-z0-9_-]+")
		}
	}
	return nil
}

// AccountIDFromToken extracts the account ID from a valid PAT of the form pat.{AccountID}.x.y.
// Callers must validate the token with ValidatePATFormat before calling this.
func AccountIDFromToken(token string) string {
	parts := strings.SplitN(token, ".", 4)
	if len(parts) == 4 && parts[0] == "pat" {
		return parts[1]
	}
	return ""
}
