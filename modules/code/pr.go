// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import (
	"fmt"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// createPRBodyFn builds the pull request create body.
// Required: --set title=<title> source_branch=<branch> target_branch=<branch>
// Optional: --set description=<text>  OR  -f <file> for multi-line description.
func createPRBodyFn(ctx *cmdctx.Ctx) (any, error) {
	title := ctx.SetArgs["title"]
	if title == "" {
		return nil, fmt.Errorf("--set title=<title> is required")
	}
	sourceBranch := ctx.SetArgs["source_branch"]
	if sourceBranch == "" {
		return nil, fmt.Errorf("--set source_branch=<branch> is required")
	}
	targetBranch := ctx.SetArgs["target_branch"]
	if targetBranch == "" {
		targetBranch = "main"
	}

	description := ctx.SetArgs["description"]

	// -f / --file overrides --set description when provided
	if fileText, err := cmdctx.SlurpInputFile(ctx.FlagValues); err == nil && strings.TrimSpace(fileText) != "" {
		description = strings.TrimRight(fileText, "\n")
	}

	isDraft := ctx.SetArgs["is_draft"] == "true"

	body := map[string]any{
		"title":         title,
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"description":   description,
		"is_draft":      isDraft,
	}
	return body, nil
}
