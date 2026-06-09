// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/exprenv"
	"github.com/harness/harness-cli/pkg/plugin"
	"github.com/harness/harness-cli/pkg/spec"
)

// execPluginCompletion execs the plugin binary with the original os.Args so that
// __complete is handled by the plugin process (which has IsMainBinary=false and
// will run the completion handler directly instead of re-delegating).
// Returns true if the exec succeeded (the process is replaced); on error it
// returns false so the caller can fall through to a no-op directive.
func execPluginCompletion(r *Registry, module string) ([]string, cobra.ShellCompDirective) {
	extBin := r.externalBinaryFor(module)
	if extBin == "" {
		panic(fmt.Sprintf("execPluginCompletion: no external binary registered for module %q", module))
	}
	binPath, err := plugin.FindBinary(extBin)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	// plugin.Exec replaces the process; if it returns, something went wrong.
	_ = plugin.Exec(binPath, os.Args[1:])
	return nil, cobra.ShellCompDirectiveError
}

// wireCompletion attaches a ValidArgsFunction to cmd. If the command has a
// CompletionSeq, multi-part slash-delimited completion is used; otherwise the
// single-noun completion driven by CompletionNoun (or the command's own Noun) is used.
func (r *Registry) wireCompletion(cmd *cobra.Command, cs *spec.CommandSpec) {
	if len(cs.CompletionSeq) > 0 {
		r.wireSeqCompletion(cmd, cs)
		return
	}
	if cs.CompletionNoun == "-" {
		return
	}
	targetNoun := cs.CompletionNoun
	if targetNoun == "" {
		targetNoun = cs.Noun
	}

	nd := r.GetNoun(targetNoun)
	isMultiLevel := nd != nil && nd.MultiLevel
	isList := verbRegistry[cs.Verb].AllowsParentId

	// For multi-level list commands with no existing completion handler, inject
	// static scope sentinels. If there is a completion endpoint, they are prepended.
	if isMultiLevel && isList {
		listSpec := r.GetSpec(VerbList, targetNoun)
		if listSpec == nil || listSpec.Endpoint == nil || listSpec.Endpoint.Completion == nil {
			// No dynamic completions — just offer the scope sentinels.
			cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
				if cs.External {
					return execPluginCompletion(r, cs.Module)
				}
				if len(args) > 0 {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				return []string{"account", "org"}, cobra.ShellCompDirectiveNoFileComp
			}
			return
		}
		ep := listSpec.Endpoint
		cspec := ep.Completion
		cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if cs.External {
				return execPluginCompletion(r, cs.Module)
			}
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			ctx, err := r.buildCompletionCtx(cmd, VerbList, targetNoun, toComplete)
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			items, err := fetchCompletionItems(ctx, ep)
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			completions, err := extractCompletions(items, cspec, exprenv.Make(ctx))
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			return append([]string{"account", "org"}, completions...), cobra.ShellCompDirectiveNoFileComp
		}
		return
	}

	listSpec := r.GetSpec(VerbList, targetNoun)
	if listSpec == nil || listSpec.Endpoint == nil || listSpec.Endpoint.Completion == nil {
		return
	}
	ep := listSpec.Endpoint
	cspec := ep.Completion

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if cs.External {
			return execPluginCompletion(r, cs.Module)
		}
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		// For multi-level nouns (e.g. get registry), the user may type a scope
		// prefix like "account." or "org." before the id. Detect it so we can
		// fetch at the right scope and prefix the returned completions.
		scopeArg := ""
		scopePrefix := ""
		if isMultiLevel {
			if strings.HasPrefix(toComplete, "account.") {
				scopeArg = "account."
				scopePrefix = "account."
			} else if strings.HasPrefix(toComplete, "org.") {
				scopeArg = "org."
				scopePrefix = "org."
			}
		}

		ctx, err := r.buildCompletionCtx(cmd, VerbList, targetNoun, scopeArg)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		items, err := fetchCompletionItems(ctx, ep)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		completions, err := extractCompletions(items, cspec, exprenv.Make(ctx))
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		if scopePrefix != "" {
			for i, c := range completions {
				completions[i] = scopePrefix + c
			}
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// wireSeqCompletion attaches a ValidArgsFunction that completes slash-delimited
// multi-part IDs one segment at a time. Each CompletionSeqStep maps to one slash-
// separated segment. Typing "reg/" advances to step 1 which filters artifacts by
// the registry typed before the slash. Intermediate steps return NoSpace so the
// shell appends the next character rather than inserting a space.
func (r *Registry) wireSeqCompletion(cmd *cobra.Command, cs *spec.CommandSpec) {
	steps := cs.CompletionSeq

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if cs.External {
			return execPluginCompletion(r, cs.Module)
		}
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		parts := strings.Split(toComplete, "/")
		stepIdx := len(parts) - 1
		if stepIdx >= len(steps) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		step := steps[stepIdx]
		prefix := strings.Join(parts[:stepIdx], "/")
		if prefix != "" {
			prefix += "/"
		}

		listSpec := r.GetSpec(VerbList, step.CompletionNoun)
		if listSpec == nil || listSpec.Endpoint == nil || listSpec.Endpoint.Completion == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ep := listSpec.Endpoint
		cspec := ep.Completion

		ctx, err := r.buildCompletionCtx(cmd, VerbList, step.CompletionNoun, strings.Join(parts[:stepIdx], "/"))
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		items, err := fetchCompletionItems(ctx, ep)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		completions, err := extractCompletions(items, cspec, exprenv.Make(ctx))
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		isLastStep := stepIdx == len(steps)-1
		directive := cobra.ShellCompDirectiveNoFileComp
		if step.KeepOrder {
			directive |= cobra.ShellCompDirectiveKeepOrder
		}
		if !isLastStep {
			directive |= cobra.ShellCompDirectiveNoSpace
		}

		// Prepend the already-typed prefix and, for non-final steps, insert "/"
		// after the completion value (before the tab-separated description) so the
		// shell lands ready for the next segment without inserting a space.
		for i, c := range completions {
			if !isLastStep {
				if tabIdx := strings.Index(c, "\t"); tabIdx >= 0 {
					c = c[:tabIdx] + "/" + c[tabIdx:]
				} else {
					c += "/"
				}
			}
			completions[i] = prefix + c
		}
		return completions, directive
	}
}

