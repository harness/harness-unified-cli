// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package code

import (
	"fmt"
	"maps"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/endpoint"
	"github.com/harness/harness-cli/pkg/spec"
)

const listMinePRFetchFnID = "list_mine_pr_fetch"

// listMinePRFetchFn is a FetchFn for "list pr:mine". It resolves the current
// user's Code principal ID on first call then delegates to HTTPFetchFn with
// author_id injected.
func listMinePRFetchFn(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, wantStart, wantCount int, cursor any) (*cmdctx.PageResult, error) {
	principalID, err := CurrentUserPrincipalID(ctx)
	if err != nil {
		return nil, err
	}

	// Clone the EndpointSpec so we don't mutate the shared spec.
	epCopy := *ep
	qp := make(map[string]string, len(ep.QueryParams)+3)
	for k, v := range ep.QueryParams {
		qp[k] = v
	}
	// Inject current-user filter and state. The state flag maps directly since
	// this endpoint accepts "open", "closed", "merged" (no "all" sentinel).
	qp["author_id"] = fmt.Sprintf("%d", principalID)
	state := cmdctx.GetString(ctx.FlagValues, "state")
	if state != "all" && state != "" {
		qp["state"] = state
	}
	createdAfter := cmdctx.GetString(ctx.FlagValues, "created-after")
	if createdAfter != "" {
		qp["created_gt"] = createdAfter
	}
	createdBefore := cmdctx.GetString(ctx.FlagValues, "created-before")
	if createdBefore != "" {
		qp["created_lt"] = createdBefore
	}
	epCopy.QueryParams = qp

	result, err := endpoint.HTTPFetchFn(ctx, &epCopy, wantStart, wantCount, cursor)
	if err != nil {
		return nil, err
	}
	for i, raw := range result.Items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pr, _ := m["pull_request"].(map[string]any)
		if pr == nil {
			continue
		}
		repo := m["repository"]
		flat := make(map[string]any, len(pr)+1)
		maps.Copy(flat, pr)
		flat["repository"] = repo
		result.Items[i] = flat
	}
	return result, nil
}
