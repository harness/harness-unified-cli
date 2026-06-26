// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

// Package exprenv builds and evaluates expr-lang environments for spec expressions.
package exprenv

import (
	"fmt"
	"maps"
	"strings"

	"github.com/expr-lang/expr"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/exprenv/exprfuncs"
	"github.com/harness/harness-cli/pkg/spec"
)

func isMachineFormat(flags map[string]any) bool {
	f, _ := flags["format"].(string)
	switch f {
	case "json", "jsonl", "csv", "tsv", "markdown", "ui":
		return true
	}
	return false
}

// WithIt returns a shallow copy of env with "it" set to val.
func WithIt(env map[string]any, val any) map[string]any {
	out := make(map[string]any, len(env)+1)
	maps.Copy(out, env)
	out["it"] = val
	return out
}

// Make builds the expr-lang environment from ctx.
// Flags are exposed as a flat map under "flags".
func Make(ctx *cmdctx.Ctx) map[string]any {
	a := ctx.Auth

	parts := []string{}
	if a != nil {
		for _, p := range []string{a.AccountID, a.OrgID, a.ProjectID} {
			if p != "" {
				parts = append(parts, p)
			}
		}
	}

	idParts := strings.Split(ctx.Id, "/")
	parentIdParts := strings.Split(ctx.ParentId, "/")

	flags := ctx.FlagValues
	if flags == nil {
		flags = map[string]any{}
	}

	env := map[string]any{
		"ctx": map[string]any{
			"id":            ctx.Id,
			"parentId":      ctx.ParentId,
			"idParts":       idParts,
			"parentIdParts": parentIdParts,
			"level":         ctx.Level,
			"args":          ctx.Args,
			"setArgs":       ctx.SetArgs,
			"delArgs":       ctx.DelArgs,
		},
		"auth": func() map[string]any {
			if a == nil {
				return map[string]any{"account": "", "org": "", "project": "", "scope": "", "ui_url": ""}
			}
			uiURL := a.UIUrl
			if uiURL == "" {
				uiURL = a.APIUrl
			}
			return map[string]any{
				"account": a.AccountID,
				"org":     a.OrgID,
				"project": a.ProjectID,
				"scope":   strings.Join(parts, "/"),
				"ui_url":  uiURL,
			}
		}(),
		"flags":                 flags,
		"lastPart":              exprfuncs.LastPart,
		"coalesce":              exprfuncs.Coalesce,
		"formatTags":            exprfuncs.FormatTags,
		"formatTagDisplay":      exprfuncs.FormatTagDisplay,
		"formatMetadata":        exprfuncs.FormatMetadata,
		"pipelineSparkline":     exprfuncs.NewPipelineSparkline(ctx.IsPty && !isMachineFormat(flags)),
		"statusIcon":            exprfuncs.NewStatusIcon(ctx.IsPty && !isMachineFormat(flags)),
		"spaceAfter":            exprfuncs.SpaceAfter,
		"duration":              exprfuncs.Duration,
		"harScopeUrl":           exprfuncs.HarScopeUrl,
		"epochMs":               exprfuncs.EpochMs,
		"parseDateMs":           exprfuncs.ParseDateMs,
		"jsonArray":             exprfuncs.JsonArray,
		"formatRoleAssignments": exprfuncs.FormatRoleAssignments,
		"formatRoleIds":         exprfuncs.FormatRoleIds,
		"truncate":              exprfuncs.Truncate,
		"substr":                exprfuncs.Substr,
		"formatOrder":           exprfuncs.FormatOrder,
	}
	if ctx.Resolver != nil {
		noun := ctx.Noun
		if ctx.FieldsNoun != "" {
			noun = ctx.FieldsNoun
		}
		injectUrlFn(env, ctx, ctx.Resolver.GetNoun(noun))
	}
	return env
}

