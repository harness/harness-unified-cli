// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"testing"

	"github.com/harness/harness-cli/pkg/spec"
)

func TestSetDotPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		val  any
		want map[string]any
	}{
		{
			name: "top-level key",
			path: "query_string",
			val:  "find entity",
			want: map[string]any{"query_string": "find entity"},
		},
		{
			name: "one-level nested",
			path: "options.timeout_ms",
			val:  5000,
			want: map[string]any{"options": map[string]any{"timeout_ms": 5000}},
		},
		{
			name: "two-level nested",
			path: "a.b.c",
			val:  "deep",
			want: map[string]any{"a": map[string]any{"b": map[string]any{"c": "deep"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			setDotPath(m, tc.path, tc.val)
			if !mapsEqual(m, tc.want) {
				t.Errorf("setDotPath(%q, %v)\n got  %v\n want %v", tc.path, tc.val, m, tc.want)
			}
		})
	}
}

func TestSetDotPath_MergesExistingMap(t *testing.T) {
	m := map[string]any{
		"options": map[string]any{"max_results": 100},
	}
	setDotPath(m, "options.timeout_ms", 5000)
	opts, ok := m["options"].(map[string]any)
	if !ok {
		t.Fatal("options is not a map")
	}
	if opts["timeout_ms"] != 5000 {
		t.Errorf("timeout_ms = %v, want 5000", opts["timeout_ms"])
	}
	if opts["max_results"] != 100 {
		t.Errorf("max_results = %v, want 100 (existing key should be preserved)", opts["max_results"])
	}
}

func TestBuildBody_NilSkipped(t *testing.T) {
	// When an expression returns nil, the key must be omitted from the body.
	// This is the pattern used for optional params like options.timeout_ms.
	ep := &spec.EndpointSpec{
		BodyParams: map[string]string{
			"query_string":      `"find entity"`,
			"options.timeout_ms": `nil`,
		},
	}
	env := map[string]any{}
	body := buildBody(ep, env)
	if _, ok := body["options"]; ok {
		t.Error("options key should be absent when expression returns nil")
	}
	if body["query_string"] != "find entity" {
		t.Errorf("query_string = %v, want %q", body["query_string"], "find entity")
	}
}

func TestBuildBody_NestedParams(t *testing.T) {
	ep := &spec.EndpointSpec{
		BodyParams: map[string]string{
			"options.timeout_ms": "5000",
			"query_string":       `"select *"`,
		},
	}
	env := map[string]any{}
	body := buildBody(ep, env)
	opts, ok := body["options"].(map[string]any)
	if !ok {
		t.Fatal("options should be a map")
	}
	if opts["timeout_ms"] != 5000 {
		t.Errorf("timeout_ms = %v, want 5000", opts["timeout_ms"])
	}
}

// mapsEqual does a shallow structural comparison for test assertions.
func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch va2 := va.(type) {
		case map[string]any:
			vb2, ok := vb.(map[string]any)
			if !ok {
				return false
			}
			if !mapsEqual(va2, vb2) {
				return false
			}
		default:
			if va != vb {
				return false
			}
		}
	}
	return true
}
