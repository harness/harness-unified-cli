// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// ModuleOrder defines the preferred display order for known modules.
// Modules not listed here appear after these, in alphabetical order.
var ModuleOrder = []string{"core", "platform", "pipeline", "cd"}

//go:embed *.spec.yaml
var specsFS embed.FS

// Files returns the names of all embedded *.spec.yaml files.
func Files() []string {
	entries, _ := fs.Glob(specsFS, "*.spec.yaml")
	return entries
}

// Read returns the raw contents of a named spec file.
func Read(name string) ([]byte, error) {
	return specsFS.ReadFile(name)
}

// HandlerType identifies how a command is dispatched.
type HandlerType string

const (
	HandlerWorkflow HandlerType = "workflow"
	HandlerEndpoint HandlerType = "endpoint"
)

// Valid confirm_mode values for CommandSpec.
const (
	ConfirmNone   = ""
	ConfirmPrompt = "prompt"
	ConfirmID     = "confirm_id"
)

// Valid update_strategy values for EndpointSpec.
const (
	UpdateStrategyGetThenPut   = "get-then-put"
	UpdateStrategyGetThenPutKV = "get-then-put-kv"
)

// Valid create_strategy values for EndpointSpec.
const (
	CreateStrategySetFields = "set-fields"
)

// Valid file_body values for EndpointSpec and CommandSpec.
const (
	FileBodyNone     = ""         // no file body support
	FileBodyOptional = "optional" // -f accepted; falls back to other strategy if omitted
	FileBodyRequired = "required" // -f is mandatory; error if omitted
)

// Valid paging_strategy values for PagingSpec.
const (
	PagingStrategyPageIndex  = "page_index"  // API accepts pageIndex + pageSize; response has totalItems, content, empty
	PagingStrategyPageHeader = "page_header" // v1 API: bare array body; total/page info in X-Total-Elements / X-Page-Number / X-Page-Size response headers
	PagingStrategyFlatList   = "flat_list"   // API returns all items in one shot; offset/limit applied client-side
	PagingStrategyNone       = "none"        // API returns everything; offset/limit applied client-side
	PagingStrategyCursor     = "cursor"      // token-based iteration; no random access, not countable (not yet implemented)
)

// BuiltinFlags enables predefined system flags that have fixed registration and dispatch behavior.
type BuiltinFlags struct {
	Page bool `yaml:"page,omitempty"` // --page N (1-indexed); exposed in expr as integer flags.page = N-1
	Set  bool `yaml:"set,omitempty"`  // --set key=value (repeatable); parsed into ctx.SetArgs
	Del  bool `yaml:"del,omitempty"`  // --del key (repeatable); parsed into ctx.DelArgs
	UI   bool `yaml:"ui,omitempty"`   // --ui launch interactive TUI (requires both stdin and stdout to be a TTY)
}

// Flag declares a command-specific flag surfaced in --help and validated before dispatch.
type Flag struct {
	Name             string   `yaml:"name"`
	Short            string   `yaml:"short,omitempty"` // single-char shorthand; use sparingly — prefer reserving shorthands for core flags
	Default          string   `yaml:"default,omitempty"`
	Description      string   `yaml:"description,omitempty"`
	Required         bool     `yaml:"required,omitempty"`          // if true, command errors if flag is not provided
	IsBool           bool     `yaml:"is_bool,omitempty"`           // if true, declares a bool flag instead of a string flag
	IsArray          bool     `yaml:"is_array,omitempty"`          // if true, value is parsed as []string: comma-separated or JSON array syntax
	IsMulti          bool     `yaml:"is_multi,omitempty"`          // if true, registers as StringArray (repeatable: --flag v1 --flag v2)
	Hidden           bool     `yaml:"hidden,omitempty"`            // if true, flag is registered but not shown in --help output
	CompletionNoun   string   `yaml:"completion_noun,omitempty"`   // list noun to call for dynamic tab-completion
	CompletionFn     string   `yaml:"completion_fn,omitempty"`     // registered FlagCompletionFn name (overrides completion_noun)
	CompletionValues []string `yaml:"completion_values,omitempty"` // static list of completion values (overrides completion_noun/fn)
	ParentFromArg    int      `yaml:"parent_from_arg,omitempty"`   // positional arg index to use as parentId when calling completion_noun list endpoint
}