// EvalItemsExpr evaluates s as an expr-lang expression expected to return a []any.
func EvalItemsExpr(env map[string]any, s string) ([]any, error) {
	program, err := expr.Compile(s, expr.Env(env), expr.AsAny())
	if err != nil {
		return nil, err
	}
	out, err := expr.Run(program, env)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []any{}, nil
	}
	items, ok := out.([]any)
	if !ok {
		return nil, fmt.Errorf("expression returned %T, want []any", out)
	}
	return items, nil
}

// EvalExprAny evaluates s as an expr-lang expression and returns the raw result.
// The bool is false when the expression fails or the result is nil.
func EvalExprAny(env map[string]any, s string) (any, bool) {
	program, err := expr.Compile(s, expr.Env(env), expr.AsAny())
	if err != nil {
		return nil, false
	}
	out, err := expr.Run(program, env)
	if err != nil || out == nil {
		return nil, false
	}
	return out, true
}

// EvalExpr evaluates s as an expr-lang expression, returning the result as a string.
// Returns "" when the expression fails or produces a nil/empty result.
func EvalExpr(env map[string]any, s string) string {
	out, ok := EvalExprAny(env, s)
	if !ok {
		return ""
	}
	s2 := fmt.Sprint(out)
	if s2 == "<nil>" || s2 == "map[]" {
		return ""
	}
	return s2
}

// ResolvePath evaluates all {{expr}} segments in a path template using the given
// expr environment, replacing each with its string result.
func ResolvePath(env map[string]any, path string) (string, error) {
	var result strings.Builder
	for {
		start := strings.Index(path, "{{")
		if start == -1 {
			result.WriteString(path)
			break
		}
		end := strings.Index(path[start+2:], "}}")
		if end == -1 {
			return "", fmt.Errorf("unclosed '{{' in path %q", path)
		}
		end += start + 2
		result.WriteString(path[:start])
		exprStr := strings.TrimSpace(path[start+2 : end])
		val := EvalExpr(env, exprStr)
		result.WriteString(val)
		path = path[end+2:]
	}
	return result.String(), nil
}

// InjectUrlFn adds url(it) and url_link(it[, label]) functions to env.
//
// url(it) resolves the noun's url_path as a {{expr}} template with "it" bound to the argument,
// prepending the auth base URL. Returns "" when url_path is empty.
//
// url_link(it[, label]) returns an OSC 8 hyperlink when stdout is a PTY, or the raw
// URL otherwise. label defaults to "link" when omitted.
func injectUrlFn(env map[string]any, ctx *cmdctx.Ctx, noun *spec.NounDef) {
	var template string
	if noun != nil {
		template = noun.UrlPath
	}
	isPty := ctx.IsPty

	// Build a level-trimmed copy of the auth map so harScopeUrl produces the
	// correct UI path segment for the scope at which the item was queried.
	urlEnv := env
	if authMap, ok := env["auth"].(map[string]any); ok {
		trimmed := maps.Clone(authMap)
		switch ctx.Level {
		case "account":
			trimmed["org"] = ""
			trimmed["project"] = ""
			trimmed["scope"] = authMap["account"]
		case "org":
			trimmed["project"] = ""
			if acct, _ := authMap["account"].(string); acct != "" {
				if org, _ := authMap["org"].(string); org != "" {
					trimmed["scope"] = acct + "/" + org
				}
			}
		}
		urlEnv = maps.Clone(env)
		urlEnv["auth"] = trimmed
	}

	urlFn := func(it any) string {
		if template == "" || ctx.Auth == nil {
			return ""
		}
		result, _ := ResolvePath(WithIt(urlEnv, it), template)
		uiBase := ctx.Auth.UIUrl
		if uiBase == "" {
			uiBase = ctx.Auth.APIUrl
		}
		return strings.TrimRight(uiBase, "/") + result
	}
	env["url"] = urlFn

	env["url_link"] = func(it any, args ...string) string {
		u := urlFn(it)
		if u == "" {
			return ""
		}
		label := "link"
		if len(args) > 0 && args[0] != "" {
			label = args[0]
		}
		if !isPty {
			return u
		}
		// OSC 8 hyperlink: \x1b]8;;<url>\x1b\\<label>\x1b]8;;\x1b\\
		return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", u, label)
	}
}
