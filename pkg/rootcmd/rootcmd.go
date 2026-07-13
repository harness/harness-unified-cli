// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package rootcmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/hbase"
	"github.com/harness/harness-cli/pkg/hlog"
	"github.com/harness/harness-cli/modules/core/mgmt"
	"github.com/harness/harness-cli/pkg/registry"
	"github.com/harness/harness-cli/pkg/spec"
	"github.com/harness/harness-cli/pkg/specloader"
	"github.com/harness/harness-cli/pkg/release"
	"github.com/harness/harness-cli/pkg/telemetry"
)

// MaybeRunBackgroundUpdateCheck exits if this invocation is the background update subprocess.
func MaybeRunBackgroundUpdateCheck() {
	for _, arg := range os.Args[1:] {
		if arg == release.FlagName {
			release.RunBackgroundCheck()
			os.Exit(0)
		}
	}
}

// postInstallFlag is the hidden flag install.sh invokes right after placing a
// fresh binary on disk, purely to fire a cli_installed telemetry event.
const postInstallFlag = "--post-install"

// MaybeRunPostInstall exits if this invocation is install.sh's post-install
// telemetry ping. Respects the same opt-out as every other event.
func MaybeRunPostInstall() {
	for _, arg := range os.Args[1:] {
		if arg == postInstallFlag {
			flush := telemetry.Init()
			telemetry.RecordInstall(telemetry.InstallEvent{
				RunID:       hbase.RunID,
				InstallType: telemetry.ResolveInstallType(),
				Env:         telemetry.NewEnv(),
			})
			flush()
			os.Exit(0)
		}
	}
}

// MaybeCheckSpecs runs spec validation and exits if HARNESS_CHECKSPECS=1, otherwise returns immediately.
func MaybeCheckSpecs(reg *registry.Registry) {
	if os.Getenv(hbase.EnvCheckSpecs) != "1" {
		return
	}
	if err := reg.CheckFunctions(); err != nil {
		console.PrintError(err.Error())
		os.Exit(1)
	}
	for _, w := range reg.CheckWarnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	var names []string
	for _, m := range reg.GetModuleMetas() {
		names = append(names, m.Name)
	}
	fmt.Printf("specs ok [%s]\n", strings.Join(names, ", "))
	os.Exit(0)
}

// SetupAndExecutePluginRootCmd is like SetupAndExecuteRootCmd but adds hidden
// --spec and --modulehelp flags for use by the plugin host.
func SetupAndExecutePluginRootCmd(root *cobra.Command, reg *registry.Registry, moduleName string) {
	if os.Getenv(hbase.EnvDebugCompletion) == "1" && isCompletionInvocation() {
		hlog.SetDebugFile(hbase.CompletionDebugLogFile)
	}
	hlog.SetPlugin(moduleName)
	root.Flags().Bool("spec", false, "Dump the module spec YAML to stdout")
	root.Flags().Lookup("spec").Hidden = true
	root.Flags().Bool("modulehelp", false, "Dump the rendered module help text to stdout")
	root.Flags().Lookup("modulehelp").Hidden = true

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the " + moduleName + " plugin version",
		RunE: func(cmd *cobra.Command, args []string) error {
			bt := hbase.BuildTime
			if bt == "" {
				bt = "dev"
			}
			fmt.Printf("harness-%s version %s (%s)\n", moduleName, hbase.Version, bt)
			return nil
		},
	})

	pluginMsg := fmt.Sprintf("harness-%s is a plugin for the Harness CLI — it is not meant to be run directly.\nUse: harness <verb> <noun> [flags]\n\nTo explore %s commands:\n  harness get module %s\n", moduleName, moduleName, moduleName)

	origRun := root.RunE
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if ok, _ := cmd.Flags().GetBool("spec"); ok {
			return dumpSpec(moduleName)
		}
		if ok, _ := cmd.Flags().GetBool("modulehelp"); ok {
			return dumpModuleHelp(moduleName, reg)
		}
		if origRun != nil {
			return origRun(cmd, args)
		}
		fmt.Print(pluginMsg)
		return nil
	}
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !cmd.HasParent() {
			fmt.Print(pluginMsg)
			return
		}
		defaultHelp(cmd, args)
	})
	SetupAndExecuteRootCmd(root, reg)
}

func dumpSpec(moduleName string) error {
	data, err := specloader.ReadSpecFile(moduleName)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	return nil
}

