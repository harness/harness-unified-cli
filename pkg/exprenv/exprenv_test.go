// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package exprenv

import (
	"testing"
)

func baseEnv() map[string]any {
	return map[string]any{
		"ctx": map[string]any{
			"id":       "my-id",
			"parentId": "parent-id",
		},
		"flags": map[string]any{
			"search":  "hello",
			"timeout": "",
		},
		"it": map[string]any{
			"name":        "test-item",
			"description": "a description",
			"nested": map[string]any{
				"value": "deep",
			},
		},
	}
}

func TestEvalExpr(t *testing.T) {
	env := baseEnv()
	tests := []struct {
		expr string
		want string
	}{
		{"it.name", "test-item"},
		{"it.nested.value", "deep"},
		{"ctx.id", "my-id"},
		{"flags.search", "hello"},
		// nil/missing → empty string
		{"it.missing", ""},
		// oneof null-coalescing
		{"it.absent ?? it.name", "test-item"},
		// string literal
		{`"literal"`, "literal"},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got := EvalExpr(env, tc.expr)
			if got != tc.want {
				t.Errorf("EvalExpr(%q) = %q, want %q", tc.expr, got, tc.want)
			}
		})
	}
}

func TestEvalExprAny_Nil(t *testing.T) {
	env := baseEnv()
	// nil return from conditional — used for optional body params
	_, ok := EvalExprAny(env, `flags.timeout != "" ? flags.timeout : nil`)
	if ok {
		t.Error("expected ok=false for nil expression result")
	}
}

func TestEvalExprAny_Value(t *testing.T) {
	env := baseEnv()
	env["flags"] = map[string]any{"timeout": "5000"}
	result, ok := EvalExprAny(env, `flags.timeout != "" ? flags.timeout : nil`)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if result != "5000" {
		t.Errorf("got %v, want %q", result, "5000")
	}
}

func TestEvalItemsExpr(t *testing.T) {
	env := map[string]any{
		"it": map[string]any{
			"items": []any{"a", "b", "c"},
		},
	}
	items, err := EvalItemsExpr(env, "it.items")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
}

func TestEvalItemsExpr_Concat(t *testing.T) {
	env := map[string]any{
		"it": map[string]any{
			"entity_types":   []any{"e1", "e2"},
			"event_types":    []any{"ev1"},
			"config_types":   nil,
		},
		"concat": func(args ...[]any) []any {
			var out []any
			for _, a := range args {
				out = append(out, a...)
			}
			return out
		},
	}
	// mirrors the kg:type items_expr pattern
	items, err := EvalItemsExpr(env, `concat(it.entity_types ?? [], it.event_types ?? [], it.config_types ?? [])`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
}

func TestResolvePath(t *testing.T) {
	env := map[string]any{
		"ctx": map[string]any{"parentId": "my-repo", "id": "main"},
	}
	tests := []struct {
		path string
		want string
		err  bool
	}{
		{"/code/api/v1/repos/{{ctx.parentId}}/branches", "/code/api/v1/repos/my-repo/branches", false},
		{"/code/api/v1/repos/{{ctx.parentId}}/branches/{{ctx.id}}", "/code/api/v1/repos/my-repo/branches/main", false},
		{"/static/path", "/static/path", false},
		{"{{ctx.id}}", "main", false},
		{"{{unclosed", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got, err := ResolvePath(env, tc.path)
			if tc.err {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolvePath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestWithIt(t *testing.T) {
	base := map[string]any{"ctx": "original", "it": "old"}
	updated := WithIt(base, "new-it")
	if updated["it"] != "new-it" {
		t.Errorf("expected it=new-it, got %v", updated["it"])
	}
	// base must be unchanged
	if base["it"] != "old" {
		t.Error("WithIt mutated the original env")
	}
}