// ModuleMeta holds metadata declared at the top level of a spec file.
type ModuleMeta struct {
	Name           string
	Type           string // e.g. "builtin"
	Desc           string
	Core           bool     // true for CLI-internal modules (auth, mgmt) that are hidden from "list module"
	HelpText       string   // contents of <module>.help.txt, empty if none
	NounOrder      []string // noun names in spec-file declaration order, for conceptual ordering
	ExternalBinary string   // when set, commands in this module are dispatched to this external binary
}

// VerbInfo carries display metadata for a registered verb.
type VerbInfo struct {
	Verb      string
	ShortDesc string
	Gerund    string
}

// NounDef defines the canonical field vocabulary for a noun.
// Declared at the top level of a spec file; commands look up their fields by noun name.
type NounDef struct {
	Noun      string     `yaml:"noun"`
	ShortDesc string     `yaml:"short_desc,omitempty"`
	Fields    []FieldDef `yaml:"fields"`
	// UrlPath is an optional UI URL template using the same {{expr}} syntax as endpoint paths.
	// "it" is bound to the response item, so column exprs can call url(it) to get a
	// clickable link to the resource in the Harness UI. Returns "" when empty.
	UrlPath string `yaml:"url_path,omitempty"`
	// MultiLevel indicates the noun can exist at account, org, or project scope.
	// Set explicitly in the spec YAML on nouns that support --level.
	MultiLevel bool `yaml:"multi_level,omitempty"`
	// NounAliases lists alternate names for this noun (e.g. "org", "orgs" for "organization").
	// Alias commands are wired up as hidden cobra commands and do not appear in help output.
	NounAliases []string `yaml:"noun_aliases,omitempty"`
}

// FieldDef defines a named, reusable field for a noun. Fields declared here can be
// referenced by ID in fields_subset, fields_extra, and table columns lists.
//
// Two mutually exclusive forms:
//   - Read-only: set Expr. Value is derived by evaluating Expr against the item.
//   - Editable:  set Path. Path is the dot-path to the value in the item (e.g. "it.project.name").
//     FormatExpr may optionally override display formatting; when absent, Path is used as
//     the display expression. Editable fields participate in "update" commands.
type FieldDef struct {
	// ID is required: the slug used in columns:, fields_subset:, fields_extra:, and --columns flag.
	// Must be lowercase with underscores (e.g. "last_run_by").
	ID string `yaml:"id"`
	// Label is the human-readable column header / text label.
	// When omitted, auto-derived from ID by replacing underscores with spaces and title-casing.
	Label string `yaml:"label,omitempty"`
	// Expr is a required expr-lang expression evaluated against the item ("it") for display.
	Expr string `yaml:"expr,omitempty"`
	// MutablePath is the dot-path relative to the update_body_pick subtree (e.g. "name",
	// "spec.value"). Must not start with "it.". Its presence marks the field as writable
	// and should match update_body_pick on the corresponding update command.
	MutablePath string `yaml:"mutable_path,omitempty"`
	// FieldType optionally changes how the value is rendered or updated.
	// Supported: "multiline_text" (renders raw block below other fields), "yaml" (alias for multiline_text, use for actual YAML content),
	// "tags" (tag-map handling), "set" (string-set handling), "ts" (epoch-ms timestamp).
	FieldType string `yaml:"field_type,omitempty"`
	// Align controls horizontal alignment in the table renderer.
	// Supported: "right". Empty means left (default).
	Align string `yaml:"align,omitempty"`
	// WidthMax caps the column width in characters; the renderer wraps longer values.
	// Zero means unconstrained.
	WidthMax int `yaml:"width_max,omitempty"`
}

// Validate checks that the field definition is internally consistent.
func (f FieldDef) Validate() error {
	if f.Expr == "" {
		return fmt.Errorf("field %q: expr is required", f.ID)
	}
	if strings.HasPrefix(f.MutablePath, "it.") {
		return fmt.Errorf("field %q: mutable_path must not start with \"it.\" (it is relative to the update_body_pick subtree)", f.ID)
	}
	return nil
}

