// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"strings"
	"unicode"

	"github.com/expr-lang/expr"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/extractutil"
	"github.com/harness/harness-cli/pkg/spec"
)

// TextFormatterFn is an alias for the canonical type in cmdctx.
type TextFormatterFn = cmdctx.TextFormatterFn

var validArrayFormats = map[string]bool{
	"json": true, "jsonl": true, "table": true, "csv": true, "tsv": true, "markdown": true,
}

// PageMeta carries optional paging summary information for display after a table.
// Offset is the item-level offset of the first item returned. Count is the number
// of items actually returned. HasTotal indicates whether Total is valid.
type PageMeta struct {
	Offset   int
	Count    int
	HasTotal bool
	Total    int64
}

// FormatArrayOutput renders a list response (table, json, jsonl, csv, tsv).
// itemsExpr is an expr-lang expression that resolves the row slice; "it" is bound to the full response.
// defaultTspec is the command's declared table layout; may be nil.
// exprEnv is the base expr-lang environment (ctx, flags, auth, helpers); "it" is injected per row for columns.
// meta, when non-nil, causes a "showing X-Y of Z" footer to be printed after the table.
func FormatArrayOutput(flags cmdctx.FormatFlags, isPty bool, data any, itemsExpr string, defaultTspec *spec.TableSpec, fields []spec.FieldDef, exprEnv map[string]any, meta *PageMeta) error {
	// 1. Resolve --columns into a tspec (overrides default).
	tspec := defaultTspec
	if flags.Columns != "" {
		var base []spec.TableColumn
		if defaultTspec != nil {
			base = defaultTspec.Columns
		}
		cols, err := ApplyColumns(fields, base, flags.Columns)
		if err != nil {
			return err
		}
		tspec = &spec.TableSpec{Columns: cols}
	}

	// 2. Default format: table only when attached to a terminal and we have a spec.
	if flags.Format == "" {
		if tspec != nil {
			flags.Format = "table"
		} else {
			flags.Format = "json"
		}
	}

	if !validArrayFormats[flags.Format] {
		return fmt.Errorf("unknown format %q: must be one of json, jsonl, table, csv, tsv, markdown", flags.Format)
	}

	// 3. Table format requires a resolved spec.
	if flags.Format == "table" && tspec == nil {
		return fmt.Errorf("--format table requires a table spec or --columns")
	}

	if flags.Raw && flags.Format != "json" {
		return fmt.Errorf("--raw is only supported with --format json")
	}

	w, close, err := OpenWriter(flags.OutFile)
	if err != nil {
		return err
	}
	defer close()

	itemsEnv := withIt(exprEnv, data)

	if flags.Format == "jsonl" {
		items, err := evalItemsExpr(itemsEnv, itemsExpr)
		if err != nil {
			return fmt.Errorf("jsonl items_expr %q: %w", itemsExpr, err)
		}
		return formatJsonl(w, itemsExpr, items)
	}

	if flags.Format != "json" {
		t, err := BuildTable(tspec, itemsExpr, data, flags.NoHeaders, exprEnv)
		if err != nil {
			return err
		}
		t.SetOutputMirror(w)
		switch flags.Format {
		case "csv":
			t.RenderCSV()
		case "tsv":
			t.RenderTSV()
		case "markdown":
			t.RenderMarkdown()
		default:
			t.Render()
			if meta != nil && !flags.NoHeaders {
				if meta.Count > 0 {
					first := meta.Offset + 1
					last := meta.Offset + meta.Count
					fmt.Fprintf(w, "─────\n")
					if meta.HasTotal {
						fmt.Fprintf(w, "Showing %d-%d of %d\n", first, last, meta.Total)
					} else {
						fmt.Fprintf(w, "Showing %d-%d\n", first, last)
					}
				} else if meta.HasTotal {
					fmt.Fprintf(w, "No results (%d items total)\n", meta.Total)
				}
			}
		}
		return nil
	}

	// json
	payload := data
	if !flags.Raw {
		extracted, err := evalItemsExpr(itemsEnv, itemsExpr)
		if err != nil {
			return fmt.Errorf("items_expr %q: %w", itemsExpr, err)
		}
		payload = extracted
	}
	return writeJSON(w, payload)
}

