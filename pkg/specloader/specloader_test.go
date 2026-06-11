// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package specloader

import (
	"bytes"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/harness/harness-cli/pkg/registry"
	"github.com/harness/harness-cli/pkg/spec"
)

// TestLoadAllEmbeddedSpecs verifies that every bundled *.spec.yaml parses
// without error and registers without conflicts. This catches duplicate nouns,
// duplicate commands, bad YAML, and unknown fields before they reach users.
func TestLoadAllEmbeddedSpecs(t *testing.T) {
	reg := registry.New()
	if err := LoadSpecs(reg); err != nil {
		t.Fatalf("LoadSpecs failed: %v", err)
	}
}

// TestDuplicateNoun ensures that loading two specs that declare the same noun
// produces a clear error rather than a silent override.
func TestDuplicateNoun(t *testing.T) {
	reg := registry.New()
	if err := parseAndLoad(reg, "a.spec.yaml", []byte(`
spec_version: 1
nouns:
  - noun: widget
    fields:
      - id: identifier
        expr: it.id
`)); err != nil {
		t.Fatalf("loading specA: %v", err)
	}
	err := parseAndLoad(reg, "b.spec.yaml", []byte(`
spec_version: 1
nouns:
  - noun: widget
    fields:
      - id: identifier
        expr: it.id
`))
	if err == nil {
		t.Fatal("expected error for duplicate noun, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate noun") {
		t.Errorf("error should mention 'duplicate noun', got: %v", err)
	}
}

// TestStrictYAML verifies strict mode rejects unknown top-level YAML keys.
func TestStrictYAML(t *testing.T) {
	reg := registry.New()
	reg.StrictYAML = true
	err := parseAndLoad(reg, "bad.spec.yaml", []byte(`
spec_version: 1
totally_unknown_root_key: oops
nouns:
  - noun: thing
    fields: []
`))
	if err == nil {
		t.Fatal("expected error for unknown field in strict mode, got nil")
	}
}

// TestMinimalSpec confirms that a minimal valid spec loads cleanly.
func TestMinimalSpec(t *testing.T) {
	reg := registry.New()
	if err := parseAndLoad(reg, "minimal.spec.yaml", []byte(`
spec_version: 1
nouns:
  - noun: thing
    fields:
      - id: identifier
        expr: it.id
commands:
  - command: list thing
    verb: list
    noun: thing
    short: List things
    handler_type: endpoint
    endpoint:
      path: /api/things
      items_expr: it
      paging:
        paging_strategy: flat_list
`)); err != nil {
		t.Fatalf("minimal spec failed: %v", err)
	}
}

// parseAndLoad mirrors LoadSpec but accepts raw bytes instead of reading from
// embed.FS, allowing unit tests to exercise the parse-and-register path.
func parseAndLoad(reg *registry.Registry, name string, data []byte) error {
	var f specFile
	if reg.StrictYAML {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil {
			return specParseError(name, data, err)
		}
	} else if err := yaml.Unmarshal(data, &f); err != nil {
		return specParseError(name, data, err)
	}
	if f.SpecVersion < MinSpecVersion || f.SpecVersion > MaxSpecVersion {
		return specParseError(name, data, nil)
	}
	for i, nd := range f.Nouns {
		if err := reg.RegisterNoun(nd); err != nil {
			return wrapSpecErr(name, "noun", i, err)
		}
	}
	mod := reg.Module(strings.TrimSuffix(name, ".spec.yaml"))
	for i, cmd := range f.Commands {
		if cmd == nil {
			continue
		}
		cmd.SpecFile = name
		if err := mod.Register(cmd); err != nil {
			return wrapSpecErr(name, "command", i, err)
		}
	}
	return nil
}

func wrapSpecErr(name, kind string, i int, err error) error {
	return &specErr{name: name, kind: kind, index: i, err: err}
}

type specErr struct {
	name, kind string
	index      int
	err        error
}

func (e *specErr) Error() string {
	return "spec: " + e.name + " " + e.kind + "[" + strings.Repeat("x", e.index) + "]: " + e.err.Error()
}

func (e *specErr) Unwrap() error { return e.err }

// satisfy interface — test uses strings.Contains, not errors.As
var _ error = (*specErr)(nil)

// Ensure specFile is accessible (it's defined in specloader.go, same package).
var _ = specFile{}
var _ = spec.NounDef{}
