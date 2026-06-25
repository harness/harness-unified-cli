// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import "github.com/spf13/pflag"

type flagKind int

const (
	flagKindString flagKind = iota
	flagKindBool
	flagKindInt
)

type flagSpec struct {
	name    string
	short   string
	defStr  string
	defInt  int
	defBool bool
	usage   string
	kind    flagKind
}

var (
	specFormat      = flagSpec{name: "format", usage: "Output format: json, yaml, jsonl, table, csv, tsv"}
	specJson        = flagSpec{name: "json", kind: flagKindBool, usage: "Output as JSON (shorthand for --format json)"}
	specYaml        = flagSpec{name: "yaml", kind: flagKindBool, usage: "Output as YAML (shorthand for --format yaml)"}
	specColumns     = flagSpec{name: "columns", usage: `Columns to display by ID or expr, e.g. "name,org" or "+sparkline" or "Name:it.name"`}
	specNoHeaders   = flagSpec{name: "no-headers", kind: flagKindBool, usage: "Suppress column headers (table/csv/tsv) and paging footer (table)"}
	specOut         = flagSpec{name: "out", short: "o", usage: "Write output to file instead of stdout"}
	specRaw         = flagSpec{name: "raw", kind: flagKindBool, usage: "Output the full raw API response (only with --format json)"}
	specFile        = flagSpec{name: "file", short: "f", usage: "Read request body from file, or - for stdin"}
	specListColumns = flagSpec{name: "list-columns", kind: flagKindBool, usage: "Print available column IDs and exit (use with --columns to customize output)"}
	specFields      = flagSpec{name: "fields", usage: `Fields to extract, tab-separated on one line, e.g. "name" or "name,git_url"`}
	specListFields  = flagSpec{name: "list-fields", kind: flagKindBool, usage: "Print available field IDs and exit (use with --fields to customize output)"}
	specPage        = flagSpec{name: "page", kind: flagKindInt, defInt: 1, usage: "Page number (1-indexed)"}
	specLevel       = flagSpec{name: "level", defStr: "", usage: "Scope level: project, org, or account (overrides prefix on id)"}
	specOffset      = flagSpec{name: "offset", kind: flagKindInt, usage: "Skip the first N items (item-level)"}
	specLimit       = flagSpec{name: "limit", kind: flagKindInt, usage: "Return at most N items"}
	specAll         = flagSpec{name: "all", kind: flagKindBool, usage: "Fetch all pages (incompatible with --offset and --limit)"}
	specCount       = flagSpec{name: "count", kind: flagKindBool, usage: "Print total item count and exit (incompatible with --offset, --limit, --all)"}
	specUI          = flagSpec{name: "ui", kind: flagKindBool, usage: "Launch interactive TUI (requires a TTY)"}

	specLevelValues = []string{"project", "org", "account"}
)

func addFlag(f *pflag.FlagSet, spec flagSpec) {
	switch spec.kind {
	case flagKindBool:
		if spec.short != "" {
			f.BoolP(spec.name, spec.short, spec.defBool, spec.usage)
		} else {
			f.Bool(spec.name, spec.defBool, spec.usage)
		}
	case flagKindInt:
		if spec.short != "" {
			f.IntP(spec.name, spec.short, spec.defInt, spec.usage)
		} else {
			f.Int(spec.name, spec.defInt, spec.usage)
		}
	default:
		if spec.short != "" {
			f.StringP(spec.name, spec.short, spec.defStr, spec.usage)
		} else {
			f.String(spec.name, spec.defStr, spec.usage)
		}
	}
}

func addFlags(f *pflag.FlagSet, specs ...flagSpec) {
	for _, s := range specs {
		addFlag(f, s)
	}
}