// FormatFieldsOutput extracts a comma-separated list of field IDs from a single-item response
// and prints their values tab-separated on one line. Designed for shell $() capture.
// Unknown field IDs are silently skipped (empty string).
func FormatFieldsOutput(flags cmdctx.FormatFlags, data any, itemExpr string, fields []spec.FieldDef, fieldIDs []string, exprEnv map[string]any) error {
	w, closeW, err := OpenWriter(flags.OutFile)
	if err != nil {
		return err
	}
	defer closeW()

	payload := data
	if !flags.Raw && itemExpr != "" {
		extracted := evalColumnExpr(withIt(exprEnv, data), itemExpr)
		if extracted == nil {
			return nil
		}
		payload = extracted
	}

	byID := make(map[string]spec.FieldDef, len(fields))
	for _, f := range fields {
		byID[f.ID] = f
	}

	env := withIt(exprEnv, payload)
	vals := make([]string, 0, len(fieldIDs))
	for _, id := range fieldIDs {
		f, ok := byID[id]
		if !ok {
			vals = append(vals, "")
			continue
		}
		v := evalColumnExpr(env, f.DisplayExpr())
		if v == nil {
			vals = append(vals, "")
		} else {
			vals = append(vals, fmt.Sprintf("%v", v))
		}
	}
	fmt.Fprintln(w, strings.Join(vals, "\t"))
	return nil
}

// FormatSingleOutput renders a single-item response (get, execute, …).
// itemExpr is an expr-lang expression unwrapped unless --raw is set. Use "it" for bare responses.
// "it" is bound to the full response; ctx, auth, flags, and helpers are also available via exprEnv.
// textFmt, when non-nil, is used when format is "text" (or unset on a PTY with a textFmt registered).
func FormatSingleOutput(flags cmdctx.FormatFlags, isPty bool, data any, itemExpr string, textFmt TextFormatterFn, exprEnv map[string]any) error {
	if flags.Format == "" {
		if textFmt != nil {
			flags.Format = "text"
		} else {
			flags.Format = "json"
		}
	}
	if flags.Format != "json" && flags.Format != "text" {
		return fmt.Errorf("format %q is not supported here; use json or text", flags.Format)
	}

	w, closeW, err := OpenWriter(flags.OutFile)
	if err != nil {
		return err
	}
	defer closeW()

	payload := data
	if !flags.Raw && itemExpr != "" {
		extracted := evalColumnExpr(withIt(exprEnv, data), itemExpr)
		if extracted == nil {
			return nil
		}
		payload = extracted
	}

	if flags.Format == "text" {
		if textFmt == nil {
			return fmt.Errorf("--format text is not supported for this command")
		}
		return textFmt(w, extractutil.MakeDataAccessor(exprEnv, payload))
	}
	return writeJSON(w, payload)
}

func OpenWriter(outFile string) (io.Writer, func(), error) {
	if outFile == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(outFile)
	if err != nil {
		return nil, nil, fmt.Errorf("opening output file: %w", err)
	}
	return f, func() { f.Close() }, nil
}

func writeJSON(w io.Writer, data any) error {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("formatting output: %w", err)
	}
	fmt.Fprintln(w, string(out))
	return nil
}

// formatJsonl writes items as newline-delimited JSON.
func formatJsonl(w io.Writer, itemsPath string, items any) error {
	v := reflect.ValueOf(items)
	if v.Kind() != reflect.Slice {
		return fmt.Errorf("jsonl items path %q did not resolve to a slice (got %T)", itemsPath, items)
	}
	enc := json.NewEncoder(w)
	for i := range v.Len() {
		if err := enc.Encode(v.Index(i).Interface()); err != nil {
			return fmt.Errorf("jsonl encode: %w", err)
		}
	}
	return nil
}

// BuildTextFieldFormatter returns a TextFormatterFn driven by a declarative field list
// with optional header and footer templates. interpolate resolves {{expr}} in header/footer
// against the response item — callers supply this to avoid an import cycle with the
// registry's resolvePath.
// Supported Format values: "epoch_ms", "bool", "json_array", "role_assignments".
func BuildTextFieldFormatter(fields []spec.FieldDef, header, footer string, interpolate func(tmpl string, item any) string) TextFormatterFn {
	return func(w io.Writer, d cmdctx.DataAccessor) error {
		item := d.GetData()
		if header != "" {
			if s := interpolate(header, item); s != "" {
				fmt.Fprint(w, s)
			}
		}
		if len(fields) > 0 {
			items := make([]LabeledValue, 0, len(fields))
			var yamlBlocks []string
			for _, f := range fields {
				if f.FieldType == "multiline_text" || f.FieldType == "yaml" {
					yamlBlocks = append(yamlBlocks, d.GetString(f.DisplayExpr()))
					continue
				}
				label := f.Label
				if label == "" {
					label = fieldLabelFromID(f.ID)
				}
				val := d.GetString(f.DisplayExpr())
				items = append(items, LabeledValue{Label: label, Value: val})
			}
			WriteLabeledValues(w, items)
			for _, block := range yamlBlocks {
				if block != "" {
					fmt.Fprintf(w, "\n%s", block)
					if len(block) > 0 && block[len(block)-1] != '\n' {
						fmt.Fprintln(w)
					}
				}
			}
		}
		if footer != "" {
			if s := interpolate(footer, item); s != "" {
				fmt.Fprint(w, s)
			}
		} else {
			fmt.Fprintln(w)
		}
		return nil
	}
}

