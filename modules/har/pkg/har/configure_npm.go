// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

const configureRegistryHandlerID = "configure_registry"

func configureRegistryHandler(ctx *cmdctx.Ctx) error {
	client := cmdctx.GetString(ctx.FlagValues, "client")
	if client == "" {
		return fmt.Errorf("--client is required (supported: npm)")
	}
	switch client {
	case "npm":
		return configureNpm(ctx)
	default:
		return fmt.Errorf("unsupported client %q (supported: npm)", client)
	}
}

func configureNpm(ctx *cmdctx.Ctx) error {
	a := ctx.Auth
	registryID := ctx.Id
	scope := cmdctx.GetString(ctx.FlagValues, "scope")
	global := cmdctx.GetBool(ctx.FlagValues, "global")

	if scope != "" && !strings.HasPrefix(scope, "@") {
		scope = "@" + scope
	}

	registryURL := fmt.Sprintf("%s/pkg/%s/%s/npm", a.RegistryURL, a.AccountID, registryID)

	if !global {
		if _, err := os.Stat("package.json"); os.IsNotExist(err) {
			return fmt.Errorf("package.json not found in current directory — run from your npm project root or use --global")
		}
	}

	npmrcPath, err := npmrcPath(global)
	if err != nil {
		return err
	}

	if err := writeNpmrc(npmrcPath, registryURL, scope, a.PATToken); err != nil {
		return fmt.Errorf("writing .npmrc: %w", err)
	}

	loc := npmrcPath
	if global {
		loc = "~/.npmrc"
	}
	if scope != "" {
		fmt.Printf("Configured npm scope %s → %s (%s)\n", scope, registryURL, loc)
	} else {
		fmt.Printf("Configured npm default registry → %s (%s)\n", registryURL, loc)
	}
	return nil
}

func npmrcPath(global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("getting home directory: %w", err)
		}
		return filepath.Join(home, ".npmrc"), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting current directory: %w", err)
	}
	return filepath.Join(cwd, ".npmrc"), nil
}

func writeNpmrc(npmrcPath, registryURL, scope, authToken string) error {
	parsedURL, err := url.Parse(registryURL)
	if err != nil {
		return fmt.Errorf("invalid registry URL: %w", err)
	}
	registryHost := parsedURL.Host + parsedURL.Path

	var scopeRegistryLine string
	if scope != "" {
		scopeRegistryLine = fmt.Sprintf("%s:registry=%s/", scope, registryURL)
	} else {
		scopeRegistryLine = fmt.Sprintf("registry=%s/", registryURL)
	}
	authTokenLine := fmt.Sprintf("//%s/:_authToken=%s", registryHost, authToken)
	alwaysAuthLine := "always-auth=true"

	var existingContent []byte
	hasExisting := false
	if _, err := os.Stat(npmrcPath); err == nil {
		existingContent, err = os.ReadFile(npmrcPath)
		if err != nil {
			return fmt.Errorf("reading existing .npmrc: %w", err)
		}
		if len(strings.TrimSpace(string(existingContent))) > 0 {
			hasExisting = true
		}
	}

	scopeFound := false
	authFound := false
	var lines []string

	if hasExisting {
		for _, line := range strings.Split(string(existingContent), "\n") {
			trimmed := strings.TrimSpace(line)
			if scope != "" && strings.HasPrefix(trimmed, scope+":registry=") {
				lines = append(lines, scopeRegistryLine)
				scopeFound = true
			} else if scope == "" && strings.HasPrefix(trimmed, "registry=") && !strings.Contains(trimmed, ":") {
				lines = append(lines, scopeRegistryLine)
				scopeFound = true
			} else if strings.HasPrefix(trimmed, "//"+registryHost+"/:_authToken=") {
				lines = append(lines, authTokenLine)
				authFound = true
			} else if trimmed != "" {
				lines = append(lines, line)
			}
		}
	}

	if !scopeFound || !authFound {
		if hasExisting && len(lines) > 0 {
			lines = append(lines, "")
		}
		if !scopeFound {
			lines = append(lines, scopeRegistryLine)
		}
		if !authFound {
			lines = append(lines, authTokenLine)
		}
		lines = append(lines, alwaysAuthLine)
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return os.WriteFile(npmrcPath, []byte(content), 0600)
}
