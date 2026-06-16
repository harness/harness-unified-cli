// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import (
	"fmt"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
)

// createPRCommentBodyFn builds the comment create body.
// -f/--file (or -f -) supplies the comment text as plain text.
// --reply-to sets parent_id for threaded replies.
func createPRCommentBodyFn(ctx *cmdctx.Ctx) (any, error) {
	text, err := cmdctx.SlurpInputFile(ctx.FlagValues)
	if err != nil {
		return nil, fmt.Errorf("comment text: use -f <file> or -f - to read from stdin")
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil, fmt.Errorf("comment text is empty")
	}

	body := map[string]any{"text": text}

	if replyTo := cmdctx.GetInt(ctx.FlagValues, "reply-to"); replyTo != 0 {
		body["parent_id"] = replyTo
	}

	return body, nil
}
