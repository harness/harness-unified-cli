// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
)

func SSOStatusHandler(ctx *cmdctx.Ctx) error {
	profileFlag := cmdctx.GetString(ctx.FlagValues, "profile")

	resolved, err := auth.Load(profileFlag)
	if err != nil {
		return err
	}
	if resolved.AuthType != auth.AuthTypeSSO {
		return fmt.Errorf("profile %q does not use SSO — sso_status only applies to SSO profiles", resolved.Source)
	}

	fmt.Println("Token expiry:")
	printTokenExpiry(resolved.SSOToken, resolved.RefreshToken)

	fmt.Fprintln(os.Stdout)
	fmt.Println("SSO token claims:")
	if err := printJWTClaims(resolved.SSOToken); err != nil {
		fmt.Fprintf(os.Stderr, "  (could not decode: %v)\n", err)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Println("Refresh token claims:")
	if err := printJWTClaims(resolved.RefreshToken); err != nil {
		fmt.Fprintf(os.Stderr, "  (could not decode: %v)\n", err)
	}

	return nil
}

func printJWTClaims(token string) error {
	if token == "" {
		fmt.Println("  not set")
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a JWT (expected 3 segments)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("decoding payload: %w", err)
	}
	var claims any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("parsing claims: %w", err)
	}
	out, err := json.MarshalIndent(claims, "", "  ")
	if err != nil {
		return fmt.Errorf("formatting claims: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
