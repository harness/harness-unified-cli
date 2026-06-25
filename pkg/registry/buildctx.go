// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/hbase"
	"github.com/harness/harness-cli/pkg/spec"
)

// timeoutGracePeriod is the window given for in-flight work to finish after
// the timeout fires before the process is hard-killed with exit code 124.
const timeoutGracePeriod = time.Second

// completionTimeout is the default timeout for completion requests.
const completionTimeout = 5.0

// runTimeout sleeps for secs seconds, cancels the context with a timeout cause,
// then hard-exits with code 124 after timeoutGracePeriod. Intended to run in a goroutine.
func runTimeout(secs float64, cancel context.CancelCauseFunc) {
	time.Sleep(time.Duration(float64(time.Second) * secs))
	cancel(&cmdctx.TimeoutError{Secs: secs})
	time.Sleep(timeoutGracePeriod)
	os.Exit(hbase.TimeoutExitCode)
}

// parseScopePrefix inspects a raw id/parentId arg and returns the stripped value
// and the detected scope level. For list verbs, bare "account" / "org" are valid
// sentinels (returns "", level). For all other verbs a prefix is required to set
// a non-default level — bare "account"/"org" are treated as literal ids.
func parseScopePrefix(raw string, isList bool) (stripped string, level string) {
	if strings.HasPrefix(raw, "account.") {
		return strings.TrimPrefix(raw, "account."), "account"
	}
	if strings.HasPrefix(raw, "org.") {
		return strings.TrimPrefix(raw, "org."), "org"
	}
	if isList && raw == "account" {
		return "", "account"
	}
	if isList && raw == "org" {
		return "", "org"
	}
	return raw, "project"
}

// buildCompletionCtx constructs the Ctx needed for completion handlers.
// It resolves auth from the command's --profile/--org/--project flags.
// parentId is optional — pass "" when not applicable.
func (r *Registry) buildCompletionCtx(cmd *cobra.Command, verb, noun, parentId string) (*cmdctx.Ctx, error) {
	profileFlag, _ := cmd.Flags().GetString("profile")
	orgFlag, _ := cmd.Flags().GetString("org")
	projectFlag, _ := cmd.Flags().GetString("project")
	resolved, err := auth.Resolve(profileFlag)
	if err != nil {
		return nil, err
	}
	resolved.OrgID = firstNonEmpty(orgFlag, resolved.OrgID)
	resolved.ProjectID = firstNonEmpty(projectFlag, resolved.ProjectID)
	ctx, cancel := context.WithCancelCause(context.Background())
	go runTimeout(completionTimeout, cancel)

	level := ""
	nd := r.GetNoun(noun)
	if nd != nil && nd.MultiLevel {
		if parentId != "" {
			parentId, level = parseScopePrefix(parentId, true)
		}
		if levelFlag, _ := cmd.Flags().GetString("level"); levelFlag != "" {
			if level != "" && level != "project" && level != levelFlag {
				// prefix and --level disagree — return an error so callers can bail on completions
				return nil, fmt.Errorf("--level %q conflicts with %q prefix", levelFlag, level)
			}
			level = levelFlag
		}
	}

	return &cmdctx.Ctx{
		Context:      ctx,
		CancelFn:     cancel,
		Verb:         verb,
		Noun:         noun,
		ParentId:     parentId,
		Level:        level,
		Auth:         resolved,
		Resolver:     r,
		IsCompletion: true,
	}, nil
}

