// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/exprenv"
	"github.com/harness/harness-cli/pkg/hlog"
	"github.com/harness/harness-cli/pkg/spec"
)

// ResolveFetchFn returns the FetchFn to use for ep. If ep.FetchFn is set it is
// looked up through ctx.Resolver; otherwise HTTPFetchFn is returned.
func ResolveFetchFn(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (cmdctx.FetchFn, error) {
	if ep.FetchFn == "" {
		return HTTPFetchFn, nil
	}
	if ctx.Resolver == nil {
		return nil, fmt.Errorf("fetch_fn %q declared but no resolver available", ep.FetchFn)
	}
	return ctx.Resolver.ResolveFetchFn(ep.FetchFn)
}

// BuildRequest resolves path and base query params from ctx and ep into a client.Request.
func BuildRequest(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (*client.Request, error) {
	a := ctx.Auth
	if a == nil {
		return nil, fmt.Errorf("BuildRequest requires auth")
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
	origAuth := ctx.Auth
	ctx.Auth = a
	defer func() { ctx.Auth = origAuth }()

	exprEnv := exprenv.Make(ctx)
	path, err := exprenv.ResolvePath(exprEnv, ep.Path)
	if err != nil {
		return nil, fmt.Errorf("resolving path %q: %w", ep.Path, err)
	}
	qp := map[string]string{
		"orgIdentifier":     a.OrgID,
		"projectIdentifier": a.ProjectID,
	}
	for paramName, exprStr := range ep.QueryParams {
		if result := exprenv.EvalExpr(exprEnv, exprStr); result != "" {
			qp[paramName] = result
		}
	}
	method := ep.Method
	if method == "" {
		method = http.MethodGet
	}
	req := &client.Request{
		Method:      method,
		Path:        path,
		QueryParams: qp,
	}
	if len(ep.BodyParams) > 0 {
		req.Body = buildBody(ep, exprEnv)
	} else if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		// gRPC-gateway rejects POST/PUT/PATCH with no body; send empty object.
		req.Body = map[string]any{}
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		req.BodyContentType = "application/json"
	}
	if len(ep.RequestHeaders) > 0 {
		req.Headers = evalRequestHeaders(ep, exprEnv)
	}
	return req, nil
}

// ExtractItems applies ep.ItemsExpr to raw and returns the resulting slice.
// Returns nil when the expression doesn't resolve.
func ExtractItems(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, raw any) []any {
	exprEnv := exprenv.WithIt(exprenv.Make(ctx), raw)
	items, _ := exprenv.EvalItemsExpr(exprEnv, ep.ItemsExpr)
	return items
}

// HTTPFetchFn is the default FetchFn for spec-declared endpoints. It composes
// BuildRequest → InjectPaging → DoRequest → ExtractItems → ExtractPaging.
func HTTPFetchFn(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, wantStart, wantCount int, cursor any) (*cmdctx.PageResult, error) {
	pg := ep.Paging
	if pg == nil {
		return nil, fmt.Errorf("HTTPFetchFn called on endpoint with no paging spec")
	}
	strategy, err := MakePagingStrategy(pg.PagingStrategy)
	if err != nil {
		return nil, err
	}
	req, err := BuildRequest(ctx, ep)
	if err != nil {
		return nil, err
	}
	pagingData := strategy.InjectPaging(req, pg, wantStart, wantCount)
	raw, headers, err := client.New(ctx.Context, ctx.Auth).DoRequest(*req)
	if err != nil {
		hlog.Debug("HTTPFetchFn error", "noun", ctx.Noun, "err", err)
		return nil, err
	}
	items := ExtractItems(ctx, ep, raw)
	hlog.Debug("HTTPFetchFn", "noun", ctx.Noun, "strategy", pg.PagingStrategy, "items", len(items))
	return strategy.ExtractPaging(ctx, ep, raw, items, headers, pagingData)
}

func evalRequestHeaders(ep *spec.EndpointSpec, exprEnv map[string]any) map[string]string {
	result := make(map[string]string, len(ep.RequestHeaders))
	for name, headerExpr := range ep.RequestHeaders {
		if v := exprenv.EvalExpr(exprEnv, headerExpr); v != "" {
			result[name] = v
		}
	}
	return result
}

func buildBody(ep *spec.EndpointSpec, exprEnv map[string]any) map[string]any {
	body := make(map[string]any)
	for dotPath, exprStr := range ep.BodyParams {
		if result, ok := exprenv.EvalExprAny(exprEnv, exprStr); ok && result != nil {
			setDotPath(body, dotPath, result)
		}
	}
	return body
}

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

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case int:
		return int64(n), nil
	case string:
		var i int64
		_, err := fmt.Sscanf(n, "%d", &i)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %q as int64", n)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}
