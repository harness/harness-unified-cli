// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
)

func SetHandler(ctx *cmdctx.Ctx) error {
	profileName := cmdctx.GetString(ctx.FlagValues, "profile")
	if profileName == "" {
		profileName = "default"
	}
	orgID := cmdctx.GetString(ctx.FlagValues, "org")
	projectID := cmdctx.GetString(ctx.FlagValues, "project")

	if orgID == "" && projectID == "" {
		if !console.IsBothTTY() {
			return fmt.Errorf("nothing to set — pass --org and/or --project")
		}
		return setInteractive(ctx, profileName)
	}

	return setFlags(profileName, orgID, projectID)
}

func setInteractive(ctx *cmdctx.Ctx, profileName string) error {
	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[profileName]
	if !ok {
		return fmt.Errorf("profile %q not found — run 'harness auth login' first", profileName)
	}
	creds, err := auth.LoadCredentials()
	if err != nil {
		return err
	}
	profileCreds := creds[profileName]
	if profileCreds == nil || profileCreds.Token == "" {
		return fmt.Errorf("no token found for profile %q — run 'harness auth login' first", profileName)
	}
	token := profileCreds.Token

	result, err := RunSetWizard(ctx, &SetWizardInput{
		APIURL:    p.APIUrl,
		Token:     token,
		AccountID: p.AccountID,
		AuthType:  p.AuthType,
		RegURL:    p.RegistryURL,
		OrgID:     p.OrgID,
		ProjectID: p.ProjectID,
	})
	if err != nil {
		return err
	}
	if result == nil {
		fmt.Println("Cancelled.")
		return nil
	}
	return setFlags(profileName, result.OrgID, result.Project)
}

func setFlags(profileName, orgID, projectID string) error {
	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[profileName]
	if !ok {
		return fmt.Errorf("profile %q not found — run 'harness auth login' first", profileName)
	}

	if orgID != "" {
		p.OrgID = orgID
	}
	if projectID != "" {
		p.ProjectID = projectID
	}

	if err := auth.SaveConfig(cfg); err != nil {
		return fmt.Errorf("saving profile: %w", err)
	}

	fmt.Printf("Profile %q updated.\n\n", profileName)
	printStatus(runStatusChecks(profileName))
	return nil
}