// buildCtx constructs a Ctx from a cobra command, resolving auth and global flags.
func buildCtx(cmd *cobra.Command, cs *spec.CommandSpec, args []string, r *Registry) (*cmdctx.Ctx, error) {
	formatFlag, _ := cmd.Flags().GetString("format")
	jsonFlag, _ := cmd.Flags().GetBool("json")
	yamlFlag, _ := cmd.Flags().GetBool("yaml")
	if jsonFlag && yamlFlag {
		return nil, fmt.Errorf("--json and --yaml are mutually exclusive")
	}
	if (jsonFlag || yamlFlag) && formatFlag != "" {
		return nil, fmt.Errorf("--json/--yaml and --format are mutually exclusive")
	}
	if jsonFlag {
		formatFlag = "json"
	}
	if yamlFlag {
		formatFlag = "yaml"
	}
	columnsFlag, _ := cmd.Flags().GetString("columns")
	fieldsFlag, _ := cmd.Flags().GetString("fields")
	if fieldsFlag != "" && (jsonFlag || yamlFlag) {
		return nil, fmt.Errorf("--fields and --json/--yaml are mutually exclusive")
	}
	if fieldsFlag != "" && formatFlag != "" {
		return nil, fmt.Errorf("--fields and --format are mutually exclusive")
	}
	noHeaders, _ := cmd.Flags().GetBool("no-headers")
	outFile, _ := cmd.Flags().GetString("out")
	rawFlag, _ := cmd.Flags().GetBool("raw")

	timeoutSecs, _ := cmd.Flags().GetFloat64("timeout")
	if timeoutSecs < 0 {
		return nil, fmt.Errorf("--timeout must be >= 0 (got %g)", timeoutSecs)
	}

	goCtx, cancel := context.WithCancelCause(context.Background())
	if timeoutSecs > 0 {
		go runTimeout(timeoutSecs, cancel)
	}
	ctx := &cmdctx.Ctx{
		Context:     goCtx,
		CancelFn:    cancel,
		Verb:        cs.Verb,
		VerbHandler: cs.VerbHandler,
		Noun:        cs.Noun,
		FieldsNoun:  cs.FieldsNoun,
		IsPty:       term.IsTerminal(int(os.Stdout.Fd())),
		FormatFlags: cmdctx.FormatFlags{
			Format:    formatFlag,
			Columns:   columnsFlag,
			Fields:    fieldsFlag,
			NoHeaders: noHeaders,
			OutFile:   outFile,
			Raw:       rawFlag,
		},
	}
	listFields, _ := cmd.Flags().GetBool("list-fields")
	listColumns, _ := cmd.Flags().GetBool("list-columns")
	uiFlag, _ := cmd.Flags().GetBool("ui")
	skipIdCheck := listFields || listColumns || uiFlag

	idLabel := cs.IdLabel
	if idLabel == "" {
		idLabel = "<id>"
	}
	vspec := verbRegistry[cs.Verb]
	nd := r.GetNoun(cs.Noun)
	if vspec.RequiresId && !cs.NoId {
		if len(args) == 0 && !skipIdCheck {
			return nil, fmt.Errorf("%s %s requires a positional %s argument", cs.Verb, cs.Noun, idLabel)
		}
		if len(args) > 0 {
			ctx.Id = args[0]
		}
	} else if vspec.AllowsId {
		if cs.RequiresId && len(args) == 0 && !skipIdCheck {
			return nil, fmt.Errorf("%s %s requires a positional %s argument", cs.Verb, cs.Noun, idLabel)
		}
		if len(args) > 0 {
			ctx.Id = args[0]
		}
	} else if vspec.AllowsParentId {
		if len(args) > 0 {
			ctx.ParentId = args[0]
		} else if cs.RequiresParentId && !skipIdCheck {
			label := cs.ParentIdLabel
			if label == "" {
				label = "<parentid>"
			}
			return nil, fmt.Errorf("%s %s requires a positional %s argument", cs.Verb, cs.Noun, label)
		}
	}
	if nd != nil && nd.MultiLevel {
		if vspec.AllowsParentId && ctx.ParentId != "" {
			ctx.ParentId, ctx.Level = parseScopePrefix(ctx.ParentId, true)
		} else if ctx.Id != "" {
			ctx.Id, ctx.Level = parseScopePrefix(ctx.Id, false)
		}
		if levelFlag, _ := cmd.Flags().GetString("level"); levelFlag != "" {
			valid := false
			for _, v := range specLevelValues {
				if levelFlag == v {
					valid = true
					break
				}
			}
			if !valid {
				return nil, fmt.Errorf("invalid --level %q: must be one of %s", levelFlag, strings.Join(specLevelValues, ", "))
			}
			if ctx.Level != "" && ctx.Level != "project" && ctx.Level != levelFlag {
				return nil, fmt.Errorf("--level %q conflicts with %q prefix on id", levelFlag, ctx.Level)
			}
			ctx.Level = levelFlag
		}
	}
	if err := validateIdParts(cs, vspec, ctx); err != nil {
		return nil, err
	}
	if !cs.NoAuth {
		profileFlag, _ := cmd.Flags().GetString("profile")
		orgFlag, _ := cmd.Flags().GetString("org")
		projectFlag, _ := cmd.Flags().GetString("project")
		resolved, err := auth.Resolve(profileFlag)
		if err != nil {
			return nil, err
		}
		resolved.OrgID = firstNonEmpty(orgFlag, resolved.OrgID)
		resolved.ProjectID = firstNonEmpty(projectFlag, resolved.ProjectID)
		ctx.Auth = resolved
	}
	if cs.HasArgs {
		extra := args
		if vspec.RequiresId || vspec.AllowsId {
			if len(args) > 0 {
				extra = args[1:]
			} else {
				extra = nil
			}
		}
		ctx.Args = extra
	}
	if cs.BuiltinFlags.Set {
		setVals, _ := cmd.Flags().GetStringArray("set")
		// positional args after the id are also treated as key=value pairs
		positional := args
		if vspec.RequiresId || vspec.AllowsId {
			if len(args) > 0 {
				positional = args[1:]
			} else {
				positional = nil
			}
		}
		all := append(setVals, positional...)
		if len(all) > 0 {
			ctx.SetArgs = make(map[string]string, len(all))
			for _, kv := range all {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return nil, fmt.Errorf("invalid value %q: expected key=value format", kv)
				}
				ctx.SetArgs[k] = v
			}
		}
	}
	if cs.BuiltinFlags.Del {
		delVals, _ := cmd.Flags().GetStringArray("del")
		if len(delVals) > 0 {
			ctx.DelArgs = delVals
		}
	}
	ctx.FlagValues = buildFlagValues(cmd.Flags(), cs)
	ctx.Resolver = r
	for _, f := range cs.Flags {
		if f.Required && cmdctx.GetString(ctx.FlagValues, f.Name) == "" {
			if len(f.CompletionValues) > 0 {
				return nil, fmt.Errorf("flag --%s is required (%s)", f.Name, strings.Join(f.CompletionValues, ", "))
			}
			return nil, fmt.Errorf("flag --%s is required", f.Name)
		}
	}
	return ctx, nil
}

