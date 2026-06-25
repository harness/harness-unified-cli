// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/jedib0t/go-pretty/v6/table"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/spec"
)

// splitFieldIDs splits a comma-separated --fields string into trimmed, non-empty IDs.
func splitFieldIDs(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if id := strings.TrimSpace(p); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// resolveNounDef returns the NounDef for the command, respecting fields_noun override.
// Returns nil if no resolver is set or the noun is not registered.
func resolveNounDef(ctx *cmdctx.Ctx) *spec.NounDef {
	if ctx.Resolver == nil {
		return nil
	}
	noun := ctx.Noun
	if ctx.FieldsNoun != "" {
		noun = ctx.FieldsNoun
	}
	return ctx.Resolver.GetNoun(noun)
}

// resolveFieldsForCommand returns the effective []FieldDef for a command endpoint.
// It looks up the noun's fields from the registry (using fields_noun override when set),
// applies fields_subset filtering, then appends fields_extra.
func resolveFieldsForCommand(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) []spec.FieldDef {
	if ep == nil || ep.NoFields {
		return nil
	}
	if ctx.Resolver == nil {
		return nil
	}
	noun := ctx.Noun
	if ctx.FieldsNoun != "" {
		noun = ctx.FieldsNoun
	}

	nd := ctx.Resolver.GetNoun(noun)
	if nd == nil {
		return nil
	}
	base := nd.Fields

	if len(ep.FieldsSubset) > 0 && len(base) > 0 {
		keep := make(map[string]bool, len(ep.FieldsSubset))
		for _, id := range ep.FieldsSubset {
			keep[id] = true
		}
		filtered := make([]spec.FieldDef, 0, len(ep.FieldsSubset))
		for _, f := range base {
			if keep[f.ID] {
				filtered = append(filtered, f)
			}
		}
		base = filtered
	}

	if len(ep.FieldsExtra) == 0 {
		return base
	}
	return append(base, ep.FieldsExtra...)
}

// fieldLabel returns the display label: explicit Label or auto-derived from ID.
func fieldLabel(f spec.FieldDef) string {
	if f.Label != "" {
		return f.Label
	}
	return labelFromID(f.ID)
}

// labelFromID converts an underscore_id to "Title Case Label".
func labelFromID(id string) string {
	words := strings.Split(id, "_")
	for i, w := range words {
		if len(w) == 0 {
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// PrintFieldTable writes a human-readable table of available field IDs to w.
func PrintFieldTable(w io.Writer, fields []spec.FieldDef) error {
	if len(fields) == 0 {
		fmt.Fprintln(w, "No fields defined for this command.")
		return nil
	}
	t := format.NewTable()
	t.AppendHeader(table.Row{"ID", "Label", "Expr"})
	for _, f := range fields {
		t.AppendRow(table.Row{f.ID, fieldLabel(f), f.Expr})
	}
	t.SetOutputMirror(w)
	t.Render()
	return nil
}

// ResolveCommandFields returns the effective []FieldDef for a CommandSpec: noun fields
// (respecting fields_noun), filtered by fields_subset, with fields_extra appended.
func (r *Registry) ResolveCommandFields(cs *spec.CommandSpec) []spec.FieldDef {
	if cs == nil || cs.Endpoint == nil || cs.Endpoint.NoFields {
		return nil
	}
	noun := cs.Noun
	if cs.FieldsNoun != "" {
		noun = cs.FieldsNoun
	}
	nd := r.GetNoun(noun)
	if nd == nil {
		return nil
	}
	base := nd.Fields
	ep := cs.Endpoint
	if len(ep.FieldsSubset) > 0 && len(base) > 0 {
		keep := make(map[string]bool, len(ep.FieldsSubset))
		for _, id := range ep.FieldsSubset {
			keep[id] = true
		}
		filtered := make([]spec.FieldDef, 0, len(ep.FieldsSubset))
		for _, f := range base {
			if keep[f.ID] {
				filtered = append(filtered, f)
			}
		}
		base = filtered
	}
	if len(ep.FieldsExtra) == 0 {
		return base
	}
	return append(base, ep.FieldsExtra...)
}

// MutableFields returns only the writable fields for a noun (those with mutable_path set).
func MutableFields(noun *spec.NounDef) []spec.FieldDef {
	if noun == nil {
		return nil
	}
	var out []spec.FieldDef
	for _, f := range noun.Fields {
		if f.MutablePath != "" {
			out = append(out, f)
		}
	}
	return out
}

// PrintMutableFieldTable writes a table of settable (path-based) fields to w,
// including their field_type so users know the --set/--del semantics.
func PrintMutableFieldTable(w io.Writer, fields []spec.FieldDef) error {
	mutable := fields
	if len(mutable) == 0 {
		fmt.Fprintln(w, "No mutable fields defined for this command.")
		return nil
	}
	t := format.NewTable()
	t.AppendHeader(table.Row{"ID", "Label", "Type"})
	for _, f := range mutable {
		ft := f.FieldType
		if ft == "" {
			ft = "scalar"
		}
		t.AppendRow(table.Row{f.ID, fieldLabel(f), ft})
	}
	t.SetOutputMirror(w)
	t.Render()
	return nil
}

// buildTspec builds a *spec.TableSpec from a columns ID list and field definitions.
// If columnIDs is non-empty, only those fields are included (in order).
// If columnIDs is empty, all fields are included.
// Returns nil if fields is empty.
func buildTspec(columnIDs []string, fields []spec.FieldDef) *spec.TableSpec {
	if len(fields) == 0 {
		return nil
	}
	byID := make(map[string]spec.FieldDef, len(fields))
	for _, f := range fields {
		byID[f.ID] = f
	}
	var cols []spec.TableColumn
	if len(columnIDs) > 0 {
		for _, id := range columnIDs {
			if f, ok := byID[id]; ok {
				cols = append(cols, spec.TableColumn{Header: fieldLabel(f), Expr: f.Expr, Align: f.Align, FieldType: f.FieldType, WidthMax: f.WidthMax})
			}
		}
	} else {
		cols = FieldsToTableColumns(fields)
	}
	if len(cols) == 0 {
		return nil
	}
	return &spec.TableSpec{Columns: cols}
}

// FieldsToTableColumns converts a []FieldDef to []spec.TableColumn.
func FieldsToTableColumns(fields []spec.FieldDef) []spec.TableColumn {
	cols := make([]spec.TableColumn, len(fields))
	for i, f := range fields {
		cols[i] = spec.TableColumn{Header: fieldLabel(f), Expr: f.Expr, Align: f.Align, FieldType: f.FieldType, WidthMax: f.WidthMax}
	}
	return cols
}


