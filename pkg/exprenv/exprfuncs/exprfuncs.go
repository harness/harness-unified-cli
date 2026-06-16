// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

// Package exprfuncs contains functions injected into the expr-lang environment
// for use in spec expressions (e.g. table columns, body params).
package exprfuncs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/format"
)

// HarScopeUrl returns the HAR UI URL path segment for the given auth map.
// account-only → ""
// org-only     → "/orgs/{org}"
// project      → "/orgs/{org}/projects/{project}"
func HarScopeUrl(auth map[string]any) string {
	org, _ := auth["org"].(string)
	project, _ := auth["project"].(string)
	if org == "" {
		return ""
	}
	if project == "" {
		return "/orgs/" + org
	}
	return "/orgs/" + org + "/projects/" + project
}

// FormatMetadata converts a map[string]string (from --set flags) into the
// []map[string]string shape the Harness metadata API expects: [{key, value}, ...].
func FormatMetadata(m map[string]string) []map[string]string {
	kvs := make([]map[string]string, 0, len(m))
	for k, v := range m {
		kvs = append(kvs, map[string]string{"key": k, "value": v})
	}
	return kvs
}

// FormatTags converts a comma-separated list of tag names into the map[string]string
// shape the Harness API expects: {"tagname": ""}.
func FormatTags(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	result := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if name := strings.TrimSpace(part); name != "" {
			result[name] = ""
		}
	}
	return result
}

// NewStatusIcon returns a function that maps a Harness execution status string to
// a colored icon string. When enabled is false (non-PTY or machine format), the
// returned function always returns "".
func NewStatusIcon(enabled bool) func(string) string {
	return func(status string) string {
		if !enabled {
			return ""
		}
		s := format.BucketStyles[format.ClassifyExecutionStatus(status)]
		return console.WithColor(console.Color(s.AnsiCode), s.NodeGlyph)
	}
}

// Duration formats the elapsed time between two epoch-millisecond timestamps as
// a human-readable string ("5m20s" or "42s"). Returns "" when either value is zero.
// Accepts any numeric type since JSON numbers arrive as float64 in untyped maps.
func Duration(startMs, endMs any) string {
	start := toInt64(startMs)
	end := toInt64(endMs)
	if start == 0 || end == 0 {
		return ""
	}
	secs := (end - start) / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%ds", secs/60, secs%60)
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case float32:
		return int64(n)
	}
	return 0
}

// FormatTagDisplay converts a map[string]string tags value (from the Harness API)
// into a comma-separated display string. Keys with non-empty values render as
// "key:value"; keys with empty values render as just "key".
func FormatTagDisplay(tags map[string]any) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		if sv, ok := v.(string); ok && sv != "" {
			parts = append(parts, k+":"+sv)
		} else {
			parts = append(parts, k)
		}
	}
	return strings.Join(parts, ", ")
}

// SpaceAfter returns s + " " if s is non-empty, otherwise "".
// Useful for conditionally padding an icon before adjacent text.
func SpaceAfter(s string) string {
	if s == "" {
		return ""
	}
	return s + " "
}

// LastPart returns the last "/"-delimited segment of s. If s contains no "/",
// the whole string is returned. Useful when an ID may be prefixed with an
// optional parent scope (e.g. "pipeline/executionId" or just "executionId").
func LastPart(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// FormatOrder formats an activity order/sub_order pair as "N" or "N.M".
// sub_order of 0 is omitted. Accepts any numeric type (JSON numbers arrive as float64).
func FormatOrder(order, subOrder any) string {
	o := toInt64(order)
	s := toInt64(subOrder)
	if s == 0 {
		return fmt.Sprintf("%d", o)
	}
	return fmt.Sprintf("%d.%d", o, s)
}

// Substr returns a substring of s starting at rune index start, up to length n runes.
// Negative start counts from the end. If start or end is out of bounds it is clamped.
func Substr(s string, start, n int) string {
	runes := []rune(s)
	l := len(runes)
	if start < 0 {
		start = l + start
	}
	if start < 0 {
		start = 0
	}
	if start >= l {
		return ""
	}
	end := start + n
	if end > l {
		end = l
	}
	return string(runes[start:end])
}

// Truncate collapses all whitespace (newlines, tabs, runs of spaces) in s to
// single spaces, trims leading/trailing space, then truncates to n runes,
// appending "…" if the string was shortened.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// EpochMs formats an epoch-millisecond timestamp as "2006-01-02 15:04:05" UTC.
// Accepts any numeric type. Returns "" for zero or unrecognized input.
func EpochMs(v any) string {
	var ms int64
	switch n := v.(type) {
	case float64:
		ms = int64(n)
	case int64:
		ms = n
	case int:
		ms = int64(n)
	case float32:
		ms = int64(n)
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return ""
		}
		ms = parsed
	default:
		return ""
	}
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05")
}

// JsonArray marshals a slice to a compact JSON array string. Returns "[]" for nil.
func JsonArray(v []any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// FormatRoleAssignments converts a roleAssignmentMetadata slice to a JSON array of
// "roleName (roleScopeLevel)" strings.
func FormatRoleAssignments(assignments []any) string {
	parts := make([]string, 0, len(assignments))
	for _, a := range assignments {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["roleName"].(string)
		scope, _ := m["roleScopeLevel"].(string)
		parts = append(parts, fmt.Sprintf("%s (%s)", name, scope))
	}
	b, _ := json.Marshal(parts)
	return string(b)
}

// FormatRoleIds converts a roleAssignmentMetadata slice to a JSON array of
// "roleIdentifier:roleScopeLevel" strings.
func FormatRoleIds(assignments []any) string {
	parts := make([]string, 0, len(assignments))
	for _, a := range assignments {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["roleIdentifier"].(string)
		scope, _ := m["roleScopeLevel"].(string)
		parts = append(parts, fmt.Sprintf("%s:%s", id, scope))
	}
	b, _ := json.Marshal(parts)
	return string(b)
}

// Coalesce returns the first argument that is non-zero. Zero values are:
// nil, empty string, integer/float 0, and boolean false.
func Coalesce(vals ...any) any {
	for _, v := range vals {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return v
			}
		case int:
			if t != 0 {
				return v
			}
		case int8:
			if t != 0 {
				return v
			}
		case int16:
			if t != 0 {
				return v
			}
		case int32:
			if t != 0 {
				return v
			}
		case int64:
			if t != 0 {
				return v
			}
		case uint:
			if t != 0 {
				return v
			}
		case uint8:
			if t != 0 {
				return v
			}
		case uint16:
			if t != 0 {
				return v
			}
		case uint32:
			if t != 0 {
				return v
			}
		case uint64:
			if t != 0 {
				return v
			}
		case float32:
			if t != 0 {
				return v
			}
		case float64:
			if t != 0 {
				return v
			}
		case bool:
			if t {
				return v
			}
		default:
			return v
		}
	}
	return nil
}
