// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/endpoint"
	"github.com/harness/harness-cli/pkg/exprenv"
	"github.com/harness/harness-cli/pkg/extractutil"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/hlog"
	"github.com/harness/harness-cli/pkg/spec"
)

const maxItemsAll = 100_000_000

// CallEndpoint executes an API call described by ep using auth and flags from ctx.
// It returns the raw decoded response. Workflows use this to fetch data and manipulate
// it before deciding how to render output.
func CallEndpoint(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (any, error) {
	result, _, err := callEndpointFull(ctx, ep, nil)
	return result, err
}

// callEndpointFull is the internal implementation of CallEndpoint that also returns
// the raw HTTP response headers. Used by fetchPage for page_header paging.
func callEndpointFull(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, extraQueryParams map[string]string) (any, http.Header, error) {
	a := ctx.Auth
	if a == nil {
		return nil, nil, fmt.Errorf("CallEndpoint requires auth; command verb does not resolve credentials")
	}

	switch ctx.Level {
	case "org":
		copy := *a
		copy.ProjectID = ""
		a = &copy
	case "account":
		copy := *a
		copy.OrgID = ""
		copy.ProjectID = ""
		a = &copy
	}

	hlog.Info("auth resolved",
		"source", a.Source,
		"account", a.AccountID,
		"org", a.OrgID,
		"project", a.ProjectID,
		"api_url", a.APIUrl,
	)

	origAuth := ctx.Auth
	ctx.Auth = a
	defer func() { ctx.Auth = origAuth }()

	exprEnv := exprenv.Make(ctx)
	path, err := exprenv.ResolvePath(exprEnv, ep.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving path %q: %w", ep.Path, err)
	}

	c := client.New(ctx.Context, a)
	method := ep.Method
	if method == "" {
		method = "GET"
	}

	// Priority 1: file body — wins over strategies when -f is provided.
	if ep.FileBody != spec.FileBodyNone && cmdctx.GetString(ctx.FlagValues, "file") != "" {
		body, err := cmdctx.SlurpInputFile(ctx.FlagValues)
		if err != nil {
			return nil, nil, err
		}
		ct := ep.ContentType
		if method == "PUT" {
			if ct == "" {
				ct = "application/json"
			}
			if ep.UpdateBodyWrap != "" {
				var parsed any
				if err := json.Unmarshal([]byte(body), &parsed); err != nil {
					return nil, nil, fmt.Errorf("parsing -f body: %w", err)
				}
				return c.Put(path, evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams), map[string]any{ep.UpdateBodyWrap: parsed})
			}
			return c.PutRaw(path, evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams), body, ct)
		}
		if ct == "" {
			ct = "application/json"
		}
		return c.PostRaw(path, evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams), body, ct)
	}

	if ep.FileBody == spec.FileBodyRequired {
		return nil, nil, fmt.Errorf("-f/--file is required for this command")
	}

	// Priority 2: update/create strategies — each owns its own query params.
	if ep.UpdateStrategy == spec.UpdateStrategyGetThenPut && method == "PUT" {
		result, err := runGetThenPut(ctx, ep, c, path)
		return result, nil, err
	}
	if ep.UpdateStrategy == spec.UpdateStrategyGetThenPutKV && method == "PUT" {
		result, err := runGetThenPutKV(ctx, ep, c, path)
		return result, nil, err
	}
	if ep.CreateStrategy == spec.CreateStrategySetFields && method == "POST" {
		result, err := runSetFields(ctx, ep, c, path)
		return result, nil, err
	}

	// Priority 3: default body_params / body_fn path.
	qp := evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams)
	extraHeaders := evalRequestHeaders(ep, exprEnv)
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		body, err := resolveBody(ep, ctx)
		if err != nil {
			return nil, nil, err
		}
		if rb, ok := body.(*cmdctx.RawBody); ok {
			hlog.Debug("raw body", "content_type", rb.ContentType, "size", len(rb.Content))
			if len(extraHeaders) > 0 {
				return c.DoRequest(client.Request{Method: method, Path: path, QueryParams: qp, Body: rb.Content, BodyContentType: rb.ContentType, Headers: extraHeaders})
			}
			return c.PostRaw(path, qp, rb.Content, rb.ContentType)
		}
		if len(extraHeaders) > 0 {
			ct := ""
			if method == "POST" || method == "PUT" || method == "PATCH" {
				ct = "application/json"
				if body == nil {
					body = map[string]any{}
				}
			}
			return c.DoRequest(client.Request{Method: method, Path: path, QueryParams: qp, Body: body, BodyContentType: ct, Headers: extraHeaders})
		}
		switch method {
		case "POST":
			return c.Post(path, qp, body)
		case "DELETE":
			if body != nil {
				return c.DeleteWithBody(path, qp, body)
			}
			return c.Delete(path, qp)
		case "PUT":
			return c.Put(path, qp, body)
		default:
			return c.Post(path, qp, body)
		}
	default:
		if len(extraHeaders) > 0 {
			return c.DoRequest(client.Request{Method: "GET", Path: path, QueryParams: qp, Headers: extraHeaders})
		}
		return c.Get(path, qp)
	}
}