// TableSpec is a declarative table definition used internally by the rendering layer.
// Not exposed in spec YAML — use fields: + columns: instead.
type TableSpec struct {
	Columns []TableColumn
}

// TableColumn is one column in a TableSpec.
type TableColumn struct {
	Header    string
	Expr      string
	Align     string
	FieldType string
	WidthMax  int
}

// CompletionSpec drives dynamic tab-completion for the <id> positional argument.
// IdExpr and NameExpr are expr-lang expressions evaluated against each item; "it" is bound to the item.
type CompletionSpec struct {
	IdExpr         string `yaml:"id_expr"`
	NameExpr       string `yaml:"name_expr"`
	NoSearchInject bool   `yaml:"no_search_inject,omitempty"`
}

// CompletionSeqStep describes one segment of a slash-delimited multi-part ID completion.
// Index 0 completes the first segment (e.g. registry), index 1 completes the second
// (e.g. artifact), and so on. The already-typed segments are joined and passed as
// ParentId when calling the list endpoint, so parentIdParts resolves correctly in path templates.
type CompletionSeqStep struct {
	CompletionNoun string   `yaml:"completion_noun"`
	StaticValues   []string `yaml:"static_values,omitempty"` // fixed completion list for this step (mutually exclusive with completion_noun)
	KeepOrder      bool     `yaml:"keep_order,omitempty"`
}

// PagingSpec declares the paging model for a list endpoint.
// The framework uses this to implement --offset, --limit, --all, and --count
// transparently across different API paging styles.
type PagingSpec struct {
	// PagingStrategy identifies the paging style. See PagingStrategy* consts.
	PagingStrategy string `yaml:"paging_strategy"`

	// Countable indicates whether the API returns a total item count in its response.
	// When true, --count is supported. When false, --count returns an error.
	// PagingStrategyNone always supports --count (client-side length). PagingStrategyCursor never does.
	Countable bool `yaml:"countable,omitempty"`

	// page_index model fields
	PageIndexParam  string `yaml:"page_index_param,omitempty"`  // query param name, e.g. "pageIndex"
	PageSizeParam   string `yaml:"page_size_param,omitempty"`   // query param name, e.g. "pageSize"
	PageSizeDefault int    `yaml:"page_size_default,omitempty"` // default page size (e.g. 50)
	PageSizeMax     int    `yaml:"page_size_max,omitempty"`     // max page size the API accepts (e.g. 100)

	// TotalExpr is an expression evaluated against the raw response (with "it" bound to the response root).
	// Resolves to the total item count, e.g. "it.data.totalItems". Required for page_index model.
	TotalExpr string `yaml:"total_expr,omitempty"`
	// PageBase is added to every page number sent to the API. Default 0 (0-based).
	// Set to 1 for APIs that require page >= 1. page_index model only.
	PageBase int `yaml:"page_base,omitempty"`

	// page_header model fields
	// TotalHeader is the response header name to read for the total item count. Default "X-Total-Elements".
	TotalHeader string `yaml:"total_header,omitempty"`
}

// IsCountable reports whether --count is supported for this paging spec.
// flat_list always supports it (total is the client-side length); other strategies
// require countable: true in the spec.
func (p *PagingSpec) IsCountable() bool {
	return p.Countable || p.PagingStrategy == PagingStrategyFlatList
}

