// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/modules/code"
	"github.com/harness/harness-cli/modules/core"
	"github.com/harness/harness-cli/modules/iacm"
	"github.com/harness/harness-cli/modules/pipeline"
	"github.com/harness/harness-cli/pkg/registry"
	"github.com/harness/harness-cli/pkg/rootcmd"
	"github.com/harness/harness-cli/pkg/spec"
	"github.com/harness/harness-cli/pkg/specloader"
)

//go:embed noargs.txt
var noargsText string

func main() {
	reg := registry.New()
	reg.IsMainBinary = true
	if err := specloader.LoadSpecs(reg); err != nil {
		console.PrintError(err.Error())
		os.Exit(1)
	}
	code.ModuleInit(reg.Module("code"))
	core.ModuleInit(reg.Module("core"))
	pipeline.ModuleInit(reg.Module("pipeline"))
	// har is an external module (external_binary: harness-har) — ModuleInit is not loaded here.
	iacm.ModuleInit(reg.Module("iacm"))
	rootcmd.MaybeCheckSpecs(reg)

	root := &cobra.Command{
		Use:   "harness",
		Short: "Harness CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print(strings.ReplaceAll(noargsText, "{{modules}}", renderModules(reg.GetModuleMetas())))
			return nil
		},
	}
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !cmd.HasParent() {
			fmt.Print(strings.ReplaceAll(noargsText, "{{modules}}", renderModules(reg.GetModuleMetas())))
			return
		}
		defaultHelp(cmd, args)
	})
	rootcmd.SetupAndExecuteRootCmd(root, reg)
}

func renderModules(metas []spec.ModuleMeta) string {
	var visible []spec.ModuleMeta
	for _, m := range metas {
		if !m.Core {
			visible = append(visible, m)
		}
	}

	// find longest name for alignment
	maxLen := 0
	for _, m := range visible {
		if len(m.Name) > maxLen {
			maxLen = len(m.Name)
		}
	}

	var sb strings.Builder
	for _, m := range visible {
		fmt.Fprintf(&sb, "  %-*s  %s\n", maxLen, m.Name, m.Desc)
	}
	return sb.String()
}
