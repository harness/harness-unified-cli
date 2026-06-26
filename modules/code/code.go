// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import "github.com/harness/harness-cli/pkg/registry"

const (
	mergePRBodyFnID         = "merge_pr_body"
	createPRCommentBodyFnID = "create_pr_comment_body"
	createPRBodyFnID        = "create_pr_body"
)

func ModuleInit(reg registry.ModuleRegistrar) {
	reg.RegisterBodyFn(mergePRBodyFnID, mergePRBodyFn)
	reg.RegisterBodyFn(createPRCommentBodyFnID, createPRCommentBodyFn)
	reg.RegisterBodyFn(createPRBodyFnID, createPRBodyFn)
}
