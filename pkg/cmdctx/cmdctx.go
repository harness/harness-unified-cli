// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package cmdctx

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/harness/cli/pkg/auth"
	"github.com/harness/cli/pkg/spec"
)

// TimeoutError is the cause set on the context when --timeout expires.
type TimeoutError struct {
	Secs float64
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("command timed out after %gs", e.Secs)
}

// IsTimeout reports whether err (or any error in its chain) is a TimeoutError.
func IsTimeout(err error) bool {
	var t *TimeoutError
	return errors.As(err, &t)
}

// DataAccessor provides typed field access over a response value.
// Paths are expr-lang expressions with "it" bound to the root data.
type DataAccessor interface {
	GetString(path string) string
	GetInt64(path string) int64
	GetBool(path string) bool
	GetTs(path string) string
	GetData() any
	GetSlice(path string) []any
}

// TextFormatterFn is a human-readable text renderer for a single item.
type TextFormatterFn func(w io.Writer, d DataAccessor) error

// CreateBodyFn builds the POST/PUT request body for an endpoint command.
type CreateBodyFn func(ctx *Ctx) (any, error)

// QueryParamsFn returns extra query parameters to merge into a request.
// It is called after the spec's query_params CEL expressions are evaluated,
// and its results are merged on top (so it can override CEL values too).
// Only takes effect on GET and list requests; it is not called for
// update/create strategies (get-then-put, set-fields) which manage their
// own query params internally.
type QueryParamsFn func(ctx *Ctx) (map[string]string, error)

// FollowFn is called after a successful endpoint command when --follow is set.
// result is the raw API response (same value RunEndpoint returns).
type FollowFn func(ctx *Ctx, result any) error

// EndpointValidatorFn validates a request before it is sent. Declared via
// validators_endpoint in the spec; runs after file slurp, before the HTTP call.
// req.Body is the fully materialized request body (string or structured value).
type EndpointValidatorFn func(ctx *Ctx, req EndpointRequest) error

// EndpointRequest is the materialized form of a pending API request passed to
// EndpointValidatorFns. Body mirrors client.Request.Body: a string when the
// request body came from a -f file, a map/struct for structured bodies, or nil.
type EndpointRequest struct {
	Method      string
	Path        string
	QueryParams map[string]string
	Body        any
	ContentType string
}

// PageResult is returned by a FetchFn for one logical page of results.
type PageResult struct {
	Raw         any
	Items       []any
	StartOffset int
	Last        bool
	HasTotal    bool
	Total       int64
	NextCursor  any
}

// FetchFn fetches one page of results. cursor is nil on the first call.
// Implementations may ignore wantStart/wantCount/cursor and return everything
// at once — the driver's slice math handles the window.
type FetchFn func(ctx *Ctx, ep *spec.EndpointSpec, wantStart, wantCount int, cursor any) (*PageResult, error)

// RawBody signals that the body should be sent as-is with the given ContentType,
// bypassing JSON encoding. Return this from a CreateBodyFn when the API expects
// a raw non-JSON body (e.g. application/yaml).
type RawBody struct {
	ContentType string
	Content     string
}

// FlagResolveFn transforms a raw flag string value before it is placed into
// the CEL expression environment. Returning ("", nil) is valid and leaves the
// flag value as an empty string. Returning an error aborts the command.
type FlagResolveFn func(ctx *Ctx, raw string) (string, error)

