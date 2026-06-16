// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import (
	"fmt"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
)

// mergePRBodyFn builds the merge request body for execute pr:merge.
// The API requires source_sha as a safety check, so we fetch the PR first.
func mergePRBodyFn(ctx *cmdctx.Ctx) (any, error) {
	if len(ctx.IdParts) < 2 {
		return nil, fmt.Errorf("expected <repo_id>/<pr_number>")
	}
	repoID := ctx.IdParts[0]
	prNumber := ctx.IdParts[1]

	c := client.New(ctx.Context, ctx.Auth)
	params := map[string]string{
		"accountIdentifier": ctx.Auth.AccountID,
		"orgIdentifier":     ctx.Auth.OrgID,
		"projectIdentifier": ctx.Auth.ProjectID,
	}
	raw, _, err := c.Get(fmt.Sprintf("/code/api/v1/repos/%s/pullreq/%s", repoID, prNumber), params)
	if err != nil {
		return nil, fmt.Errorf("fetching PR to get source SHA: %w", err)
	}

	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected PR response type")
	}
	sourceSHA, _ := m["source_sha"].(string)
	if sourceSHA == "" {
		return nil, fmt.Errorf("PR response missing source_sha")
	}

	return map[string]any{
		"source_sha":           sourceSHA,
		"method":               cmdctx.GetString(ctx.FlagValues, "method"),
		"delete_source_branch": cmdctx.GetBool(ctx.FlagValues, "delete-branch"),
		"dry_run":              cmdctx.GetBool(ctx.FlagValues, "dry-run"),
	}, nil
}
