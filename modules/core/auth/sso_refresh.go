// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/format"
)

func SSORefreshHandler(ctx *cmdctx.Ctx) error {
	profileFlag := cmdctx.GetString(ctx.FlagValues, "profile")

	resolved, err := auth.Load(profileFlag)
	if err != nil {
		return err
	}
	if resolved.AuthType != auth.AuthTypeSSO {
		return fmt.Errorf("profile %q does not use SSO — sso_refresh only applies to SSO profiles", resolved.Source)
	}
	if !strings.HasPrefix(resolved.Source, "profile:") {
		return fmt.Errorf("sso_refresh requires a saved profile, not env-var auth")
	}

	fmt.Println("Refreshing SSO token...")

	newAccess, newRefresh, err := auth.RefreshSSOToken(resolved.RefreshToken)
	if err != nil {
		return fmt.Errorf("%w\n\nRun 'harness auth loginsso' to log in again", err)
	}

	profileName := strings.TrimPrefix(resolved.Source, "profile:")
	if err := auth.SetSSOCredentials(profileName, newAccess, newRefresh); err != nil {
		return fmt.Errorf("saving refreshed credentials: %w", err)
	}

	fmt.Println()
	printTokenExpiry(newAccess, newRefresh)
	return nil
}

func printTokenExpiry(ssoToken, refreshToken string) {
	var rows []format.LabeledValue
	add := func(label, value string) {
		rows = append(rows, format.LabeledValue{Label: label, Value: value})
	}
	add("SSO Token", formatTokenExpiry(ssoToken))
	add("Refresh Token", formatTokenExpiry(refreshToken))
	format.WriteLabeledValues(os.Stdout, rows)
}

func formatTokenExpiry(token string) string {
	if token == "" {
		return "not set"
	}
	exp, err := auth.AccessTokenExpiry(token)
	if err != nil {
		return fmt.Sprintf("unknown (%v)", err)
	}
	remaining := time.Until(exp)
	if remaining <= 0 {
		return fmt.Sprintf("%s %s (expired %s ago)",
			console.RedX(), exp.Local().Format(time.RFC3339), (-remaining).Round(time.Second))
	}
	return fmt.Sprintf("%s %s (expires in %s)",
		console.GreenCheck(), exp.Local().Format(time.RFC3339), remaining.Round(time.Second))
}
