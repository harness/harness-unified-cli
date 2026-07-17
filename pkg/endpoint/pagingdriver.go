// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"fmt"

	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/format"
	"github.com/harness/cli/pkg/hlog"
	"github.com/harness/cli/pkg/spec"
)

const maxItemsAll = 100_000_000
const defaultPageSize = 20

// FetchItems normalizes PagingFlags into the appropriate FetchRange/FetchAll call.
// --all maps to FetchAll; otherwise FetchRange is called with the offset/limit from flags.
func FetchItems(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, pf cmdctx.PagingFlags) ([]any, *format.PageMeta, error) {
	if ep.Paging == nil {
		return nil, nil, fmt.Errorf("FetchItems called on endpoint with no paging spec")
	}
	if pf.Offset < 0 || pf.Limit < 0 {
		return nil, nil, fmt.Errorf("offset and limit must be non-negative")
	}
	if pf.All {
		return fetchRange(ctx, ep, 0, maxItemsAll, pf.OnPage)
	}
	return fetchRange(ctx, ep, pf.Offset, pf.Limit, pf.OnPage)
}

// FetchRange fetches items [wantStart, wantStart+wantCount) using the FetchFn
// resolved from ep. It is strategy-blind: all paging knowledge lives in the FetchFn.
// If wantCount is 0 it defaults to defaultPageSize.
func FetchRange(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, wantStart, wantCount int) ([]any, *format.PageMeta, error) {
	return fetchRange(ctx, ep, wantStart, wantCount, nil)
}

// fetchRange is the implementation behind FetchRange/FetchAll, with an optional
// per-page progress callback.
func fetchRange(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, wantStart, wantCount int, onPage func(int, int64, bool)) ([]any, *format.PageMeta, error) {
	fn, err := ResolveFetchFn(ctx, ep)
	if err != nil {
		return nil, nil, err
	}
	if wantCount == 0 {
		wantCount = defaultPageSize
	}

	wantEnd := wantStart + wantCount

	var out []any
	meta := &format.PageMeta{Offset: wantStart}

	var cursor any
	pos := wantStart
	prevOffset := -1

	for pos < wantEnd {
		p, err := fn(ctx, ep, pos, wantEnd-pos, cursor)
		if err != nil {
			return nil, nil, err
		}

		if !p.Last && p.StartOffset == prevOffset {
			return nil, nil, fmt.Errorf("paging stalled at offset %d — fetch_fn bug or unexpected API response", p.StartOffset)
		}
		prevOffset = p.StartOffset

		if p.HasTotal && !meta.HasTotal {
			meta.HasTotal = true
			meta.Total = p.Total
		}

		// Slice out only the items that fall within [wantStart, wantEnd).
		pageStart := p.StartOffset
		pageEnd := p.StartOffset + len(p.Items)
		lo := max(pageStart, wantStart) - pageStart
		hi := min(pageEnd, wantEnd) - pageStart
		if lo < hi {
			out = append(out, p.Items[lo:hi]...)
		}

		cursor = p.NextCursor
		pos = pageEnd

		if onPage != nil {
			onPage(len(out), meta.Total, meta.HasTotal)
		}

		hlog.Debug("FetchRange page", "noun", ctx.Noun, "start_offset", p.StartOffset, "items", len(p.Items), "last", p.Last, "total_so_far", len(out))

		if p.Last || len(p.Items) == 0 {
			break
		}
	}

	meta.Count = len(out)
	return out, meta, nil
}

// FetchAll fetches every available item from offset 0.
func FetchAll(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) ([]any, *format.PageMeta, error) {
	return FetchRange(ctx, ep, 0, maxItemsAll)
}

// FetchCount returns the total number of items available without fetching them all.
// For strategies that return a total in the first response (page_index, page_header)
// this is a single API call. For flat_list it fetches everything and counts.
// Returns an error if the endpoint does not support counting.
func FetchCount(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (int64, error) {
	fn, err := ResolveFetchFn(ctx, ep)
	if err != nil {
		return 0, err
	}
	if ep.Paging == nil {
		return 0, fmt.Errorf("FetchCount called on endpoint with no paging spec")
	}
	hlog.Debug("FetchCount", "noun", ctx.Noun)
	p, err := fn(ctx, ep, 0, 1, nil)
	if err != nil {
		return 0, err
	}
	if !p.HasTotal {
		return 0, fmt.Errorf("--count not available for %s %s (strategy %q returns no total)", ctx.Verb, ctx.Noun, ep.Paging.PagingStrategy)
	}
	hlog.Debug("FetchCount resolved", "noun", ctx.Noun, "total", p.Total)
	return p.Total, nil
}