// evalRequestHeaders evaluates ep.RequestHeaders expressions and returns the resolved header map.
func evalRequestHeaders(ep *spec.EndpointSpec, exprEnv map[string]any) map[string]string {
	if len(ep.RequestHeaders) == 0 {
		return nil
	}
	result := make(map[string]string, len(ep.RequestHeaders))
	for name, headerExpr := range ep.RequestHeaders {
		if v := exprenv.EvalExpr(exprEnv, headerExpr); v != "" {
			result[name] = v
		}
	}
	return result
}

// RunEndpoint calls CallEndpoint then renders the result according to ctx.FormatFlags
// and the endpoint's output spec. Workflows call this as the final output step.
func RunEndpoint(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (any, error) {
	if ctx.VerbHandler == VerbList {
		return nil, fmt.Errorf("RunEndpoint called with list verb_handler — use RunListEndpoint instead")
	}

	// Handle --list-fields: print field table and exit without hitting the API.
	if cmdctx.GetBool(ctx.FlagValues, "list-fields") {
		fields := resolveFieldsForCommand(ctx, ep)
		w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
		if err != nil {
			return nil, err
		}
		defer closeW()
		if ctx.VerbHandler == VerbUpdate || ep.CreateStrategy == spec.CreateStrategySetFields {
			return nil, PrintMutableFieldTable(w, MutableFields(resolveNounDef(ctx)))
		}
		return nil, PrintFieldTable(w, fields)
	}

	result, err := CallEndpoint(ctx, ep)
	if err != nil {
		return nil, err
	}

	exprEnv := exprenv.Make(ctx)

	if result == nil {
		var textFmt cmdctx.TextFormatterFn
		if ep.TextFormatter != "" && ctx.Resolver != nil {
			textFmt = ctx.Resolver.ResolveTextFormatter(ep.TextFormatter)
		}
		if textFmt == nil && (ep.TextHeader != "" || ep.TextFooter != "") {
			textFmt = buildDeclTextFmt(nil, ep, exprEnv)
		}
		if textFmt == nil {
			return nil, nil
		}
		w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
		if err != nil {
			return nil, err
		}
		defer closeW()
		return nil, textFmt(w, extractutil.MakeDataAccessor(exprEnv, nil))
	}

	if ep.FieldExtract != "" && !ctx.FormatFlags.Raw {
		m, ok := result.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("response is not a JSON object; cannot extract field %q", ep.FieldExtract)
		}
		val, ok := m[ep.FieldExtract]
		if !ok {
			return nil, fmt.Errorf("field %q not found in response", ep.FieldExtract)
		}
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("field %q is not a string", ep.FieldExtract)
		}
		if ctx.FormatFlags.Format == "json" {
			var parsed any
			if err := yaml.Unmarshal([]byte(s), &parsed); err != nil {
				return nil, fmt.Errorf("parsing %q as YAML: %w", ep.FieldExtract, err)
			}
			return result, format.FormatSingleOutput(ctx.FormatFlags, ctx.IsPty, parsed, "it", nil, exprEnv)
		}
		w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
		if err != nil {
			return nil, err
		}
		defer closeW()
		fmt.Fprint(w, s)
		return result, nil
	}

	if ctx.VerbHandler == VerbDelete {
		if ep.TextHeader != "" || ep.TextFooter != "" {
			textFmt := buildDeclTextFmt(nil, ep, exprEnv)
			w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
			if err != nil {
				return nil, err
			}
			defer closeW()
			return nil, textFmt(w, extractutil.MakeDataAccessor(exprEnv, nil))
		}
		return nil, nil
	}

	var textFmt cmdctx.TextFormatterFn
	if ep.TextFormatter != "" && ctx.Resolver != nil {
		textFmt = ctx.Resolver.ResolveTextFormatter(ep.TextFormatter)
	}
	if textFmt == nil {
		fields := resolveFieldsForCommand(ctx, ep)
		if len(fields) > 0 || ep.TextHeader != "" || ep.TextFooter != "" {
			textFmt = buildDeclTextFmt(fields, ep, exprEnv)
		}
	}
	return result, format.FormatSingleOutput(ctx.FormatFlags, ctx.IsPty, result, ep.ItemExpr, textFmt, exprEnv)
}