// buildDetailCtx constructs a minimal Ctx for a get-by-id drilldown from inside
// the TUI. It copies auth and scope from the parent list ctx, overrides the verb/noun
// to "get", and injects the resolved id.
func buildDetailCtx(parent *cmdctx.Ctx, cs *spec.CommandSpec, id string) *cmdctx.Ctx {
	goCtx, cancel := context.WithCancelCause(parent.Context)
	return &cmdctx.Ctx{
		Context:     goCtx,
		CancelFn:    cancel,
		Auth:        parent.Auth,
		Verb:        cs.Verb,
		VerbHandler: cs.VerbHandler,
		Noun:        cs.Noun,
		FieldsNoun:  cs.FieldsNoun,
		Id:          id,
		Level:       parent.Level,
		IsPty:       parent.IsPty,
		Resolver:    parent.Resolver,
		FormatFlags: cmdctx.FormatFlags{Format: "text"},
		FlagValues:  map[string]any{},
	}
}

func validateIdParts(cs *spec.CommandSpec, vspec VerbSpec, ctx *cmdctx.Ctx) error {
	val, label := ctx.Id, cs.IdLabel
	if vspec.AllowsParentId {
		val = ctx.ParentId
		if cs.ParentIdLabel != "" {
			label = "<" + cs.ParentIdLabel + ">"
		}
	}
	if label == "" {
		label = "<id>"
	}
	if val == "" || cs.IdAllowSlash {
		return nil
	}
	allowed := max(cs.IdParts-1, 0)
	if got := strings.Count(val, "/"); got > allowed {
		if cs.IdParts > 1 {
			return fmt.Errorf("expected %s with exactly %d parts separated by '/', got %q", label, cs.IdParts, val)
		}
		return fmt.Errorf("%s %s: %s must not contain '/' (got %q)", cs.Verb, cs.Noun, label, val)
	}
	if cs.IdParts > 1 {
		ctx.IdParts = strings.SplitN(val, "/", cs.IdParts)
	}
	return nil
}