// Resolver looks up registered handler functions by their fully-qualified ID.
// The registry implements this; commands receive it via Ctx.Resolver.
type Resolver interface {
	ResolveTextFormatter(id string) TextFormatterFn
	ResolveBodyFn(id string) CreateBodyFn
	ResolveQueryParamsFn(id string) QueryParamsFn
	ResolveFlagResolveFn(id string) FlagResolveFn
	ResolveFetchFn(id string) (FetchFn, error)
	ResolveEndpointValidator(id string) EndpointValidatorFn
	GetSpec(verb, noun string) *spec.CommandSpec
	GetNoun(noun string) *spec.NounDef
	// ResolveNounAlias returns the canonical noun name for the given alias, or "" if not an alias.
	ResolveNounAlias(alias string) string
	RunEndpoint(ctx *Ctx, ep *spec.EndpointSpec) (any, error)
	// FormatList renders rows through the standard list formatting pipeline (table/json/csv/tsv).
	// fields declares the available columns; columnIDs sets the default column order (nil = all).
	FormatList(ctx *Ctx, rows []any, fields []spec.FieldDef, columnIDs []string) error
	// FetchItems fetches all items satisfying pf from a paging-enabled list endpoint.
	// Returns raw items as extracted by the endpoint's items_expr — no field unwrapping.
	FetchItems(ctx *Ctx, ep *spec.EndpointSpec, pf PagingFlags) ([]any, error)
	// GetModuleMetas returns metadata for all loaded modules in load order.
	GetModuleMetas() []spec.ModuleMeta
	// GetSpecsForModule returns all registered CommandSpecs belonging to the given module.
	GetSpecsForModule(module string) []*spec.CommandSpec
	// GetAllSpecs returns every registered CommandSpec across all modules.
	GetAllSpecs() []*spec.CommandSpec
	// GetVerbInfos returns display metadata for all verbs that have at least one registered command.
	GetVerbInfos() []spec.VerbInfo
	// ResolveCommandFields returns the effective []FieldDef for a command: noun fields (or
	// fields_noun override), filtered by fields_subset, with fields_extra appended.
	ResolveCommandFields(cs *spec.CommandSpec) []spec.FieldDef
}

// FormatFlags holds all output-formatting flags (format, columns, headers, file, raw).
// Fields are zero-valued when the command did not declare support for them.
type FormatFlags struct {
	Format    string
	Columns   string
	Fields    string // --fields: comma-separated field IDs; outputs tab-separated values (get commands only)
	NoHeaders bool
	OutFile   string
	Raw       bool
}

// PagingFlags holds the user-facing paging control flags for list commands.
// Only populated when the endpoint declares a paging model.
// Count is mutually exclusive with Offset, Limit, and All — the caller validates this.
type PagingFlags struct {
	Offset int  // item-level offset (not page-level)
	Limit  int  // max items to return; 0 means use page_size_default
	All    bool // fetch all pages, overrides Offset and Limit
	Count  bool // return total count instead of items; incompatible with Offset/Limit/All
	// OnPage, if set, is invoked once per fetched page with the number of items
	// accumulated so far and the reported grand total (hasTotal is false when the
	// paging strategy does not return one). Used to surface progress on slow,
	// many-page fetches (e.g. thousands of orgs during login).
	OnPage func(fetched int, total int64, hasTotal bool)
}

// GlobalFlags is reserved for future non-formatting global flags.
type GlobalFlags struct{}

// Ctx is passed to every workflow handler, providing resolved auth and the parsed command identity.
// Auth is nil for management commands (version, etc.) that do not require credentials.
// When Auth is non-nil, OrgID and ProjectID already reflect any --org/--project overrides.
type Ctx struct {
	Context      context.Context
	CancelFn     context.CancelCauseFunc
	Auth         *auth.ResolvedAuth
	Verb         string
	VerbHandler  string // behavioral dispatch verb; defaults to Verb when verb_handler is unset in spec
	Noun         string
	FieldsNoun   string // overrides Noun for field lookup when set (from spec fields_noun)
	Id           string
	ParentId     string            // optional parent-id arg for list commands (e.g. pipeline ID on "list execution")
	SetArgs      map[string]string // --set key=value pairs for update verb (when HasSetArg set on spec)
	DelArgs      []string          // --del key targets for update verb (when HasSetArg set on spec)
	Args         []string          // extra positional args beyond [id] (when HasArgs set on spec)
	IdParts      []string          // id split on "/" when id_parts > 1 on spec; length equals the number of actual parts
	Level        string            // scope level: "account", "org", or "project" (empty when flag not present)
	IsPty        bool              // true when stdout is an interactive terminal
	IsCompletion bool              // true when this ctx was built for a shell completion request
	Resolver     Resolver
	GlobalFlags  GlobalFlags
	FormatFlags  FormatFlags
	PagingFlags  PagingFlags
	// FlagValues holds typed flag values for this command, keyed by flag name. It contains:
	//   - all flags declared in the spec (cs.Flags), typed as string/bool/[]string
	//   - "page"         int    (0-indexed) when the spec declares builtin_flags.page
	//   - "format"       string when the flag exists (gates PTY-sensitive expr rendering)
	//   - "columns"      string when the flag exists (list commands)
	//   - "list-columns" bool   when the flag exists (list commands)
	//   - "list-fields"  bool   when the flag exists (get/update commands)
	//   - "profile", "org", "project" string when no_auth: true (the handler owns auth resolution)
	FlagValues map[string]any
}
