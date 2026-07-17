// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/harness/cli/pkg/client"
	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/endpoint"
	"github.com/harness/cli/pkg/exprenv"
	"github.com/harness/cli/pkg/extractutil"
	"github.com/harness/cli/pkg/format"
	"github.com/harness/cli/pkg/hlog"
	"github.com/harness/cli/pkg/spec"
)

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

	c := client.New(ctx)
	method := ep.Method
	if method == "" {
		method = "GET"
	}

	// Priority 1: file body — wins over strategies when -f is provided.
	if ep.FileBody != spec.FileBodyNone && cmdctx.GetString(ctx.FlagValues, "file") != "" {
		rawBody, err := cmdctx.SlurpInputFile(ctx.FlagValues)
		if err != nil {
			return nil, nil, err
		}

		// CD-style DTO envelope: the -f file may be given as the raw resource YAML
		// (wrapped or flat) rather than the API's {identifier, ..., yaml: "..."} shape.
		// Build the envelope here instead of going through the generic normalize/wrap path.
		if ep.FileBodyYamlEnvelope != "" {
			env, err := buildYamlEnvelope(ctx, ep.FileBodyYamlEnvelope, rawBody)
			if err != nil {
				return nil, nil, err
			}
			qp := evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams)
			ct := resolveContentType(ep, method)
			if err := runEndpointValidators(ctx, ep, cmdctx.EndpointRequest{
				Method:      method,
				Path:        path,
				QueryParams: qp,
				Body:        env,
				ContentType: ct,
			}); err != nil {
				return nil, nil, err
			}
			return c.DoRequest(client.Request{
				Method:          method,
				Path:            path,
				QueryParams:     qp,
				Body:            env,
				BodyContentType: ct,
			})
		}

		body, ct, err := cmdctx.NormalizeFileBody(rawBody, resolveFileBodyContentType(ep, method), cmdctx.GetString(ctx.FlagValues, "file"))
		if err != nil {
			return nil, nil, err
		}
		qp := evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams)
		if err := runEndpointValidators(ctx, ep, cmdctx.EndpointRequest{
			Method:      method,
			Path:        path,
			QueryParams: qp,
			Body:        body,
			ContentType: ct,
		}); err != nil {
			return nil, nil, err
		}
		if method == "PUT" || method == "PATCH" {
			if ep.FileBodyWrapAsString != "" {
				return c.DoRequest(client.Request{
					Method:          method,
					Path:            path,
					QueryParams:     qp,
					Body:            map[string]any{ep.FileBodyWrapAsString: body},
					BodyContentType: "application/json",
				})
			}
			if ep.UpdateBodyWrap != "" {
				var parsed any
				if err := json.Unmarshal([]byte(body), &parsed); err != nil {
					return nil, nil, fmt.Errorf("parsing -f body: %w", err)
				}
				return c.DoRequest(client.Request{
					Method:          method,
					Path:            path,
					QueryParams:     qp,
					Body:            map[string]any{ep.UpdateBodyWrap: unwrapIfAlreadyWrapped(parsed, ep.UpdateBodyWrap)},
					BodyContentType: ct,
				})
			}
			return c.DoRequest(client.Request{
				Method:          method,
				Path:            path,
				QueryParams:     qp,
				Body:            body,
				BodyContentType: ct,
			})
		}
		if ep.FileBodyWrapAsString != "" {
			return c.Post(path, qp, map[string]any{ep.FileBodyWrapAsString: body})
		}
		if ep.CreateBodyWrap != "" || len(ep.CreateBodyInit) > 0 {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(body), &parsed); err != nil {
				return nil, nil, fmt.Errorf("parsing -f body: %w", err)
			}
			inner := parsed
			if ep.CreateBodyWrap != "" {
				if wrapped, ok := parsed[ep.CreateBodyWrap].(map[string]any); ok {
					inner = wrapped
				}
			}
			if len(ep.CreateBodyInit) > 0 {
				exprEnv := exprenv.Make(ctx)
				for dotPath, exprStr := range ep.CreateBodyInit {
					if _, exists := inner[dotPath]; !exists {
						if result, ok := exprenv.EvalExprAny(exprEnv, exprStr); ok && result != nil {
							setDotPath(inner, dotPath, result)
						}
					}
				}
			}
			if ep.CreateBodyWrap != "" {
				return c.Post(path, qp, map[string]any{ep.CreateBodyWrap: inner})
			}
			return c.Post(path, qp, inner)
		}
		return c.PostRaw(path, qp, body, ct)
	}

	if ep.FileBody == spec.FileBodyRequired {
		return nil, nil, fmt.Errorf("-f/--file is required for this command")
	}

	// Priority 2: update/create strategies — each owns its own query params.
	if ep.UpdateStrategy == spec.UpdateStrategyGetThenPut && method == "PUT" {
		result, err := runGetThenUpdate(ctx, ep, c, path, method)
		return result, nil, err
	}
	if ep.UpdateStrategy == spec.UpdateStrategyGetThenPutKV && method == "PUT" {
		result, err := runGetThenPutKV(ctx, ep, c, path)
		return result, nil, err
	}
	if ep.UpdateStrategy == spec.UpdateStrategyGetThenPatch && method == "PATCH" {
		result, err := runGetThenUpdate(ctx, ep, c, path, method)
		return result, nil, err
	}
	if ep.CreateStrategy == spec.CreateStrategySetFields && method == "POST" {
		result, err := runSetFields(ctx, ep, c, path)
		return result, nil, err
	}

	// Priority 3: default body_params / body_fn path.
	qp := evalQueryParams(ctx, ep.QueryParams, true, extraQueryParams)
	if method == "GET" {
		if err := endpoint.ApplyQueryParamsFn(ctx, ep, qp); err != nil {
			return nil, nil, err
		}
	}
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
			return result, format.FormatSingleOutput(ctx.FormatFlags, ctx.IsPty, parsed, "", "", nil, nil, exprEnv)
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

	if ctx.VerbHandler == VerbGet && ctx.FormatFlags.Fields != "" {
		fieldIDs := splitFieldIDs(ctx.FormatFlags.Fields)
		fields := resolveFieldsForCommand(ctx, ep)
		return result, format.FormatFieldsOutput(ctx.FormatFlags, result, ep.ItemExpr, fields, fieldIDs, exprEnv)
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
	return result, format.FormatSingleOutput(ctx.FormatFlags, ctx.IsPty, result, ep.ItemExpr, ep.YamlPickExpr, ep.YamlExclude, textFmt, exprEnv)
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

// runEndpointValidators runs all validators_endpoint declared on ep, in order.
// Returns the first error encountered, or nil if all pass or none are declared.
func runEndpointValidators(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, req cmdctx.EndpointRequest) error {
	if len(ep.ValidatorsEndpoint) == 0 || ctx.Resolver == nil {
		return nil
	}
	for _, id := range ep.ValidatorsEndpoint {
		fn := ctx.Resolver.ResolveEndpointValidator(id)
		if fn == nil {
			return fmt.Errorf("validators_endpoint %q not registered", id)
		}
		if err := fn(ctx, req); err != nil {
			return err
		}
	}
	return nil
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
	if ep.BodyParams != nil {
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

// unwrapIfAlreadyWrapped guards against double-wrapping when the -f file is a full
// Harness Studio YAML export that already has the wrapper key at the top level
func unwrapIfAlreadyWrapped(parsed any, wrapperKey string) any {
	m, ok := parsed.(map[string]any)
	if !ok {
		return parsed
	}
	if wrapped, ok := m[wrapperKey].(map[string]any); ok {
		return wrapped
	}
	return parsed
}

// envelopeIdentityFields lists the keys lifted from the inner resource object to the
// top level of a CD-style DTO envelope (see buildYamlEnvelope).
var envelopeIdentityFields = []string{
	"identifier", "name", "orgIdentifier", "projectIdentifier",
	"description", "tags", "type", "environmentRef",
}

// buildYamlEnvelope wraps a raw -f YAML file into a Harness CD-style DTO envelope:
// {identifier, name, orgIdentifier, projectIdentifier, ..., yaml: "<full resource yaml>"}.
func buildYamlEnvelope(ctx *cmdctx.Ctx, wrapperKey, rawBody string) (map[string]any, error) {
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(rawBody), &parsed); err != nil {
		return nil, fmt.Errorf("parsing -f YAML: %w", err)
	}

	if _, ok := parsed["yaml"].(string); ok {
		seedEnvelopeScope(parsed, ctx)
		return parsed, nil
	}

	inner := parsed
	yamlStr := rawBody
	if wrapped, ok := parsed[wrapperKey].(map[string]any); ok {
		inner = wrapped
	} else {
		b, err := yaml.Marshal(map[string]any{wrapperKey: parsed})
		if err != nil {
			return nil, fmt.Errorf("re-wrapping -f YAML under %q: %w", wrapperKey, err)
		}
		yamlStr = string(b)
	}

	env := map[string]any{"yaml": yamlStr}
	for _, k := range envelopeIdentityFields {
		if v, ok := inner[k]; ok {
			env[k] = v
		}
	}
	seedEnvelopeScope(env, ctx)
	return env, nil
}

// seedEnvelopeScope defaults orgIdentifier/projectIdentifier on env from ctx.Auth
// when the -f file did not already specify them.
func seedEnvelopeScope(env map[string]any, ctx *cmdctx.Ctx) {
	if ctx.Auth == nil {
		return
	}
	if _, ok := env["orgIdentifier"]; !ok && ctx.Auth.OrgID != "" {
		env["orgIdentifier"] = ctx.Auth.OrgID
	}
	if _, ok := env["projectIdentifier"]; !ok && ctx.Auth.ProjectID != "" {
		env["projectIdentifier"] = ctx.Auth.ProjectID
	}
}

// resolveFileBodyContentType returns the format to validate/normalize the -f file
// against: ep.FileBodyContentType if set, otherwise resolveContentType(ep, method).
func resolveFileBodyContentType(ep *spec.EndpointSpec, method string) string {
	if ep.FileBodyContentType != "" {
		return ep.FileBodyContentType
	}
	return resolveContentType(ep, method)
}

// resolveContentType returns the Content-Type for a request: ep.ContentType if set,
// otherwise a method-based default (PATCH → merge-patch+json, POST/PUT → application/json).
func resolveContentType(ep *spec.EndpointSpec, method string) string {
	if ep.ContentType != "" {
		return ep.ContentType
	}
	switch method {
	case "PATCH":
		return "application/merge-patch+json"
	case "POST", "PUT":
		return "application/json"
	default:
		return ""
	}
}

// runGetThenUpdate implements the "get-then-put" and "get-then-patch" update strategies:
//  1. GET the resource using get_query_params (falls back to query_params)
//  2. Extract ep.UpdateBodyPick subtree from the response
//  3. Apply --set/--del mutations using noun field paths
//  4. Re-wrap under ep.UpdateBodyWrap key (if set)
//  5. PUT or PATCH the result (method determines verb and Content-Type)
func runGetThenUpdate(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, c *client.Client, path, method string) (any, error) {
	exprEnv := exprenv.Make(ctx)

	getPath := path
	if ep.GetPath != "" {
		var err error
		getPath, err = exprenv.ResolvePath(exprEnv, ep.GetPath)
		if err != nil {
			return nil, fmt.Errorf("get-then-%s: resolving get_path %q: %w", strings.ToLower(method), ep.GetPath, err)
		}
	}

	getQP := evalQueryParams(ctx, firstNonEmptyMap(ep.GetQueryParams, ep.QueryParams), true)
	getResult, _, err := c.Get(getPath, getQP)
	if err != nil {
		return nil, fmt.Errorf("get-then-%s: GET failed: %w", strings.ToLower(method), err)
	}

	// Extract the mutable subtree from the root GET response.
	// If update_body_pick is set, evaluate it against the root (same as yaml_pick_expr on the
	// corresponding get command). Otherwise fall back to item_expr for backwards compatibility.
	item := getResult
	if ep.UpdateBodyPick != "" {
		picked, ok := exprenv.EvalExprAny(exprenv.WithIt(exprEnv, getResult), ep.UpdateBodyPick)
		if !ok {
			return nil, fmt.Errorf("get-then-%s: update_body_pick %q did not resolve", strings.ToLower(method), ep.UpdateBodyPick)
		}
		item = picked
	} else if ep.ItemExpr != "" && ep.ItemExpr != "it" {
		if v, ok := exprenv.EvalExprAny(exprenv.WithIt(exprEnv, getResult), ep.ItemExpr); ok {
			item = v
		}
	}

	// Round-trip through JSON to get a map[string]any we can mutate.
	b, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("get-then-%s: marshaling picked item: %w", strings.ToLower(method), err)
	}
	var mutable map[string]any
	if err := json.Unmarshal(b, &mutable); err != nil {
		return nil, fmt.Errorf("get-then-%s: unmarshaling picked item: %w", strings.ToLower(method), err)
	}

	// Build a fieldID→FieldDef map from the noun's mutable fields for --set/--del resolution.
	fieldPaths := map[string]spec.FieldDef{}
	for _, f := range MutableFields(resolveNounDef(ctx)) {
		fieldPaths[f.ID] = f
	}

	if err := applyMutations(mutable, ctx.SetArgs, ctx.DelArgs, fieldPaths); err != nil {
		return nil, err
	}

	var updateBody any = mutable
	if ep.UpdateBodyWrap != "" {
		updateBody = map[string]any{ep.UpdateBodyWrap: mutable}
	}
	updateQP := evalQueryParams(ctx, ep.QueryParams, true)
	result, _, err := c.DoRequest(client.Request{
		Method:          method,
		Path:            path,
		QueryParams:     updateQP,
		Body:            updateBody,
		BodyContentType: resolveContentType(ep, method),
	})
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

	getPath := path
	if ep.GetPath != "" {
		var err error
		getPath, err = exprenv.ResolvePath(exprEnv, ep.GetPath)
		if err != nil {
			return nil, fmt.Errorf("get-then-put-kv: resolving get_path %q: %w", ep.GetPath, err)
		}
	}

	getQP := evalQueryParams(ctx, firstNonEmptyMap(ep.GetQueryParams, ep.QueryParams), true)
	getResult, _, err := c.Get(getPath, getQP)
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

	if err := applyMutations(mutable, ctx.SetArgs, ctx.DelArgs, fieldPaths); err != nil {
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
// applyMutations applies --set and --del operations to mutable in-place.
// fieldPaths maps field IDs to their FieldDef; fd.MutablePath is the dot-path
// within mutable (relative to the update_body_pick subtree, no "it." prefix).
func applyMutations(mutable map[string]any, setArgs map[string]string, delArgs []string, fieldPaths map[string]spec.FieldDef) error {
	// Build a map from user-facing field ID to its mutable_path within mutable.
	idToRel := map[string]string{}
	for id, fd := range fieldPaths {
		idToRel[id] = fd.MutablePath
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
