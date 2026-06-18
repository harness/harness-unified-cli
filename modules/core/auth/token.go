// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"os"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
)

func TokenHandler(ctx *cmdctx.Ctx) error {
	profileFlag := cmdctx.GetString(ctx.FlagValues, "profile")

	resolved, err := auth.Load(profileFlag)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout, resolved.PATToken)
	return nil
}