// wireFlagCompletions registers RegisterFlagCompletionFunc for each flag in cs that
// declares a completion_noun, completion_fn, or completion_values.
func (r *Registry) wireFlagCompletions(cmd *cobra.Command, cs *spec.CommandSpec) {
	for _, f := range cs.Flags {
		if len(f.CompletionValues) == 0 && f.CompletionFn == "" && f.CompletionNoun == "" {
			continue
		}
		r.wireFlagCompletion(cmd, cs, f)
	}
}

func (r *Registry) wireFlagCompletion(cmd *cobra.Command, cs *spec.CommandSpec, f spec.Flag) {
	if len(f.CompletionValues) > 0 {
		values := f.CompletionValues
		cmd.RegisterFlagCompletionFunc(f.Name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return values, cobra.ShellCompDirectiveNoFileComp
		})
		return
	}

	if f.CompletionFn != "" {
		completionFn := r.flagCompletionFns[f.CompletionFn]
		if completionFn == nil {
			if cs.External {
				cmd.RegisterFlagCompletionFunc(f.Name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
					return execPluginCompletion(r, cs.Module)
				})
			}
			return
		}
		cmd.RegisterFlagCompletionFunc(f.Name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			profileFlag, _ := cmd.Flags().GetString("profile")
			resolved, err := auth.Resolve(profileFlag)
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			if orgFlag, _ := cmd.Flags().GetString("org"); orgFlag != "" {
				resolved.OrgID = orgFlag
			}
			if projectFlag, _ := cmd.Flags().GetString("project"); projectFlag != "" {
				resolved.ProjectID = projectFlag
			}
			completions, err := completionFn(resolved, args, cmd.Flags())
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			return completions, cobra.ShellCompDirectiveNoFileComp
		})
		return
	}

	if cs.External {
		cmd.RegisterFlagCompletionFunc(f.Name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return execPluginCompletion(r, cs.Module)
		})
		return
	}
	listSpec := r.GetSpec(VerbList, f.CompletionNoun)
	if listSpec == nil || listSpec.Endpoint == nil || listSpec.Endpoint.Completion == nil {
		return
	}
	ep := listSpec.Endpoint
	cspec := ep.Completion
	cmd.RegisterFlagCompletionFunc(f.Name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		parentId := ""
		if f.ParentFromArg >= 0 && f.ParentFromArg < len(args) {
			parentId = args[f.ParentFromArg]
		}
		ctx, err := r.buildCompletionCtx(cmd, VerbList, f.CompletionNoun, parentId)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		items, err := fetchCompletionItems(ctx, ep)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		completions, err := extractCompletions(items, cspec, exprenv.Make(ctx))
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	})
}

// wireProfileCompletion registers tab-completion for the --profile flag by reading the local config file.
func wireProfileCompletion(cmd *cobra.Command) {
	cmd.RegisterFlagCompletionFunc("profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		cfg, err := auth.LoadConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		names := make([]string, 0, len(cfg.Profiles))
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	})
}

// extractCompletions applies id_expr/name_expr to pre-extracted items to produce completion strings.
// Each entry is formatted as "id\tname" so shells display the name as a description.
func extractCompletions(items []any, cspec *spec.CompletionSpec, exprEnv map[string]any) ([]string, error) {
	out := make([]string, 0, len(items))
	for _, item := range items {
		itemEnv := exprenv.WithIt(exprEnv, item)
		idRaw, ok := exprenv.EvalExprAny(itemEnv, cspec.IdExpr)
		if !ok || idRaw == nil {
			continue
		}
		id := fmt.Sprint(idRaw)
		if id == "" {
			continue
		}
		entry := id
		if cspec.NameExpr != "" {
			if nameRaw, ok := exprenv.EvalExprAny(itemEnv, cspec.NameExpr); ok && nameRaw != nil {
				if name := fmt.Sprint(nameRaw); name != "" && name != id {
					entry = id + "\t" + name
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
