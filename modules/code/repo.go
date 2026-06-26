// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import (
	"fmt"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
)

// DefaultBranch returns the default branch name for the given repository.
func DefaultBranch(cc *cmdctx.Ctx, repoID string) (string, error) {
	c := client.New(cc)
	raw, _, err := c.Get("/code/api/v1/repos/"+repoID, nil)
	if err != nil {
		return "", fmt.Errorf("fetching repo %q: %w", repoID, err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected response type from repo endpoint")
	}
	branch, ok := m["default_branch"].(string)
	if !ok || branch == "" {
		return "", fmt.Errorf("repo %q has no default_branch in response", repoID)
	}
	return branch, nil
}
