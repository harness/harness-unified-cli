// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package mgmt

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/spec"
)

type nounEntry struct {
	module string
	verbs  map[string]bool // bare verb (e.g. "get") → true, for ordering
	tokens map[string]bool // display token: "get" for plain commands, "get:summary" for variants
}

func ListNounsFetchFn(ctx *cmdctx.Ctx, _ *spec.EndpointSpec, _, _ int, _ any) (*cmdctx.PageResult, error) {
	modFilter := cmdctx.GetString(ctx.FlagValues, "module")
	search := cmdctx.GetString(ctx.FlagValues, "search")

	verbInfos := ctx.Resolver.GetVerbInfos()
	entries := map[string]*nounEntry{}
	for _, cs := range ctx.Resolver.GetAllSpecs() {
		if cs.Noun == "" {
			continue
		}
		if modFilter != "" && !strings.EqualFold(cs.Module, modFilter) {
			continue
		}
		e := entries[cs.Noun]
		if e == nil {
			e = &nounEntry{module: cs.Module, verbs: map[string]bool{}, tokens: map[string]bool{}}
			entries[cs.Noun] = e
		}
		e.verbs[cs.Verb] = true
		if cs.NounVariant != "" {
			e.tokens[cs.Verb+" "+cs.Noun+":"+cs.NounVariant] = true
		} else {
			e.tokens[cs.Verb] = true
		}
	}
	nouns := make([]string, 0, len(entries))
	for n := range entries {
		if search != "" && !strings.Contains(n, search) && !strings.Contains(entries[n].module, search) {
			continue
		}
		nouns = append(nouns, n)
	}
	sort.Strings(nouns)

	items := make([]any, 0, len(nouns))
	for _, n := range nouns {
		e := entries[n]
		// build verb token list in canonical verb order, variants immediately after their base verb
		var verbList []string
		for _, vi := range verbInfos {
			if e.tokens[vi.Verb] {
				verbList = append(verbList, vi.Verb)
			}
			for tok := range e.tokens {
				if strings.HasPrefix(tok, vi.Verb+" ") {
					verbList = append(verbList, tok)
				}
			}
		}
		if len(verbList) == 0 {
			continue
		}
		multiLevel := ""
		if nd := ctx.Resolver.GetNoun(n); nd != nil && nd.MultiLevel {
			multiLevel = "yes"
		}
		items = append(items, map[string]any{
			"noun":        n,
			"module":      e.module,
			"verbs":       strings.Join(verbList, ", "),
			"multi_level": multiLevel,
		})
	}
	return &cmdctx.PageResult{
		Items:       items,
		StartOffset: 0,
		Last:        true,
		HasTotal:    true,
		Total:       int64(len(items)),
	}, nil
}

type nounCmd struct {
	short string
	usage string
	cs    *spec.CommandSpec
}

