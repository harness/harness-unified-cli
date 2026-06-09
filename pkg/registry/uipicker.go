// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/spec"
)

// RunUIPickerForGet intercepts a get command with --ui and runs one or more
// sequential picker TUIs to resolve the id, following the command's completion
// spec exactly as tab-completion does.
//
// Returns the fully-resolved id string (e.g. "myregistry/myartifact/v1.0").
// Returns an error if the user cancels or if no completion spec is available.
//
// ID pre-fill rules:
//   - If ctx.Id is fully specified (last "/" segment non-empty, or no seq), returns it unchanged.
//   - If ctx.Id ends with "/" (trailing slash), the parts before the slash are treated as done.
//   - Remaining steps are resolved interactively via RunUIPicker.
func RunUIPickerForGet(ctx *cmdctx.Ctx, cs *spec.CommandSpec) (string, error) {
	switch {
	case len(cs.CompletionSeq) > 0:
		return runSeqPicker(ctx, cs)
	case cs.CompletionNoun != "":
		return runSimplePicker(ctx, cs)
	default:
		return "", fmt.Errorf("--ui requires completion_noun or completion_seq on %s %s", cs.Verb, cs.Noun)
	}
}

// runSimplePicker handles the single-step case (completion_noun).
func runSimplePicker(ctx *cmdctx.Ctx, cs *spec.CommandSpec) (string, error) {
	if ctx.Id != "" {
		return ctx.Id, nil
	}
	listCs := ctx.Resolver.GetSpec(VerbList, cs.CompletionNoun)
	if listCs == nil || listCs.Endpoint == nil {
		return "", fmt.Errorf("no list command found for completion_noun %q", cs.CompletionNoun)
	}
	noun := strings.ReplaceAll(cs.CompletionNoun, "_", " ")
	title := fmt.Sprintf("pick %s", noun)
	preview := &PickerPreview{
		Verb: cs.Verb + " " + strings.ReplaceAll(cs.Noun, "_", " "),
	}
	pickerCtx := buildPickerCtx(ctx, listCs)
	return RunUIPicker(pickerCtx, listCs.Endpoint, title, preview)
}

// runSeqPicker handles multi-step completion_seq.
func runSeqPicker(ctx *cmdctx.Ctx, cs *spec.CommandSpec) (string, error) {
	steps := cs.CompletionSeq
	totalSteps := len(steps)

	// Parse already-provided parts from ctx.Id.
	// "mikereg/art/" → ["mikereg", "art"] (done), one step remaining.
	// "mikereg/art/v1" → ["mikereg", "art", "v1"] (all done, last non-empty).
	// "" → [] (all steps needed).
	doneParts, allDone := parseProvidedId(ctx.Id, totalSteps)
	if allDone {
		return ctx.Id, nil
	}

	picked := make([]string, totalSteps)
	copy(picked, doneParts)

	for i, step := range steps {
		if i < len(doneParts) {
			continue // already have this part
		}
		listCs := ctx.Resolver.GetSpec(VerbList, step.CompletionNoun)
		if listCs == nil || listCs.Endpoint == nil {
			return "", fmt.Errorf("no list command found for completion_seq step %d noun %q", i, step.CompletionNoun)
		}
		noun := strings.ReplaceAll(step.CompletionNoun, "_", " ")
		title := fmt.Sprintf("pick %s  (%d of %d)", noun, i+1, totalSteps)
		verbNoun := cs.Verb + " " + strings.ReplaceAll(cs.Noun, "_", " ")
		donePrefix := strings.Join(picked[:i], "/")
		if donePrefix != "" {
			donePrefix += "/"
		}
		var suffix string
		if i < totalSteps-1 {
			suffix = strings.Repeat("/…", totalSteps-1-i)
		}
		preview := &PickerPreview{Verb: verbNoun, Done: donePrefix, Suffix: suffix}
		pickerCtx := buildPickerCtx(ctx, listCs)
		// Pass already-picked parts as parentId so list endpoints that use
		// ctx.parentId / ctx.parentIdParts resolve correctly.
		pickerCtx.ParentId = strings.Join(picked[:i], "/")
		id, err := RunUIPicker(pickerCtx, listCs.Endpoint, title, preview)
		if err != nil {
			return "", err
		}
		picked[i] = id
	}

	return strings.Join(picked, "/"), nil
}

// parseProvidedId splits ctx.Id into done parts and reports whether the id is
// fully specified (no more picker steps needed).
//
//   - ""              → ([], false)   — nothing provided
//   - "a/"            → (["a"], false) — trailing slash, one step remains
//   - "a/b"           → (["a","b"], true) — last segment non-empty, all done
//   - "a/b/"          → (["a","b"], false) — two done, one step remains
func parseProvidedId(id string, totalSteps int) (doneParts []string, allDone bool) {
	if id == "" {
		return nil, false
	}
	// If last char is not "/" the id is complete as-is.
	if !strings.HasSuffix(id, "/") {
		return nil, true
	}
	// Trailing slash: split and treat non-empty parts as done.
	parts := strings.Split(strings.TrimSuffix(id, "/"), "/")
	var done []string
	for _, p := range parts {
		if p != "" {
			done = append(done, p)
		}
	}
	if len(done) >= totalSteps {
		return nil, true
	}
	return done, false
}

// buildPickerCtx builds a list-scoped ctx from the get ctx, inheriting auth and level.
// If the list spec declares a --search flag, it is seeded into FlagValues so the picker
// TUI enables "/" search.
func buildPickerCtx(getCtx *cmdctx.Ctx, listCs *spec.CommandSpec) *cmdctx.Ctx {
	goCtx, cancel := getCtx.Context, getCtx.CancelFn
	fv := map[string]any{}
	for _, f := range listCs.Flags {
		if f.Name == "search" {
			fv["search"] = ""
			break
		}
	}
	return &cmdctx.Ctx{
		Context:     goCtx,
		CancelFn:    cancel,
		Auth:        getCtx.Auth,
		Verb:        listCs.Verb,
		VerbHandler: listCs.VerbHandler,
		Noun:        listCs.Noun,
		FieldsNoun:  listCs.FieldsNoun,
		Level:       getCtx.Level,
		IsPty:       getCtx.IsPty,
		Resolver:    getCtx.Resolver,
		FormatFlags: cmdctx.FormatFlags{},
		FlagValues:  fv,
	}
}
