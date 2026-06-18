// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
)

var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func LoginHandler(ctx *cmdctx.Ctx) error {
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

	apiURL := cmdctx.GetString(ctx.FlagValues, "api-url")
	token := cmdctx.GetString(ctx.FlagValues, "api-token")
	accountID := cmdctx.GetString(ctx.FlagValues, "account")
	orgID := cmdctx.GetString(ctx.FlagValues, "org")
	projectID := cmdctx.GetString(ctx.FlagValues, "project")
	noValidate := cmdctx.GetBool(ctx.FlagValues, "no-validate")

	const defaultAPIURL = "https://app.harness.io"

	// isInteractive: both stdin+stdout are TTYs and at least one required value is missing.
	isInteractive := console.IsBothTTY() && (apiURL == "" || token == "")

	// Load config early so we can check for an existing profile.
	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}

	var registryURL string

	if isInteractive {
		// Run the bubbletea wizard — handles URL, PAT, validation, org/project pickers.
		var existing *WizardExisting
		if existingProfile, exists := cfg.Profiles[profileName]; exists {
			switch {
			case noOverwrite:
				return fmt.Errorf("profile %q already exists (use --overwrite to replace it)", profileName)
			case !overwrite:
				fmt.Fprintf(os.Stderr, "WARNING: profile %q already exists, continuing will overwrite it\n\n", profileName)
				if !console.PromptYesNo("Overwrite?") {
					return fmt.Errorf("Canceled by user — config not written")
				}
				fmt.Fprintln(os.Stderr)
			}
			// Load existing token so the wizard can offer "use existing".
			existingURL := existingProfile.APIUrl
			existingToken := ""
			if creds, cerr := auth.LoadCredentials(); cerr == nil {
				if c := creds[profileName]; c != nil {
					existingToken = c.Token
				}
			}
			existing = &WizardExisting{APIURL: existingURL, Token: existingToken}
		}

		result, err := RunLoginWizard(ctx, existing)
		if err != nil {
			return err
		}
		if result == nil {
			return fmt.Errorf("Canceled by user — config not written")
		}

		apiURL = result.APIURL
		token = result.Token
		accountID = result.Account
		registryURL = result.RegURL
		if orgID == "" {
			orgID = result.OrgID
		}
		if projectID == "" {
			projectID = result.Project
		}
	} else {
		// Non-interactive: all values from flags/env.
		if _, exists := cfg.Profiles[profileName]; exists {
			switch {
			case noOverwrite:
				return fmt.Errorf("profile %q already exists (use --overwrite to replace it)", profileName)
			case overwrite:
				// silent
			default:
				return fmt.Errorf("profile %q already exists — pass --overwrite or --no-overwrite", profileName)
			}
		}

		fmt.Fprintf(os.Stderr, "Logging in for profile %q\n\n", profileName)

		if ctx.IsPty {
			// pty but all flags provided — validate URL only
			if apiURL == "" {
				apiURL = defaultAPIURL
			} else if err := auth.ValidateAPIURL(apiURL); err != nil {
				return err
			}
		} else {
			if token == "" {
				return fmt.Errorf("not a terminal — pass --api-token (and --api-url if not using the default)")
			}
			if apiURL == "" {
				apiURL = defaultAPIURL
			} else if err := auth.ValidateAPIURL(apiURL); err != nil {
				return err
			}
		}

		if token == "" {
			return fmt.Errorf("API token is required")
		}

		if err := auth.ValidatePATFormat(token); err != nil {
			return fmt.Errorf("invalid token: %w", err)
		}
		tokenAccountID := auth.AccountIDFromToken(token)
		if accountID == "" {
			accountID = tokenAccountID
		} else if accountID != tokenAccountID {
			return fmt.Errorf("--account %q does not match account ID in token (%q)", accountID, tokenAccountID)
		}

		if noValidate {
			fmt.Fprintln(os.Stderr, "Warning: token validation skipped — credentials written but not verified")
		} else {
			if err := validateToken(apiURL, token, accountID); err != nil {
				return err
			}
		}

		registryURL, err = fetchRegistryURL(apiURL, token, accountID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch registry URL: %v\n", err)
		}
	}

	cfg.Profiles[profileName] = &auth.Profile{
		APIUrl:      apiURL,
		AccountID:   accountID,
		OrgID:       orgID,
		ProjectID:   projectID,
		RegistryURL: registryURL,
	}
	if err := auth.SaveConfig(cfg); err != nil {
		return fmt.Errorf("saving profile: %w", err)
	}
	if err := auth.SetCredential(profileName, token); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Printf("Logged in. Profile %q written.\n\n", profileName)
	printStatus(runStatusChecks(profileName))
	return nil
}

// fetchRegistryURL calls GET /gateway/har/api/v3/system/info to get the package registry base URL.
// Returns empty string (not an error) when the field is absent — the caller falls back gracefully.
func fetchRegistryURL(apiURL, token, accountID string) (string, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("%s/gateway/har/api/v3/system/info?account_identifier=%s", apiURL, accountID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("x-api-key", token)

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var parsed struct {
		Data struct {
			RegistryURL string `json:"registryUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	return parsed.Data.RegistryURL, nil
}

// validateToken calls GET /ng/api/accounts/{accountID} to verify the token.
func validateToken(apiURL, token, accountID string) error {
	c := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("%s/ng/api/accounts/%s?accountIdentifier=%s", apiURL, accountID, accountID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("building validation request: %w", err)
	}
	req.Header.Set("x-api-key", token)

	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s — check your API URL: %w", apiURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 200:
		return nil
	case 401:
		return fmt.Errorf("token rejected (401) — check that your API token is valid")
	case 403:
		return fmt.Errorf("token valid but access denied (403) — check account ID or RBAC permissions")
	default:
		// Try to extract a message from JSON
		var parsed struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
			return fmt.Errorf("validation failed (%d): %s", resp.StatusCode, parsed.Message)
		}
		return fmt.Errorf("validation failed with status %d", resp.StatusCode)
	}
}