func GetNounHandler(ctx *cmdctx.Ctx) error {
	target := strings.ToLower(strings.TrimSpace(ctx.Id))
	if target == "" {
		return fmt.Errorf("usage: harness get noun <noun>")
	}

	nd := ctx.Resolver.GetNoun(target)

	verbInfos := ctx.Resolver.GetVerbInfos()
	verbOrder := make([]string, 0, len(verbInfos))
	for _, vi := range verbInfos {
		verbOrder = append(verbOrder, vi.Verb)
	}

	cmdByVerb := map[string]*nounCmd{}
	var module string
	for _, cs := range ctx.Resolver.GetAllSpecs() {
		if !strings.EqualFold(cs.Noun, target) {
			continue
		}
		module = cs.Module
		// key is "verb" for base commands, "verb:variant" for variants
		verbKey := cs.Verb
		if cs.NounVariant != "" {
			verbKey = cs.Verb + ":" + cs.NounVariant
		}
		usage := "harness " + cs.Verb + " " + cs.FullNoun()
		if cs.RequiresParentId && cs.ParentIdLabel != "" {
			usage += " " + cs.ParentIdLabel
		} else if cs.IdLabel != "" {
			usage += " " + cs.IdLabel
		} else if cs.Verb != "list" {
			usage += " <id>"
		}
		if cs.HasArgs && cs.ArgsLabel != "" {
			usage += " " + cs.ArgsLabel
		}
		cmdByVerb[verbKey] = &nounCmd{short: cs.Short, usage: usage, cs: cs}
	}

	if len(cmdByVerb) == 0 && nd == nil {
		return fmt.Errorf("noun %q not found", target)
	}

	// header
	if nd != nil && nd.ShortDesc != "" {
		fmt.Printf("%s — %s\n", target, nd.ShortDesc)
	} else {
		fmt.Printf("%s\n", target)
	}
	if module != "" {
		fmt.Printf("module: %s\n", module)
	}
	if nd != nil && nd.MultiLevel {
		fmt.Printf("scope:  account | org | project  (use --level to select)\n")
	}

	// commands section
	if len(cmdByVerb) > 0 {
		fmt.Println()
		fmt.Println("Commands:")
		first := true
		printCmd := func(nc *nounCmd) {
			if !first {
				fmt.Println()
			}
			first = false
			fmt.Printf("  # %s\n", nc.short)
			fmt.Printf("  %s\n", nc.usage)
		}
		for _, v := range verbOrder {
			if nc := cmdByVerb[v]; nc != nil {
				printCmd(nc)
			}
			// variants follow their base verb in sorted order
			var variantKeys []string
			for k := range cmdByVerb {
				if strings.HasPrefix(k, v+":") {
					variantKeys = append(variantKeys, k)
				}
			}
			sort.Strings(variantKeys)
			for _, k := range variantKeys {
				printCmd(cmdByVerb[k])
			}
		}
	}

	// fields section: union of get+list, split into shared / get-only / list-only
	getFields := ctx.Resolver.ResolveCommandFields(nounCmdSpec(cmdByVerb["get"]))
	listFields := ctx.Resolver.ResolveCommandFields(nounCmdSpec(cmdByVerb["list"]))

	type fieldEntry struct {
		f      spec.FieldDef
		inGet  bool
		inList bool
	}
	seen := map[string]*fieldEntry{}
	var fieldOrder []string
	addFields := func(fields []spec.FieldDef, isGet bool) {
		for _, f := range fields {
			fe := seen[f.ID]
			if fe == nil {
				fe = &fieldEntry{f: f}
				seen[f.ID] = fe
				fieldOrder = append(fieldOrder, f.ID)
			}
			if isGet {
				fe.inGet = true
			} else {
				fe.inList = true
			}
		}
	}
	addFields(getFields, true)
	addFields(listFields, false)

	if len(fieldOrder) > 0 {
		bothPresent := len(getFields) > 0 && len(listFields) > 0

		var shared, getOnly, listOnly []spec.FieldDef
		for _, id := range fieldOrder {
			fe := seen[id]
			switch {
			case bothPresent && fe.inGet && !fe.inList:
				getOnly = append(getOnly, fe.f)
			case bothPresent && fe.inList && !fe.inGet:
				listOnly = append(listOnly, fe.f)
			default:
				shared = append(shared, fe.f)
			}
		}

		printFieldSection := func(header string, fields []spec.FieldDef) {
			if len(fields) == 0 {
				return
			}
			fmt.Println()
			fmt.Println(header)
			maxID := 0
			for _, f := range fields {
				if len(f.ID) > maxID {
					maxID = len(f.ID)
				}
			}
			for _, f := range fields {
				padding := strings.Repeat(" ", maxID-len(f.ID))
				label := f.Label
				hasLabel := label != "" && !strings.EqualFold(label, strings.ReplaceAll(f.ID, "_", " "))
				editable := f.MutablePath != ""
				switch {
				case hasLabel && editable:
					fmt.Printf("  %s%s  -- %s (editable)\n", f.ID, padding, label)
				case hasLabel:
					fmt.Printf("  %s%s  -- %s\n", f.ID, padding, label)
				case editable:
					fmt.Printf("  %s%s  -- (editable)\n", f.ID, padding)
				default:
					fmt.Printf("  %s\n", f.ID)
				}
			}
		}

		printFieldSection("Fields:", shared)
		printFieldSection("Get only:", getOnly)
		printFieldSection("List only:", listOnly)
	}

	fmt.Println()
	return nil
}

func nounCmdSpec(nc *nounCmd) *spec.CommandSpec {
	if nc == nil {
		return nil
	}
	return nc.cs
}

func renderNounMatrix(ctx *cmdctx.Ctx, entries map[string]*nounEntry, nouns []string, verbInfos []spec.VerbInfo) error {
	// only include verbs that appear at least once across all listed nouns
	activeVerbSet := map[string]bool{}
	for _, e := range entries {
		for v := range e.verbs {
			activeVerbSet[v] = true
		}
	}
	var activeVerbs []string
	for _, vi := range verbInfos {
		if activeVerbSet[vi.Verb] {
			activeVerbs = append(activeVerbs, vi.Verb)
		}
	}

	check := console.GreenCheck()

	t := format.NewTable()
	t.SetOutputMirror(os.Stdout)

	// column 1 = noun, column 2 = module, columns 3+ = verbs (center-aligned)
	colConfigs := make([]table.ColumnConfig, len(activeVerbs))
	for i := range activeVerbs {
		colConfigs[i] = table.ColumnConfig{
			Number:      i + 3,
			Align:       text.AlignCenter,
			AlignHeader: text.AlignCenter,
		}
	}
	t.SetColumnConfigs(colConfigs)

	header := make(table.Row, 2+len(activeVerbs))
	header[0] = "noun"
	header[1] = "module"
	for i, v := range activeVerbs {
		header[i+2] = v
	}
	t.AppendHeader(header)

	for _, n := range nouns {
		e := entries[n]
		row := make(table.Row, 2+len(activeVerbs))
		row[0] = n
		row[1] = e.module
		for i, v := range activeVerbs {
			if e.verbs[v] {
				row[i+2] = check
			} else {
				row[i+2] = ""
			}
		}
		t.AppendRow(row)
	}

	t.Render()
	return nil
}