// RunListEndpoint calls CallEndpoint then renders the result as a list.
// It is the dedicated entry point for list verbs and mirrors RunEndpoint's logic.
func RunListEndpoint(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) error {
	if ep.Paging != nil {
		if ctx.PagingFlags.Count {
			n, err := endpoint.FetchCount(ctx, ep)
			if err != nil {
				return err
			}
			return renderCount(ctx, n)
		}
		items, meta, err := endpoint.FetchItems(ctx, ep, ctx.PagingFlags)
		if err != nil {
			return err
		}
		return renderList(ctx, ep, items, meta)
	}

	result, err := CallEndpoint(ctx, ep)
	if err != nil {
		return err
	}

	exprEnv := exprenv.Make(ctx)
	items, _ := exprenv.EvalItemsExpr(exprenv.WithIt(exprEnv, result), ep.ItemsExpr)
	return renderList(ctx, ep, items, nil)
}

func buildDeclTextFmt(fields []spec.FieldDef, ep *spec.EndpointSpec, exprEnv map[string]any) cmdctx.TextFormatterFn {
	interpolate := func(tmpl string, item any) string {
		env := make(map[string]any, len(exprEnv)+1)
		maps.Copy(env, exprEnv)
		env["it"] = item
		s, _ := exprenv.ResolvePath(env, tmpl)
		return s
	}
	return format.BuildTextFieldFormatter(fields, ep.TextHeader, ep.TextFooter, interpolate)
}