// fieldLabelFromID converts an underscore_id to "Title Case Label".
func fieldLabelFromID(id string) string {
	var b strings.Builder
	capitalize := true
	for i, c := range id {
		if c == '_' {
			b.WriteByte(' ')
			capitalize = true
		} else if capitalize {
			b.WriteRune(unicode.ToUpper(c))
			capitalize = false
		} else {
			b.WriteRune(c)
		}
		_ = i
	}
	return b.String()
}

// LabeledValue is a label/value pair for use with WriteLabeledValues.
type LabeledValue struct {
	Label string
	Value string
}

// WriteLabeledValues writes label/value pairs aligned by the longest label.
// Each label is suffixed with ":" and padded so all values line up.
func WriteLabeledValues(w io.Writer, items []LabeledValue) {
	maxLen := 0
	for _, item := range items {
		if len(item.Label) > maxLen {
			maxLen = len(item.Label)
		}
	}
	for _, item := range items {
		fmt.Fprintf(w, "%-*s  %s\n", maxLen+1, item.Label+":", item.Value)
	}
}

// NewTable returns a table writer with the standard style: no borders, bold headers, separator line.
func NewTable() table.Writer {
	t := table.NewWriter()
	t.SetStyle(table.StyleLight)
	t.Style().Options.DrawBorder = false
	t.Style().Options.SeparateColumns = false
	t.Style().Options.SeparateHeader = true
	t.Style().Options.SeparateRows = false
	t.Style().Format.Header = text.FormatDefault
	if console.IsStdoutTTY() {
		t.Style().Color.Header = text.Colors{text.Bold}
	}
	return t
}

// BuildTable evaluates itemsExpr against resp to locate the row slice, then renders
// the columns defined in spec. Use "it" as itemsExpr for bare array responses.
// exprEnv is the base expr-lang environment; "it" is injected per row for column expressions.
func BuildTable(tspec *spec.TableSpec, itemsExpr string, resp any, noHeaders bool, exprEnv map[string]any) (table.Writer, error) {
	rows, err := evalItemsExpr(withIt(exprEnv, resp), itemsExpr)
	if err != nil {
		return nil, fmt.Errorf("table items_expr %q: %w", itemsExpr, err)
	}

	t := NewTable()
	t.SetOutputMirror(os.Stdout)

	var colConfigs []table.ColumnConfig
	for i, col := range tspec.Columns {
		cfg := table.ColumnConfig{Number: i + 1}
		if col.Align == "right" {
			cfg.Align = text.AlignRight
		}
		if col.WidthMax > 0 {
			cfg.WidthMax = col.WidthMax
		}
		if cfg.Align != text.AlignDefault || cfg.WidthMax > 0 {
			colConfigs = append(colConfigs, cfg)
		}
	}
	if len(colConfigs) > 0 {
		t.SetColumnConfigs(colConfigs)
	}

	if !noHeaders {
		header := make(table.Row, len(tspec.Columns))
		for i, col := range tspec.Columns {
			header[i] = col.Header
		}
		t.AppendHeader(header)
	}

	for _, item := range rows {
		env := withIt(exprEnv, item)

		row := make(table.Row, len(tspec.Columns))
		for i, col := range tspec.Columns {
			val := evalColumnExpr(env, col.Expr)
			if val == nil {
				row[i] = ""
			} else {
				row[i] = fmt.Sprint(val)
			}
		}
		t.AppendRow(row)
	}

	return t, nil
}

// withIt returns a shallow copy of env with "it" set to val.
func withIt(env map[string]any, val any) map[string]any {
	out := make(map[string]any, len(env)+1)
	maps.Copy(out, env)
	out["it"] = val
	return out
}

// evalItemsExpr evaluates an expr-lang expression expected to return a []any.
func evalItemsExpr(env map[string]any, exprStr string) ([]any, error) {
	program, err := expr.Compile(exprStr, expr.Env(env), expr.AsAny())
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

// evalColumnExpr evaluates an expr-lang expression for a table column.
// Returns nil on error or nil result.
func evalColumnExpr(env map[string]any, exprStr string) any {
	program, err := expr.Compile(exprStr, expr.Env(env), expr.AsAny())
	if err != nil {
		return nil
	}
	out, err := expr.Run(program, env)
	if err != nil {
		return nil
	}
	return out
}
