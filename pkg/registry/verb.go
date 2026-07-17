// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

// Core verbs — correspond to harness_<verb> MCP tools.
const (
	VerbList     = "list"
	VerbGet      = "get"
	VerbCreate   = "create"
	VerbUpdate   = "update"
	VerbDelete   = "delete"
	VerbExecute  = "execute"
	VerbDescribe = "describe"
	VerbDiagnose = "diagnose"
	VerbSearch   = "search"
	VerbStatus   = "status"
)

// Leaf verbs — standalone commands with no noun and no subcommands.
// Only one registration per leaf verb is allowed.
const (
	VerbVersion = "version"
	VerbAsk     = "ask"
)

// Group verbs — top-level commands that own their own subcommands.
const (
	VerbAuth   = "auth"
	VerbPlugin = "plugin"
	VerbDebug  = "debug"
)

// Module-approved verbs (AR) — require client-side workflows, approved at framework level.
const (
	VerbPush      = "push"
	VerbPull      = "pull"
	VerbConfigure = "configure"
)

// Package manager verbs — install/uninstall for plugins, agents, mcp, and the CLI itself.
const (
	VerbInstall = "install"
)

// VerbKind classifies how a verb behaves in the command tree.
type VerbKind int

const (
	// VerbKindCore groups nouns under the verb: "harness <verb> <noun>".
	VerbKindCore VerbKind = iota
	// VerbKindLeaf is a standalone command with no noun and no subcommands.
	VerbKindLeaf
	// VerbKindGroup owns its own subcommands (auth, plugin, debug, …).
	VerbKindGroup
)

// VerbSpec captures all properties of a verb in one place.
type VerbSpec struct {
	Kind           VerbKind
	ShortDesc      string // used as the Short field on the parent group command
	Gerund         string // present-participle form for UX messages, e.g. "Deleting"
	HideGroup      bool   // hides the parent group command from --help
	SkipSetup      bool   // skips environment setup (EnsureHarnessHome etc); only version
	RequiresId     bool   // a positional <id> arg is mandatory; sets ctx.Id
	AllowsId       bool   // a positional <id> arg is optional; sets ctx.Id when present
	AllowsParentId bool   // an optional positional parentid arg is accepted; sets ctx.ParentId
}

// VerbOrder is the canonical display order for verbs in tables and help output.
var VerbOrder = []string{
	VerbList, VerbGet, VerbCreate, VerbUpdate, VerbDelete, VerbExecute,
	VerbDescribe, VerbDiagnose, VerbSearch, VerbStatus,
	VerbInstall, VerbPush, VerbPull, VerbConfigure,
}

// verbRegistry is the authoritative table of every allowed verb.
// To add a verb: add a const above and a row here.
var verbRegistry = map[string]VerbSpec{
	// Module-approved verbs
	VerbPush:      {Kind: VerbKindCore, Gerund: "pushing", ShortDesc: "Push an artifact to a Harness registry", RequiresId: true},
	VerbPull:      {Kind: VerbKindCore, Gerund: "pulling", ShortDesc: "Pull an artifact from a Harness registry", RequiresId: true},
	VerbInstall:   {Kind: VerbKindCore, Gerund: "installing", ShortDesc: "Install a Harness component", AllowsId: true},
	VerbConfigure: {Kind: VerbKindCore, Gerund: "configuring", ShortDesc: "Configure a local package manager client for a Harness registry", RequiresId: true},

	// Core verbs
	VerbList:     {Kind: VerbKindCore, Gerund: "listing", ShortDesc: "List Harness resources", AllowsParentId: true},
	VerbGet:      {Kind: VerbKindCore, Gerund: "getting", ShortDesc: "Get a Harness resource by identifier", RequiresId: true},
	VerbCreate:   {Kind: VerbKindCore, Gerund: "creating", ShortDesc: "Create a Harness resource", AllowsId: true},
	VerbUpdate:   {Kind: VerbKindCore, Gerund: "updating", ShortDesc: "Update a Harness resource", RequiresId: true},
	VerbDelete:   {Kind: VerbKindCore, Gerund: "deleting", ShortDesc: "Delete a Harness resource", RequiresId: true},
	VerbExecute:  {Kind: VerbKindCore, Gerund: "executing", ShortDesc: "Execute a Harness resource", RequiresId: true},
	VerbDescribe: {Kind: VerbKindCore, Gerund: "describing", ShortDesc: "Describe a Harness resource type"},
	VerbDiagnose: {Kind: VerbKindCore, Gerund: "diagnosing", ShortDesc: "Diagnose issues with a Harness resource"},
	VerbSearch:   {Kind: VerbKindCore, Gerund: "searching", ShortDesc: "Search for Harness resources"},
	VerbStatus:   {Kind: VerbKindCore, Gerund: "checking status of", ShortDesc: "Show status of a Harness resource"},

	// Leaf verbs
	VerbVersion: {Kind: VerbKindLeaf, SkipSetup: true},
	VerbAsk:     {Kind: VerbKindLeaf, Gerund: "asking"},

	// Group verbs
	VerbAuth:   {Kind: VerbKindGroup, ShortDesc: "Manage authentication profiles"},
	VerbPlugin: {Kind: VerbKindGroup, ShortDesc: "Manage CLI plugins"},
	VerbDebug:  {Kind: VerbKindGroup, HideGroup: true, AllowsId: true},
}