// EndpointSpec describes a single Harness API call.
//
// Path is a template with {placeholders}. PathParams maps flag names to placeholder
// names. QueryParams maps flag names to query param names — the framework resolves
// org/project/account automatically from auth; only resource-specific params go here.
type EndpointSpec struct {
	// Path is the API path template, e.g. "/v1/orgs/{org}/projects/{project}/pipelines"
	Path string `yaml:"path"`
	// Method is the HTTP method. Defaults to "GET" if empty.
	Method string `yaml:"method,omitempty"`
	// PathParams maps flag-name → placeholder in Path.
	PathParams map[string]string `yaml:"path_params,omitempty"`
	// QueryParams maps query param name → expr-lang expression. The param is omitted
	// when the expression returns empty. Flags are available as flags.<name>.
	QueryParams map[string]string `yaml:"query_params,omitempty"`
	// BodyParams maps dot-path in the JSON body → expr-lang expression.
	// Supports nested paths: {"config.type": "flags.type"} sets body["config"]["type"].
	// Expressions have access to ctx, auth, flags, coalesce(), formatTags(), etc.
	BodyParams map[string]string `yaml:"body_params,omitempty"`
	// RequestHeaders maps HTTP header name → expr-lang expression.
	// Headers are evaluated against the command context (auth, flags, ctx) and injected
	// into the request. Useful for APIs that require custom headers, e.g. x-tenant-id.
	// Example: {"x-tenant-id": "auth.account"}
	RequestHeaders map[string]string `yaml:"request_headers,omitempty"`
	// FieldExtract, when non-empty, extracts this top-level string field from the
	// JSON response object and prints it as a raw string instead of JSON.
	FieldExtract string `yaml:"field_extract,omitempty"`
	// ItemsExpr is an expr-lang expression that resolves to the []any of items in a
	// list response. Required for all VerbList commands; not allowed on any other verb.
	// Use "it" for bare arrays. "it" is bound to the full response; ctx, auth, flags,
	// and helpers are also available.
	ItemsExpr string `yaml:"items_expr,omitempty"`
	// ItemItemExpr, when non-empty, is evaluated against each list item to unwrap it to a
	// canonical shape (e.g. "it.project" unwraps {project:{...}} to the project object).
	// This lets list and get share field definitions when the get item_expr produces the
	// same shape as the unwrapped list item.
	ItemItemExpr string `yaml:"item_item_expr,omitempty"`
	// ItemExpr is an expr-lang expression that resolves to the single item in a get
	// response. Required for all VerbGet commands. Use "it" for bare item responses.
	// "it" is bound to the full response; ctx, auth, flags, and helpers are also available.
	ItemExpr string `yaml:"item_expr,omitempty"`
	// YamlPickExpr, when non-empty, enables --format yaml on get commands and defines which
	// subtree of the raw API response to emit. Evaluated from root ("it" = full response).
	// Should produce an object that round-trips cleanly with the corresponding update -f.
	YamlPickExpr string `yaml:"yaml_pick_expr,omitempty"`
	// GetIdExpr, when non-empty, is an expr-lang expression evaluated against each list
	// item to produce the composite id suitable for passing to the corresponding get command.
	// "it" is bound to the item; parentId/parentIdParts are also available.
	// When absent, the feature is disabled for this command.
	GetIdExpr string `yaml:"get_id_expr,omitempty"`
	// Completion, when non-nil, enables dynamic tab-completion for list commands.
	// IdExpr and NameExpr are expr-lang expressions into each item from ItemsExpr.
	Completion *CompletionSpec `yaml:"completion,omitempty"`
	// NoFields, when true, suppresses all field rendering (noun fields and fields_extra).
	// Use with text_header/text_footer for commands whose response has no displayable fields.
	NoFields bool `yaml:"no_fields,omitempty"`
	// FieldsSubset lists field IDs from the noun that this command's API actually returns.
	// When set, --list-columns only advertises these IDs.
	FieldsSubset []string `yaml:"fields_subset,omitempty"`
	// FieldsExtra declares additional fields available only on this command (e.g. list-only
	// computed columns like sparklines). Appended after the noun's fields (or fields_subset).
	FieldsExtra []FieldDef `yaml:"fields_extra,omitempty"`
	// Columns lists field IDs (from fields: or fields_from:) to display by default in table output.
	// When omitted and fields are defined, all fields are shown. Enables --format table|tsv|json.
	Columns []string `yaml:"columns,omitempty"`
	// FileBody controls whether -f/--file is added to the command.
	// "optional": accepted, falls back to other strategy if omitted.
	// "required": -f is mandatory; error if omitted.
	FileBody string `yaml:"file_body,omitempty"`
	// ContentType overrides the default Content-Type header. Only used when
	// FileBody is set; defaults to "application/json".
	ContentType string `yaml:"content_type,omitempty"`
	// TextFormatter names a registered TextFormatterFn used when --format text.
	TextFormatter string `yaml:"text_formatter,omitempty"`
	// TextHeader and TextFooter are optional {{expr}}-interpolated strings printed
	// before and after the fields block. Rendered only when non-empty after interpolation.
	TextHeader string `yaml:"text_header,omitempty"`
	TextFooter string `yaml:"text_footer,omitempty"`
	// BodyFn names a registered CreateBodyFn that builds the POST body instead of the
	// default body_params / body construction. Qualified by module at registration time.
	BodyFn string `yaml:"body_fn,omitempty"`
	// ValidatorsEndpoint lists registered EndpointValidatorFn IDs to run after the
	// request is built but before it is sent. Each fn receives ctx and the materialized
	// request; return a non-nil error to abort. Qualified by module at registration time.
	ValidatorsEndpoint []string `yaml:"validators_endpoint,omitempty"`
	// Paging, when non-nil, enables framework-managed paging for list commands.
	// Not allowed on any other verb. Exposes --offset, --limit, --all, and (when countable) --count.
	Paging *PagingSpec `yaml:"paging,omitempty"`
	// UpdateStrategy declares how update commands mutate the resource.
	// "get-then-put": GET the resource first, apply --set/--del mutations locally, then PUT.
	// "get-then-put-kv": GET a [{key,value}] array, apply --set/--del by key, then PUT the full array.
	// Only valid when method is PUT.
	UpdateStrategy string `yaml:"update_strategy,omitempty"`
	// UpdateBodyPick is an expr evaluated against the raw GET response root ("it" = full
	// response) to extract the mutable subtree before mutation and re-wrap. e.g. "it.data.project".
	// Should match yaml_pick_expr on the corresponding get command (they describe the same subtree).
	UpdateBodyPick string `yaml:"update_body_pick,omitempty"`
	// UpdateBodyWrap is the key name used to re-wrap the picked subtree in the PUT body.
	// e.g. "project" → PUT body becomes {"project": <mutated subtree>}
	UpdateBodyWrap string `yaml:"update_body_wrap,omitempty"`
	// GetPath overrides path for the GET leg of get-then-put and get-then-put-kv.
	// Use when the GET and PUT endpoints have different paths (e.g. PUT /connectors, GET /connectors/{id}).
	// When absent, path is used for both legs.
	GetPath string `yaml:"get_path,omitempty"`
	// GetQueryParams overrides query_params for the GET leg of get-then-put-kv.
	// Use when the GET and PUT endpoints require different query parameters.
	GetQueryParams map[string]string `yaml:"get_query_params,omitempty"`
	// CreateStrategy declares how create commands build the POST body from --set args.
	// "set-fields": seed from create_body_init, apply --set mutations, wrap under create_body_wrap, then POST.
	// Enables --set / positional key=value args and --list-fields on create commands.
	CreateStrategy string `yaml:"create_strategy,omitempty"`
	// CreateBodyInit is a map of dot-path → expr-lang expression used to seed the
	// initial object before --set mutations are applied. Used to inject required fields
	// like identifier and name defaults. e.g. {"identifier": "ctx.id", "name": "coalesce(setArgs.name, ctx.id)"}
	CreateBodyInit map[string]string `yaml:"create_body_init,omitempty"`
	// CreateBodyWrap is the key name used to wrap the body before POST.
	// e.g. "project" → POST body becomes {"project": <mutated object>}
	CreateBodyWrap string `yaml:"create_body_wrap,omitempty"`
	// FetchFn names a registered FetchFn used instead of the default HTTP paging
	// machinery. Used for in-memory or config-file backed list commands (e.g. "list noun").
	// When empty, HTTPFetchFn is used.
	FetchFn string `yaml:"fetch_fn,omitempty"`
}