func firstNonEmptyMap(maps ...map[string]string) map[string]string {
	for _, m := range maps {
		if len(m) > 0 {
			return m
		}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// FetchItems normalizes user flags into an offset/limit pair and delegates to FetchRange.
func FetchItems(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, pf cmdctx.PagingFlags) ([]any, *format.PageMeta, error) {
	if ep.Paging == nil {
		return nil, nil, fmt.Errorf("FetchItems called on endpoint with no paging spec")
	}
	if pf.Offset < 0 || pf.Limit < 0 {
		return nil, nil, fmt.Errorf("offset and limit must be non-negative")
	}

	if ep.Paging.PagingStrategy == spec.PagingStrategyFlatList {
		return fetchFlatList(ctx, ep, pf.Offset, pf.Limit)
	}

	offset := pf.Offset
	limit := pf.Limit

	switch {
	case pf.All:
		offset = 0
		limit = maxItemsAll
	case limit == 0:
		limit = ep.Paging.PageSizeDefault
	}

	hlog.Debug("FetchItems", "noun", ctx.Noun, "all", pf.All, "offset", offset, "limit", limit)
	return FetchRange(ctx, ep, offset, limit)
}

// fetchFlatList fetches all items in a single call and applies offset/limit client-side.
func fetchFlatList(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, offset, limit int) ([]any, *format.PageMeta, error) {
	result, _, err := callEndpointFull(ctx, ep, nil)
	if err != nil {
		return nil, nil, err
	}
	exprEnv := exprenv.WithIt(exprenv.Make(ctx), result)
	items, err := exprenv.EvalItemsExpr(exprEnv, ep.ItemsExpr)
	if err != nil {
		items = nil
	}
	hlog.Debug("fetchFlatList", "noun", ctx.Noun, "total", len(items), "offset", offset, "limit", limit)

	total := len(items)
	if offset > total {
		offset = total
	}
	items = items[offset:]
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	meta := &format.PageMeta{
		Offset:   offset,
		Count:    len(items),
		HasTotal: true,
		Total:    int64(total),
	}
	return items, meta, nil
}

// RunListWithPaging fetches one or more pages according to ctx.PagingFlags and renders
// the accumulated items through the standard list formatting pipeline.
func RunListWithPaging(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) error {
	items, meta, err := FetchItems(ctx, ep, ctx.PagingFlags)
	if err != nil {
		return err
	}
	return renderList(ctx, ep, items, meta)
}

// fetchCompletionItems returns all items for a completion call.
// For paged endpoints it fetches all pages; for flat endpoints it calls once and extracts items.
func fetchCompletionItems(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) ([]any, error) {
	if ep.Paging != nil {
		items, _, err := endpoint.FetchItems(ctx, ep, cmdctx.PagingFlags{})
		return items, err
	}
	result, err := CallEndpoint(ctx, ep)
	if err != nil {
		return nil, err
	}
	exprEnv := exprenv.WithIt(exprenv.Make(ctx), result)
	return exprenv.EvalItemsExpr(exprEnv, ep.ItemsExpr)
}

func renderCount(ctx *cmdctx.Ctx, n int64) error {
	w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
	if err != nil {
		return err
	}
	defer closeW()
	fmt.Fprintln(w, n)
	return nil
}

// renderList applies item_item_expr unwrapping and calls FormatArrayOutput.
// items must already be extracted (post-ItemsExpr).
func renderList(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, items []any, meta *format.PageMeta) error {
	exprEnv := exprenv.Make(ctx)
	fields := resolveFieldsForCommand(ctx, ep)
	tspec := buildTspec(ep.Columns, fields)

	listResult := any(items)
	listItemsExpr := "it"
	if ep.ItemItemExpr != "" {
		unwrapped := make([]any, 0, len(items))
		for _, item := range items {
			if v, ok := exprenv.EvalExprAny(exprenv.WithIt(exprEnv, item), ep.ItemItemExpr); ok {
				unwrapped = append(unwrapped, v)
			} else {
				unwrapped = append(unwrapped, item)
			}
		}
		listResult = unwrapped
	}
	return format.FormatArrayOutput(ctx.FormatFlags, ctx.IsPty, listResult, listItemsExpr, tspec, fields, exprEnv, meta)
}

// RunListWithCount fetches the first page and returns the total item count from the
// response. ep.Paging must be non-nil and countable must be true.
func RunListWithCount(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (int64, error) {
	if ep.Paging.PagingStrategy == spec.PagingStrategyFlatList {
		items, _, err := fetchFlatList(ctx, ep, 0, 0)
		if err != nil {
			return 0, err
		}
		return int64(len(items)), nil
	}
	if ep.Paging.PagingStrategy == spec.PagingStrategyPageIndex && ep.Paging.TotalExpr == "" {
		return 0, fmt.Errorf("--count requires total_expr in paging spec for %s %s", ctx.Verb, ctx.Noun)
	}
	hlog.Debug("fetching count", "noun", ctx.Noun)
	pd, err := fetchPage(ctx, ep, 0, 1)
	if err != nil {
		return 0, err
	}
	if !pd.hasTotal {
		return 0, fmt.Errorf("total count not available for %s %s", ctx.Verb, ctx.Noun)
	}
	hlog.Debug("count resolved", "noun", ctx.Noun, "count", pd.total)
	return pd.total, nil
}

// pageData holds the result of a single API page fetch.
type pageData struct {
	items    []any
	last     bool
	hasTotal bool
	total    int64
}

// fetchPage calls the endpoint with the given pageIndex and pageSize injected as
// query params, then extracts items, the done signal, and the total count.
// It never slices or accumulates — that's the caller's job.
func fetchPage(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, pageIndex, pageSize int) (*pageData, error) {
	pg := ep.Paging
	if pg == nil {
		return nil, fmt.Errorf("fetchPage called on endpoint with no paging spec")
	}

	paramName := pg.PageIndexParam
	if paramName == "" {
		paramName = "pageIndex"
	}
	extra := map[string]string{paramName: strconv.Itoa(pageIndex)}
	if pg.PageSizeParam != "" && pageSize > 0 {
		extra[pg.PageSizeParam] = strconv.Itoa(pageSize)
	}

	result, headers, err := callEndpointFull(ctx, ep, extra)
	if err != nil {
		hlog.Debug("fetchPage error", "noun", ctx.Noun, "path", ep.Path, "page_index", pageIndex, "err", err)
		return nil, err
	}

	exprEnv := exprenv.WithIt(exprenv.Make(ctx), result)

	items, err := exprenv.EvalItemsExpr(exprEnv, ep.ItemsExpr)
	if err != nil {
		hlog.Debug("items_expr did not resolve", "noun", ctx.Noun, "expr", ep.ItemsExpr, "err", err)
		items = nil
	}

	hlog.Debug("fetchPage", "noun", ctx.Noun, "path", ep.Path, "page_index", pageIndex, "page_size", pageSize, "items", len(items))
	pd := &pageData{items: items}

	// Populate hasTotal/total from the model-specific source.
	switch pg.PagingStrategy {
	case spec.PagingStrategyPageHeader:
		totalHeader := pg.TotalHeader
		if totalHeader == "" {
			totalHeader = "X-Total-Elements"
		}
		if v, err := strconv.ParseInt(headers.Get(totalHeader), 10, 64); err == nil {
			pd.hasTotal, pd.total = true, v
		} else if pg.IsCountable() {
			return nil, fmt.Errorf("page_header: missing or invalid %s header", totalHeader)
		}
	case spec.PagingStrategyPageIndex:
		if pg.TotalExpr != "" {
			v, ok := exprenv.EvalExprAny(exprEnv, pg.TotalExpr)
			if !ok {
				return nil, fmt.Errorf("page_index: total_expr %q did not resolve", pg.TotalExpr)
			}
			n, err := toInt64(v)
			if err != nil {
				return nil, fmt.Errorf("page_index: total_expr %q resolved to unexpected type %T", pg.TotalExpr, v)
			}
			pd.hasTotal, pd.total = true, n
		}
	}

	// Determine last page: any of these conditions is sufficient.
	pd.last = len(items) == 0 ||
		(pageSize > 0 && len(items) < pageSize) ||
		(pd.hasTotal && int64((pageIndex+1)*pageSize) >= pd.total)

	return pd, nil
}

// FetchRange fetches items [offset, offset+limit) from a paged endpoint.
// It starts on the page that contains offset, so no wasted API calls for large offsets.
// Fast path: if the entire window fits in one page from index 0, asks for exactly
// wantEnd items instead of PageSizeMax.
func FetchRange(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, offset, limit int) ([]any, *format.PageMeta, error) {
	pg := ep.Paging
	if pg == nil {
		return nil, nil, fmt.Errorf("FetchRange called on endpoint with no paging spec")
	}

	wantStart := offset
	wantEnd := offset + limit
	if wantEnd == 0 {
		return nil, nil, nil
	}

	var size, startPage int
	if wantEnd <= pg.PageSizeMax {
		size = wantEnd
		startPage = 0
	} else {
		size = pg.PageSizeMax
		startPage = offset / size
	}
	pos := startPage * size

	var out []any
	meta := &format.PageMeta{Offset: offset}
	for page := startPage; pos < wantEnd; page++ {
		p, err := fetchPage(ctx, ep, page, size)
		if err != nil {
			return nil, nil, err
		}

		if p.hasTotal && !meta.HasTotal {
			meta.HasTotal = true
			meta.Total = p.total
		}

		pageStart := pos
		pageEnd := pos + len(p.items)
		pos = pageEnd

		lo := max(pageStart, wantStart) - pageStart
		hi := min(pageEnd, wantEnd) - pageStart
		if lo < hi {
			out = append(out, p.items[lo:hi]...)
		}

		if p.last {
			break
		}
	}
	meta.Count = len(out)
	return out, meta, nil
}

// resolveBody builds the request body, preferring body_fn, then body_params+body, returning nil if none apply.
func resolveBody(ep *spec.EndpointSpec, ctx *cmdctx.Ctx) (any, error) {
	if ep.BodyFn != "" {
		if ctx.Resolver == nil {
			return nil, fmt.Errorf("body_fn %q declared but no resolver available", ep.BodyFn)
		}
		bf := ctx.Resolver.ResolveBodyFn(ep.BodyFn)
		if bf == nil {
			return nil, fmt.Errorf("body_fn %q not registered", ep.BodyFn)
		}
		hlog.Debug("resolved body_fn", "fn", ep.BodyFn)
		return bf(ctx)
	}
	if len(ep.BodyParams) > 0 {
		return evalBodyParams(ctx, ep.BodyParams), nil
	}
	return nil, nil
}

// evalQueryParams builds a query param map by evaluating each expr string against an
// exprEnv derived from ctx. If withScope is true, orgIdentifier/projectIdentifier are
// seeded from ctx.Auth first.
func evalQueryParams(ctx *cmdctx.Ctx, exprs map[string]string, withScope bool, extra ...map[string]string) map[string]string {
	exprEnv := exprenv.Make(ctx)
	params := map[string]string{}
	if withScope && ctx.Auth != nil {
		params["orgIdentifier"] = ctx.Auth.OrgID
		params["projectIdentifier"] = ctx.Auth.ProjectID
	}
	for paramName, exprStr := range exprs {
		if result := exprenv.EvalExpr(exprEnv, exprStr); result != "" {
			params[paramName] = result
		}
	}
	for _, e := range extra {
		maps.Copy(params, e)
	}
	return params
}

// evalBodyParams evaluates a dot-path→expr map against ctx, returning a nested map[string]any.
// Dot-path keys (e.g. "config.type") create nested objects via setDotPath.
func evalBodyParams(ctx *cmdctx.Ctx, exprs map[string]string) map[string]any {
	exprEnv := exprenv.Make(ctx)
	body := make(map[string]any)
	for dotPath, exprStr := range exprs {
		if result, ok := exprenv.EvalExprAny(exprEnv, exprStr); ok && result != nil {
			setDotPath(body, dotPath, result)
		}
	}
	return body
}

// parseArrayFlag parses a string flag value as []string.
// If the trimmed value starts with '[' it is parsed as a JSON array;
// otherwise it is split on commas.
func parseArrayFlag(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		var result []string
		if err := json.Unmarshal([]byte(s), &result); err == nil {
			return result
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

// setDotPath sets a value at a dot-separated path in m, creating intermediate
// map[string]any nodes as needed. e.g. setDotPath(m, "config.type", "VIRTUAL")
// sets m["config"]["type"] = "VIRTUAL".
func setDotPath(m map[string]any, path string, val any) {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		m[parts[0]] = val
		return
	}
	child, ok := m[parts[0]].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[parts[0]] = child
	}
	setDotPath(child, parts[1], val)
}

// runGetThenPut implements the "get-then-put" update strategy:
//  1. GET the resource using get_query_params (falls back to query_params)
//  2. Extract ep.UpdateBodyPick subtree from the response
//  3. Apply --set/--del mutations using noun field paths
//  4. Re-wrap under ep.UpdateBodyWrap key
//  5. PUT the result
func runGetThenPut(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, c *client.Client, path string) (any, error) {
	exprEnv := exprenv.Make(ctx)

	getQP := evalQueryParams(ctx, firstNonEmptyMap(ep.GetQueryParams, ep.QueryParams), false)
	getResult, _, err := c.Get(path, getQP)
	if err != nil {
		return nil, fmt.Errorf("get-then-put: GET failed: %w", err)
	}

	// Unwrap the GET response the same way item_expr does for get commands.
	unwrapped := getResult
	if ep.ItemExpr != "" && ep.ItemExpr != "it" {
		if v, ok := exprenv.EvalExprAny(exprenv.WithIt(exprEnv, getResult), ep.ItemExpr); ok {
			unwrapped = v
		}
	}

	// Extract the mutable subtree using update_body_pick expr, relative to the unwrapped item.
	item := unwrapped
	if ep.UpdateBodyPick != "" {
		picked, ok := exprenv.EvalExprAny(exprenv.WithIt(exprEnv, unwrapped), ep.UpdateBodyPick)
		if !ok {
			return nil, fmt.Errorf("get-then-put: update_body_pick %q did not resolve", ep.UpdateBodyPick)
		}
		item = picked
	}

	// Round-trip through JSON to get a map[string]any we can mutate.
	b, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("get-then-put: marshaling picked item: %w", err)
	}
	var mutable map[string]any
	if err := json.Unmarshal(b, &mutable); err != nil {
		return nil, fmt.Errorf("get-then-put: unmarshaling picked item: %w", err)
	}

	// Build a fieldID→FieldDef map from the noun's mutable fields for --set/--del resolution.
	fieldPaths := map[string]spec.FieldDef{}
	for _, f := range MutableFields(resolveNounDef(ctx)) {
		fieldPaths[f.ID] = f
	}

	if err := applyMutations(mutable, ctx.SetArgs, ctx.DelArgs, fieldPaths, ep.UpdateBodyPick); err != nil {
		return nil, err
	}

	// Re-wrap and PUT.
	var putBody any = mutable
	if ep.UpdateBodyWrap != "" {
		putBody = map[string]any{ep.UpdateBodyWrap: mutable}
	}
	putQP := evalQueryParams(ctx, ep.QueryParams, true)
	result, _, err := c.Put(path, putQP, putBody)
	return result, err
}

// runGetThenPutKV implements the "get-then-put-kv" update strategy for APIs
// that store metadata as [{key, value}] arrays (e.g. HAR v2 metadata endpoint):
//  1. GET current kv pairs using get_query_params (falls back to query_params)
//  2. Unwrap via item_expr → []any of {key, value} objects
//  3. Apply --set key=value (upsert) and --del key (remove by key name)
//  4. Merge body_params with {<update_body_wrap>: [{key, value}, ...]} and PUT
func runGetThenPutKV(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, c *client.Client, path string) (any, error) {
	exprEnv := exprenv.Make(ctx)

	getQP := evalQueryParams(ctx, firstNonEmptyMap(ep.GetQueryParams, ep.QueryParams), false)
	getResult, _, err := c.Get(path, getQP)
	if err != nil {
		return nil, fmt.Errorf("get-then-put-kv: GET failed: %w", err)
	}

	// Unwrap via item_expr to get the []any of {key, value} objects.
	unwrapped := getResult
	if ep.ItemExpr != "" && ep.ItemExpr != "it" {
		if v, ok := exprenv.EvalExprAny(exprenv.WithIt(exprEnv, getResult), ep.ItemExpr); ok {
			unwrapped = v
		}
	}

	// Convert []any of {key, value} objects into a map for easy mutation.
	kvMap := map[string]string{}
	if items, ok := unwrapped.([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				k, _ := m["key"].(string)
				v, _ := m["value"].(string)
				if k != "" {
					kvMap[k] = v
				}
			}
		}
	}

	maps.Copy(kvMap, ctx.SetArgs)
	for _, k := range ctx.DelArgs {
		delete(kvMap, k)
	}

	// Rebuild as [{key, value}, ...].
	pairs := make([]any, 0, len(kvMap))
	for k, v := range kvMap {
		pairs = append(pairs, map[string]any{"key": k, "value": v})
	}

	// Start with any body_params (e.g. registryIdentifier, package), then add the kv array.
	putBody := evalBodyParams(ctx, ep.BodyParams)
	if putBody == nil {
		putBody = map[string]any{}
	}
	if ep.UpdateBodyWrap != "" {
		putBody[ep.UpdateBodyWrap] = pairs
	} else {
		putBody["metadata"] = pairs
	}
	putQP := evalQueryParams(ctx, ep.QueryParams, true)
	result, _, err := c.Put(path, putQP, putBody)
	return result, err
}

// runSetFields implements the "set-fields" create strategy:
//  1. Seed an empty object from create_body_init (dot-path → expr map)
//  2. Apply --set mutations using noun field paths
//  3. Re-wrap under create_body_wrap key (if set)
//  4. POST the result
func runSetFields(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, c *client.Client, path string) (any, error) {
	exprEnv := exprenv.Make(ctx)
	mutable := map[string]any{}

	// Seed initial values from create_body_init (evaluated as exprs).
	for dotPath, exprStr := range ep.CreateBodyInit {
		if result, ok := exprenv.EvalExprAny(exprEnv, exprStr); ok {
			setDotPath(mutable, dotPath, result)
		}
	}

	// Build fieldID→FieldDef map from the noun's mutable fields.
	fieldPaths := map[string]spec.FieldDef{}
	for _, f := range MutableFields(resolveNounDef(ctx)) {
		fieldPaths[f.ID] = f
	}

	// create_body_wrap acts as the pick prefix for relative path resolution.
	pickPrefix := ""
	if ep.CreateBodyWrap != "" {
		pickPrefix = "it." + ep.CreateBodyWrap
	}

	if err := applyMutations(mutable, ctx.SetArgs, ctx.DelArgs, fieldPaths, pickPrefix); err != nil {
		return nil, err
	}

	var postBody any = mutable
	if ep.CreateBodyWrap != "" {
		postBody = map[string]any{ep.CreateBodyWrap: mutable}
	}
	postQP := evalQueryParams(ctx, ep.QueryParams, true)
	result, _, err := c.Post(path, postQP, postBody)
	return result, err
}

// applyMutations applies --set and --del operations to mutable in-place.
// fieldPaths maps field IDs to their FieldDef (with Path). pickPrefix is the
// update_body_pick prefix (e.g. "it.project") used to strip the leading path
// so that field paths like "it.project.name" become just "name" within mutable.
func applyMutations(mutable map[string]any, setArgs map[string]string, delArgs []string, fieldPaths map[string]spec.FieldDef, pickPrefix string) error {
	// Strip "it." and the pick object prefix from a field path to get a relative key.
	// e.g. pickPrefix="it.project", field.Path="it.project.name" → "name"
	//      pickPrefix="it.project", field.Path="it.project.tags" → "tags"
	relPath := func(fieldPath string) string {
		p := strings.TrimPrefix(fieldPath, "it.")
		if pickPrefix != "" {
			prefix := strings.TrimPrefix(pickPrefix, "it.")
			p = strings.TrimPrefix(p, prefix+".")
		}
		return p
	}

	// Build a map from user-facing field ID to relative dot-path within mutable.
	idToRel := map[string]string{}
	for id, fd := range fieldPaths {
		idToRel[id] = relPath(fd.Path)
	}

	for key, val := range setArgs {
		// key may be a field ID (e.g. "name"), a field.subkey (e.g. "tags.env"),
		// or a set member (e.g. "modules.CD" for field_type=set).
		parts := strings.SplitN(key, ".", 2)
		fieldID := parts[0]

		fd, known := fieldPaths[fieldID]
		if !known {
			return fmt.Errorf("unknown or read-only field %q; use --list-fields to see mutable fields", fieldID)
		}
		rel := idToRel[fieldID]

		switch fd.FieldType {
		case "tags":
			if len(parts) < 2 {
				return fmt.Errorf("--set %s: tag fields require a key (e.g. --%s tags.key=value)", key, "set")
			}
			tagKey := parts[1]
			// Get or create the tags map.
			tagsMap := getDotPathMap(mutable, rel)
			if tagsMap == nil {
				tagsMap = map[string]any{}
			}
			tagsMap[tagKey] = val
			setDotPath(mutable, rel, tagsMap)
		case "set":
			if len(parts) < 2 {
				return fmt.Errorf("--set %s: set fields require a member (e.g. --set modules.CD)", key)
			}
			member := parts[1]
			arr := getDotPathSlice(mutable, rel)
			if !sliceContains(arr, member) {
				arr = append(arr, member)
			}
			setDotPath(mutable, rel, arr)
		default: // scalar
			setDotPath(mutable, rel, val)
		}
	}

	for _, key := range delArgs {
		parts := strings.SplitN(key, ".", 2)
		fieldID := parts[0]

		fd, known := fieldPaths[fieldID]
		if !known {
			return fmt.Errorf("unknown or read-only field %q; use --list-fields to see mutable fields", fieldID)
		}
		rel := idToRel[fieldID]

		switch fd.FieldType {
		case "tags":
			if len(parts) < 2 {
				return fmt.Errorf("--del %s: tag fields require a key (e.g. --del tags.key)", key)
			}
			tagKey := parts[1]
			tagsMap := getDotPathMap(mutable, rel)
			if tagsMap != nil {
				delete(tagsMap, tagKey)
				setDotPath(mutable, rel, tagsMap)
			}
		case "set":
			if len(parts) < 2 {
				return fmt.Errorf("--del %s: set fields require a member (e.g. --del modules.CD)", key)
			}
			member := parts[1]
			arr := getDotPathSlice(mutable, rel)
			setDotPath(mutable, rel, sliceRemove(arr, member))
		default: // scalar
			setDotPath(mutable, rel, nil)
		}
	}
	return nil
}

// getDotPathMap retrieves a map[string]any at a dot-separated path, or nil.
func getDotPathMap(m map[string]any, path string) map[string]any {
	parts := strings.SplitN(path, ".", 2)
	v, ok := m[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		mv, _ := v.(map[string]any)
		return mv
	}
	child, _ := v.(map[string]any)
	if child == nil {
		return nil
	}
	return getDotPathMap(child, parts[1])
}

// getDotPathSlice retrieves a []any at a dot-separated path, or nil.
func getDotPathSlice(m map[string]any, path string) []any {
	parts := strings.SplitN(path, ".", 2)
	v, ok := m[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		switch sv := v.(type) {
		case []any:
			return sv
		case []string:
			out := make([]any, len(sv))
			for i, s := range sv {
				out[i] = s
			}
			return out
		}
		return nil
	}
	child, _ := v.(map[string]any)
	if child == nil {
		return nil
	}
	return getDotPathSlice(child, parts[1])
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}

func sliceContains(s []any, v string) bool {
	for _, el := range s {
		if fmt.Sprint(el) == v {
			return true
		}
	}
	return false
}

func sliceRemove(s []any, v string) []any {
	out := make([]any, 0, len(s))
	for _, el := range s {
		if fmt.Sprint(el) != v {
			out = append(out, el)
		}
	}
	return out
}
