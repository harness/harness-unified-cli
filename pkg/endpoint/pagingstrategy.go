// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/exprenv"
	"github.com/harness/harness-cli/pkg/spec"
)

// PagingStrategy encapsulates the inject/extract logic for one paging style.
// InjectPaging mutates req with the appropriate query params and returns opaque
// state that ExtractPaging will need to interpret the response.
// ExtractPaging builds a PageResult from the raw response, response headers, and
// the opaque state produced by InjectPaging.
type PagingStrategy interface {
	InjectPaging(req *client.Request, pg *spec.PagingSpec, wantStart, wantCount int) any
	ExtractPaging(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, raw any, items []any, headers http.Header, pagingData any) (*cmdctx.PageResult, error)
}

// MakePagingStrategy returns the PagingStrategy implementation for strategyName.
func MakePagingStrategy(strategyName string) (PagingStrategy, error) {
	switch strategyName {
	case spec.PagingStrategyFlatList, spec.PagingStrategyNone:
		return flatListStrategy{}, nil
	case spec.PagingStrategyPageIndex:
		return pageIndexStrategy{}, nil
	case spec.PagingStrategyPageHeader:
		return pageHeaderStrategy{}, nil
	default:
		return nil, fmt.Errorf("unsupported paging strategy %q", strategyName)
	}
}

// ---------------------------------------------------------------------------
// flat_list / none
// ---------------------------------------------------------------------------

type flatListStrategy struct{}

func (flatListStrategy) InjectPaging(_ *client.Request, _ *spec.PagingSpec, _, _ int) any {
	return nil
}

func (flatListStrategy) ExtractPaging(_ *cmdctx.Ctx, _ *spec.EndpointSpec, raw any, items []any, _ http.Header, _ any) (*cmdctx.PageResult, error) {
	return &cmdctx.PageResult{
		Raw:         raw,
		Items:       items,
		StartOffset: 0,
		Last:        true,
		HasTotal:    true,
		Total:       int64(len(items)),
	}, nil
}

// ---------------------------------------------------------------------------
// page_index
// ---------------------------------------------------------------------------

type pageIndexStrategy struct{}

type pageIndexData struct {
	pageIndex   int
	pageSize    int
	startOffset int
}

func (pageIndexStrategy) InjectPaging(req *client.Request, pg *spec.PagingSpec, wantStart, wantCount int) any {
	pageSize := pg.PageSizeMax
	wantEnd := wantStart + wantCount
	var pageIndex int
	if wantEnd <= pageSize {
		pageSize = wantEnd
		pageIndex = 0
	} else {
		pageIndex = wantStart / pageSize
	}
	startOffset := pageIndex * pageSize

	indexParam := pg.PageIndexParam
	if indexParam == "" {
		indexParam = "pageIndex"
	}
	if req.QueryParams == nil {
		req.QueryParams = map[string]string{}
	}
	req.QueryParams[indexParam] = strconv.Itoa(pageIndex)
	if pg.PageSizeParam != "" && pageSize > 0 {
		req.QueryParams[pg.PageSizeParam] = strconv.Itoa(pageSize)
	}

	return pageIndexData{pageIndex: pageIndex, pageSize: pageSize, startOffset: startOffset}
}

func (pageIndexStrategy) ExtractPaging(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, raw any, items []any, _ http.Header, pagingData any) (*cmdctx.PageResult, error) {
	d := pagingData.(pageIndexData)

	pr := &cmdctx.PageResult{
		Raw:         raw,
		Items:       items,
		StartOffset: d.startOffset,
	}

	if ep.Paging.TotalExpr != "" {
		exprEnv := exprenv.WithIt(exprenv.Make(ctx), raw)
		v, ok := exprenv.EvalExprAny(exprEnv, ep.Paging.TotalExpr)
		if !ok {
			return nil, fmt.Errorf("page_index: total_expr %q did not resolve", ep.Paging.TotalExpr)
		}
		n, err := toInt64(v)
		if err != nil {
			return nil, fmt.Errorf("page_index: total_expr %q resolved to unexpected type %T", ep.Paging.TotalExpr, v)
		}
		pr.HasTotal, pr.Total = true, n
	}

	pr.Last = len(items) == 0 ||
		(d.pageSize > 0 && len(items) < d.pageSize) ||
		(pr.HasTotal && int64(d.startOffset+len(items)) >= pr.Total)

	return pr, nil
}

// ---------------------------------------------------------------------------
// page_header
// ---------------------------------------------------------------------------

type pageHeaderStrategy struct{}

type pageHeaderData struct {
	pageIndex   int
	pageSize    int
	startOffset int
}

func (pageHeaderStrategy) InjectPaging(req *client.Request, pg *spec.PagingSpec, wantStart, wantCount int) any {
	pageSize := pg.PageSizeMax
	wantEnd := wantStart + wantCount
	var pageIndex int
	if wantEnd <= pageSize {
		pageSize = wantEnd
		pageIndex = 0
	} else {
		pageIndex = wantStart / pageSize
	}
	startOffset := pageIndex * pageSize

	indexParam := pg.PageIndexParam
	if indexParam == "" {
		indexParam = "pageIndex"
	}
	if req.QueryParams == nil {
		req.QueryParams = map[string]string{}
	}
	req.QueryParams[indexParam] = strconv.Itoa(pageIndex + pg.PageBase)
	if pg.PageSizeParam != "" && pageSize > 0 {
		req.QueryParams[pg.PageSizeParam] = strconv.Itoa(pageSize)
	}

	return pageHeaderData{pageIndex: pageIndex, pageSize: pageSize, startOffset: startOffset}
}

func (pageHeaderStrategy) ExtractPaging(_ *cmdctx.Ctx, ep *spec.EndpointSpec, raw any, items []any, headers http.Header, pagingData any) (*cmdctx.PageResult, error) {
	d := pagingData.(pageHeaderData)

	totalHeader := ep.Paging.TotalHeader
	if totalHeader == "" {
		totalHeader = "X-Total-Elements"
	}

	pr := &cmdctx.PageResult{
		Raw:         raw,
		Items:       items,
		StartOffset: d.startOffset,
	}
	if v, err := strconv.ParseInt(headers.Get(totalHeader), 10, 64); err == nil {
		pr.HasTotal, pr.Total = true, v
	} else if ep.Paging.IsCountable() {
		return nil, fmt.Errorf("page_header: missing or invalid %s header", totalHeader)
	}

	pr.Last = len(items) == 0 ||
		(d.pageSize > 0 && len(items) < d.pageSize) ||
		(int64(d.startOffset+len(items)) >= pr.Total)

	return pr, nil
}
