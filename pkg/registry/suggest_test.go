// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"strings"
	"testing"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/spec"
)

// buildTestRegistry creates a minimal registry with a few nouns and commands
// for use in SuggestRootCommand tests.
func buildTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := New()
	for _, nd := range []spec.NounDef{
		{Noun: "pr", NounAliases: []string{"prs", "pull_request"}},
		{Noun: "pipeline", NounAliases: []string{"pipelines"}},
		{Noun: "connector"},
		{Noun: "artifact"},
	} {
		if err := r.RegisterNoun(nd); err != nil {
			t.Fatalf("RegisterNoun %q: %v", nd.Noun, err)
		}
	}
	// Use workflow handler to avoid endpoint validation requirements in tests.
	wfID := "test:noop"
	r.RegisterWorkflow(wfID, func(*cmdctx.Ctx) error { return nil })
	for _, cs := range []*spec.CommandSpec{
		{Command: "create pr", Verb: VerbCreate, Noun: "pr", Module: "code", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "list pr", Verb: VerbList, Noun: "pr", Module: "code", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "list pr:mine", Verb: VerbList, Noun: "pr", NounVariant: "mine", Module: "code", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "get pr", Verb: VerbGet, Noun: "pr", Module: "code", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "execute pr:merge", Verb: VerbExecute, Noun: "pr", NounVariant: "merge", Module: "code", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "create pipeline", Verb: VerbCreate, Noun: "pipeline", Module: "pipeline", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "list pipeline", Verb: VerbList, Noun: "pipeline", Module: "pipeline", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "get pipeline:summary", Verb: VerbGet, Noun: "pipeline", NounVariant: "summary", Module: "pipeline", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "list connector", Verb: VerbList, Noun: "connector", Module: "platform", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
		{Command: "list artifact", Verb: VerbList, Noun: "artifact", Module: "har", HandlerType: spec.HandlerWorkflow, WorkflowID: wfID},
	} {
		if err := r.Register(cs); err != nil {
			t.Fatalf("Register %s %s: %v", cs.Verb, cs.Noun, err)
		}
	}
	return r
}

func TestSuggestRootCommand_Transposition(t *testing.T) {
	r := buildTestRegistry(t)

	tests := []struct {
		name        string
		args        []string
		wantContain string // substring the suggestion must contain
	}{
		{
			name:        "noun-verb swap: pr create",
			args:        []string{"pr", "create"},
			wantContain: "harness create pr",
		},
		{
			name:        "noun-verb swap: pr list",
			args:        []string{"pr", "list"},
			wantContain: "harness list pr",
		},
		{
			name:        "noun-verb swap with extra args",
			args:        []string{"pr", "create", "--set", "title=foo"},
			wantContain: "harness create pr",
		},
		{
			name:        "alias used: prs list",
			args:        []string{"prs", "list"},
			wantContain: "harness list pr",
		},
		{
			name:        "alias used: pull_request create",
			args:        []string{"pull_request", "create"},
			wantContain: "harness create pr", // suggestion uses canonical noun
		},
		{
			name:        "pipeline list transposition",
			args:        []string{"pipeline", "list"},
			wantContain: "harness list pipeline",
		},
		{
			name:        "noun:variant with verb: pipeline:summary get",
			args:        []string{"pipeline:summary", "get"},
			wantContain: "harness get pipeline:summary",
		},
		{
			name:        "verb:variant transposition: pr list:mine",
			args:        []string{"pr", "list:mine"},
			wantContain: "harness list pr:mine",
		},
		{
			name:        "verb:variant transposition: pr execute:merge",
			args:        []string{"pr", "execute:merge"},
			wantContain: "harness execute pr:merge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.SuggestRootCommand(tt.args)
			if got == "" {
				t.Fatal("expected a suggestion, got empty string")
			}
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("suggestion %q does not contain %q", got, tt.wantContain)
			}
			if !strings.Contains(got, "Did you mean?") {
				t.Errorf("suggestion %q missing 'Did you mean?' header", got)
			}
		})
	}
}

func TestSuggestRootCommand_VerbTypo(t *testing.T) {
	r := buildTestRegistry(t)

	tests := []struct {
		name        string
		args        []string
		wantContain string
	}{
		{
			name:        "one transposed letter: creaet",
			args:        []string{"creaet", "pipeline"},
			wantContain: "harness create",
		},
		{
			name:        "one missing letter: lsit",
			args:        []string{"lsit", "pr"},
			wantContain: "harness list",
		},
		{
			name:        "one extra letter: listt",
			args:        []string{"listt", "pr"},
			wantContain: "harness list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.SuggestRootCommand(tt.args)
			if got == "" {
				t.Fatal("expected a suggestion, got empty string")
			}
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("suggestion %q does not contain %q", got, tt.wantContain)
			}
		})
	}
}

func TestSuggestRootCommand_NoSuggestion(t *testing.T) {
	r := buildTestRegistry(t)

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "completely unknown command",
			args: []string{"foobar"},
		},
		{
			name: "noun alone with no second arg",
			args: []string{"pr"},
		},
		{
			name: "noun + unknown second arg (not a verb)",
			args: []string{"pr", "foobar"},
		},
		{
			name: "valid verb + noun (correct order, should not intercept)",
			args: []string{"create", "pr"},
		},
		{
			name: "empty args",
			args: []string{},
		},
		{
			name: "known noun + known verb but combo not registered",
			args: []string{"connector", "create"}, // create connector not registered
		},
		{
			name: "verb typo too far: creaxyz",
			args: []string{"creaxyz", "pipeline"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.SuggestRootCommand(tt.args)
			if got != "" {
				t.Errorf("expected no suggestion, got %q", got)
			}
		})
	}
}

func TestSuggestRootCommand_FlagsStripped(t *testing.T) {
	r := buildTestRegistry(t)

	// Global flags before the noun-verb pair should not interfere.
	got := r.SuggestRootCommand([]string{"--profile", "prod", "pr", "create"})
	if got == "" {
		t.Fatal("expected a suggestion when global flags precede noun-verb pair")
	}
	if !strings.Contains(got, "harness create pr") {
		t.Errorf("suggestion %q does not contain 'harness create pr'", got)
	}
}