// CommandSpec fully describes one CLI command.
//
// Exception verbs (version, auth, …) have an empty Noun and appear at root level.
// Core verbs always have a Noun and nest as "harness <verb> <noun>".
type CommandSpec struct {
	// Command is the full command name ("verb noun:variant", or just "verb" for noun-less commands).
	// Redundant — always equals Verb+" "+FullNoun() — but declared first in every spec entry so
	// any command can be found with a single grep: grep "command: list pipeline" *.spec.yaml
	Command          string              `yaml:"command"`
	Verb             string              `yaml:"verb"`
	Noun             string              `yaml:"noun,omitempty"`         // base noun; empty for management exceptions
	NounVariant      string              `yaml:"noun_variant,omitempty"` // optional variant suffix; produces "noun:variant" cobra subcommand
	Short            string              `yaml:"short,omitempty"`
	Long             string              `yaml:"long,omitempty"`
	RequiresId       bool                `yaml:"requires_id,omitempty"`       // positional [id] is mandatory for this command
	NoId             bool                `yaml:"no_id,omitempty"`             // opt out of the verb's default RequiresId (e.g. singleton get commands)
	IdLabel          string              `yaml:"id_label,omitempty"`          // overrides "<id>" in the Usage line (e.g. "<registry/path>")
	ArgsLabel        string              `yaml:"args_label,omitempty"`        // appended to Usage after the id label (e.g. "<local-file>"); only used when has_args is true
	IdParts          int                 `yaml:"id_parts,omitempty"`          // when > 1, id must contain exactly (id_parts-1) "/" separators; parts available as {ctx:id_part:0}, {ctx:id_part:1}, ...
	IdAllowSlash     bool                `yaml:"id_allow_slash,omitempty"`    // skip the slash-count validation on id (use when the id format has variable segments)
	RequiresParentId bool                `yaml:"requires_parentid,omitempty"` // list commands only: makes the [parentid] arg mandatory
	ParentIdLabel    string              `yaml:"parentid_label,omitempty"`    // overrides "[parentid]" in the Usage line for list commands (e.g. "<registry/name>")
	Hidden           bool                `yaml:"hidden,omitempty"`
	DevOnly          bool                `yaml:"dev_only,omitempty"` // skipped at registration time when not a dev build
	NoAuth           bool                `yaml:"no_auth,omitempty"`  // set to true to skip auth resolution (auth subcommands, version)
	BuiltinFlags     BuiltinFlags        `yaml:"flags_builtin,omitempty"`
	HasArgs          bool                `yaml:"has_args,omitempty"` // accepts extra positional args beyond [id]; parsed into ctx.Args
	HandlerType      HandlerType         `yaml:"handler_type"`
	VerbHandler      string              `yaml:"verb_handler,omitempty"`    // overrides verb for behavioral dispatch (flag binding, ctx.Verb); leave unset to use verb
	ConfirmMode      string              `yaml:"confirm_mode,omitempty"`    // not allowed on list or get; see ConfirmNone/ConfirmPrompt/ConfirmID
	WorkflowID       string              `yaml:"workflow_id,omitempty"`     // set when HandlerType == HandlerWorkflow
	FollowFn         string              `yaml:"follow_fn,omitempty"`       // optional: called after a successful endpoint command when --follow is set
	Flags            []Flag              `yaml:"flags,omitempty"`           // custom flags for workflow commands
	Endpoint         *EndpointSpec       `yaml:"endpoint,omitempty"`        // set when HandlerType == HandlerEndpoint
	FieldsNoun       string              `yaml:"fields_noun,omitempty"`     // override noun used for field lookup when the command's shape differs from its noun
	CompletionNoun   string              `yaml:"completion_noun,omitempty"` // override noun used to find the list spec for <id> completion
	CompletionSeq    []CompletionSeqStep `yaml:"completion_seq,omitempty"`  // slash-delimited multi-part ID completion; overrides completion_noun when set
	Module           string              `yaml:"-"`                         // set at registration time by ModuleRegistrar; drives workflow/formatter namespacing
	SpecFile         string              `yaml:"-"`                         // spec filename, set at load time; used in error messages
	External         bool                `yaml:"-"`                         // set at registration time on the main binary when the module has an external_binary; never in spec YAML
}

// FullNoun returns "noun:variant" when NounVariant is set, otherwise just Noun.
// Use this wherever the cobra subcommand name or command identity is needed.
// Use Noun directly when looking up field definitions or completion sources (base noun only).
func (cs *CommandSpec) FullNoun() string {
	if cs.NounVariant != "" {
		return cs.Noun + ":" + cs.NounVariant
	}
	return cs.Noun
}
