// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/harness/harness-cli/pkg/spec"
)

// ApplyColumns resolves a --columns string against a set of known FieldDefs into []TableColumn.
//
// Token forms (comma-separated, or JSON array):
//
//	"id"         → look up field by ID; error if not found
//	"id:expr"    → ad-hoc column: label auto-derived from id (underscores→spaces, title-case), expr literal
//
// Sigil prefix:
//
//	"+id"        → add to default set (modify mode)
//	"-id"        → remove from default set by ID (modify mode)
//
// If ALL tokens carry a sigil, modify mode applies against the default columns.
// If ANY token is plain, replace mode: the result is exactly those columns.
// Mixing sigiled and plain tokens is an error.
func ApplyColumns(fields []spec.FieldDef, defaultCols []spec.TableColumn, s string) ([]spec.TableColumn, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultCols, nil
	}

	byID := make(map[string]spec.FieldDef, len(fields))
	for _, f := range fields {
		byID[fieldID(f)] = f
	}

	tokens := splitTokens(s)
	if len(tokens) == 0 {
		return defaultCols, nil
	}

	allSigiled := true
	for _, t := range tokens {
		if !strings.HasPrefix(t, "+") && !strings.HasPrefix(t, "-") {
			allSigiled = false
			break
		}
	}

	if !allSigiled {
		// Replace mode.
		var result []spec.TableColumn
		for _, token := range tokens {
			if strings.HasPrefix(token, "-") {
				continue // drop sigiled removals in replace mode
			}
			token = strings.TrimPrefix(token, "+")
			col, err := resolveToken(token, byID)
			if err != nil {
				return nil, err
			}
			result = append(result, col)
		}
		return result, nil
	}

	// Modify mode against default.
	result := make([]spec.TableColumn, len(defaultCols))
	copy(result, defaultCols)
	for _, token := range tokens {
		sigil := token[0]
		rest := strings.TrimSpace(token[1:])
		if sigil == '-' {
			id := strings.SplitN(rest, ":", 2)[0]
			for i, col := range result {
				if col.Header == labelFromID(id) || strings.EqualFold(col.Header, id) {
					result = append(result[:i], result[i+1:]...)
					break
				}
			}
		} else {
			col, err := resolveToken(rest, byID)
			if err != nil {
				return nil, err
			}
			result = append(result, col)
		}
	}
	return result, nil
}

// resolveToken parses one plain token (no sigil) into a TableColumn.
// "id" → field lookup; "id:expr" → ad-hoc column.
func resolveToken(token string, byID map[string]spec.FieldDef) (spec.TableColumn, error) {
	idx := strings.Index(token, ":")
	if idx < 0 {
		// Field ID lookup.
		f, ok := byID[token]
		if !ok {
			return spec.TableColumn{}, fmt.Errorf("unknown field ID %q; run with --list-columns to see available fields", token)
		}
		return spec.TableColumn{Header: fieldLabel(f), Expr: f.Expr, Align: f.Align, FieldType: f.FieldType, WidthMax: f.WidthMax}, nil
	}
	// Ad-hoc: id:expr
	id := token[:idx]
	expr := token[idx+1:]
	if expr == "" {
		return spec.TableColumn{}, fmt.Errorf("column %q has empty expression after ':'", id)
	}
	return spec.TableColumn{Header: labelFromID(id), Expr: expr}, nil
}

// splitTokens splits a comma-separated token string, trimming whitespace.
// If the string is a JSON array, the array elements are used as tokens.
func splitTokens(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr
		}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// fieldID returns the effective ID for a FieldDef.
func fieldID(f spec.FieldDef) string {
	return f.ID
}

// fieldLabel returns the display label for a FieldDef: explicit Label, or auto-derived from ID.
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
		runes := []rune(w)
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}

// ColumnParseError is returned when a column spec string cannot be parsed.
type ColumnParseError struct {
	Input string
	Err   error
}

func (e *ColumnParseError) Error() string {
	return "invalid --columns value: " + e.Err.Error()
}

func (e *ColumnParseError) Unwrap() error { return e.Err }