func dumpModuleHelp(moduleName string, reg *registry.Registry) error {
	var meta *spec.ModuleMeta
	for _, m := range reg.GetModuleMetas() {
		if m.Name == moduleName {
			m := m
			meta = &m
			break
		}
	}
	if meta == nil || meta.HelpText == "" {
		return nil
	}
	var nouns []string
	seen := map[string]bool{}
	for _, n := range meta.NounOrder {
		if !seen[n] {
			seen[n] = true
			nouns = append(nouns, n)
		}
	}
	nounBlock := mgmt.RenderNounBlock(moduleName, nouns, reg)
	fmt.Print(strings.ReplaceAll(meta.HelpText, "{{nouns}}", nounBlock))
	return nil
}

// SetupAndExecuteRootCmd wires common flags, attaches commands, and executes root.
func SetupAndExecuteRootCmd(root *cobra.Command, reg *registry.Registry) {
	if path := os.Getenv(hbase.EnvLogFile); path != "" {
		hlog.SetLogFile(path)
	}
	reg.TelemetryEnv = telemetry.NewEnv()
	defer telemetry.Init()()
	if reg.IsMainBinary {
		release.NagIfDue(hbase.Version)
		release.MaybeSpawn()
	}
	bt := hbase.BuildTime
	if bt == "" {
		bt = "dev"
	}
	root.Version = fmt.Sprintf("%s (%s)", hbase.Version, bt)
	if os.Getenv(hbase.EnvDebugCompletion) == "1" && isCompletionInvocation() {
		hlog.SetDebugFile(hbase.CompletionDebugLogFile)
	}
	root.SilenceUsage = true
	root.SilenceErrors = true

	root.PersistentFlags().BoolFunc("debug", "Enable debug logging", func(string) error {
		if !isCompletionInvocation() {
			hlog.SetDebug()
		}
		return nil
	})
	root.PersistentFlags().Float64("timeout", 0, "Command timeout in seconds (0 = no timeout, e.g. 1.5)")
	reg.AttachGlobalAuthFlags(root)

	for _, cmd := range reg.BuildCommands() {
		root.AddCommand(cmd)
	}

	if err := root.Execute(); err != nil {
		// Only suggest an alternative command when cobra itself couldn't dispatch
		// (i.e. no runnable command was found). If cobra found and ran a command
		// handler, the error came from the handler — show it as-is.
		matched, _, _ := root.Find(os.Args[1:])
		commandResolved := matched != nil && matched != root && matched.Runnable()
		if !isCompletionInvocation() && !commandResolved {
			// emitBadUsage only for cobra parse-time failures; registry.emitError
			// already fires for handler errors.
			emitBadUsage(root, reg, err)
		}
		if !commandResolved {
			if suggestion := reg.SuggestRootCommand(os.Args[1:]); suggestion != "" {
				console.PrintError(suggestion)
				os.Exit(1)
			}
		}
		console.PrintError(err.Error())
		if cmdctx.IsTimeout(err) {
			os.Exit(hbase.TimeoutExitCode)
		}
		os.Exit(1)
	}
}

// emitBadUsage fires a CommandError for parse-time failures (unknown flag, unknown noun, bad args).
// It uses cobra's Find to resolve the deepest matched command so we get a canonical verb/noun.
// An unrecognized noun is never logged — we only record what cobra actually resolved.
func emitBadUsage(root *cobra.Command, reg *registry.Registry, err error) {
	matched, _, _ := root.Find(os.Args[1:])

	// Walk up to the verb command (depth 1 from root).
	verb, noun := "", ""
	cmd := matched
	for cmd != nil && cmd.HasParent() && cmd.Parent() != root {
		cmd = cmd.Parent()
	}
	if cmd != nil && cmd != root && cmd.HasParent() {
		verb = cmd.Name()
		if matched != nil && matched != cmd {
			noun = matched.Name()
		}
	}

	var category telemetry.ErrorCategory
	switch {
	case verb == "":
		category = telemetry.ErrorCategoryInvalidVerb
	case noun != "":
		category = telemetry.ErrorCategoryInvalidFlag
	default:
		category = telemetry.ErrorCategoryInvalidNoun
	}

	module := ""
	if cs := reg.GetSpec(verb, noun); cs != nil {
		module = cs.Module
	}

	telemetry.RecordError(telemetry.CommandError{
		Verb:     verb,
		Noun:     noun,
		Module:   module,
		Category: category,
		RunID:    hbase.RunID,
		Env:      reg.TelemetryEnv,
	})
}

func isCompletionInvocation() bool {
	for _, arg := range os.Args[1:] {
		if arg == "__complete" || arg == "__completeNoDesc" {
			return true
		}
	}
	return false
}
