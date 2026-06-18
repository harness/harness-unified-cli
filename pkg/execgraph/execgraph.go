// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package execgraph

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/hlog"
)

type ExecutionGraph struct {
	RootNodeID           string                    `json:"rootNodeId"`
	NodeMap              map[string]GraphNode      `json:"nodeMap"`
	NodeAdjacencyListMap map[string]AdjacencyEntry `json:"nodeAdjacencyListMap"`
}

type DelegateInfo struct {
	Name string `json:"name"`
}

type FailureInfo struct {
	Message string `json:"message"`
}

type GraphNode struct {
	UUID             string         `json:"uuid"`
	SetupID          string         `json:"setupId"`
	Identifier       string         `json:"identifier"`
	Name             string         `json:"name"`
	BaseFQN          string         `json:"baseFqn"`
	StepType         string         `json:"stepType"`
	Status           string         `json:"status"`
	LogBaseKey       string         `json:"logBaseKey"`
	StartTs          int64          `json:"startTs"`
	EndTs            int64          `json:"endTs"`
	DelegateInfoList []DelegateInfo `json:"delegateInfoList"`
	FailureInfo      FailureInfo    `json:"failureInfo"`

	Rank int // computed, not from JSON
}

type AdjacencyEntry struct {
	Children []string `json:"children"`
	NextIDs  []string `json:"nextIds"`
}

type ExecutionFull struct {
	Graph          ExecutionGraph
	PipelineStatus string
	StartTs        int64
	EndTs          int64
}

func NodeName(node GraphNode) string {
	if node.StepType == "liteEngineTask" {
		return "Initialize"
	}
	if node.Name != "" {
		return node.Name
	}
	return node.Identifier
}

func AssignRanks(id string, depth int, nodes map[string]GraphNode, adj map[string]AdjacencyEntry) {
	node, ok := nodes[id]
	if !ok {
		return
	}
	if node.Rank != 0 && node.Rank <= depth {
		return
	}
	node.Rank = depth
	nodes[id] = node
	for _, child := range adj[id].Children {
		AssignRanks(child, depth+1, nodes, adj)
	}
	for _, next := range adj[id].NextIDs {
		AssignRanks(next, depth, nodes, adj)
	}
}

func ReUnmarshal[T any](data any) (T, error) {
	var zero T
	b, err := json.Marshal(data)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return zero, err
	}
	return out, nil
}

func FetchExecutionGraph(hc *http.Client, a *auth.ResolvedAuth, execId string) (ExecutionGraph, error) {
	u, err := url.Parse(fmt.Sprintf("%s/pipeline/api/pipelines/execution/v2/%s", a.APIUrl, url.PathEscape(execId)))
	if err != nil {
		return ExecutionGraph{}, err
	}
	q := u.Query()
	q.Set("accountIdentifier", a.AccountID)
	q.Set("orgIdentifier", a.OrgID)
	q.Set("projectIdentifier", a.ProjectID)
	q.Set("renderFullBottomGraph", "true")
	u.RawQuery = q.Encode()
	hlog.Debug("fetching execution graph", "execId", execId, "url", u)

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return ExecutionGraph{}, err
	}
	req.Header.Set("x-api-key", a.PATToken)
	resp, err := hc.Do(req)
	if err != nil {
		return ExecutionGraph{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ExecutionGraph{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExecutionGraph{}, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}
	var envelope struct {
		Data struct {
			ExecutionGraph ExecutionGraph `json:"executionGraph"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ExecutionGraph{}, fmt.Errorf("decoding execution graph: %w", err)
	}
	return envelope.Data.ExecutionGraph, nil
}

// FetchExecutionFull fetches the execution graph and pipeline-level status in one call.
func FetchExecutionFull(hc *http.Client, a *auth.ResolvedAuth, execId string) (ExecutionFull, error) {
	u, err := url.Parse(fmt.Sprintf("%s/pipeline/api/pipelines/execution/v2/%s", a.APIUrl, url.PathEscape(execId)))
	if err != nil {
		return ExecutionFull{}, err
	}
	q := u.Query()
	q.Set("accountIdentifier", a.AccountID)
	q.Set("orgIdentifier", a.OrgID)
	q.Set("projectIdentifier", a.ProjectID)
	q.Set("renderFullBottomGraph", "true")
	u.RawQuery = q.Encode()
	hlog.Debug("fetching execution full", "execId", execId, "url", u)

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return ExecutionFull{}, err
	}
	req.Header.Set("x-api-key", a.PATToken)
	resp, err := hc.Do(req)
	if err != nil {
		return ExecutionFull{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ExecutionFull{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExecutionFull{}, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}
	var envelope struct {
		Data struct {
			Summary struct {
				Status  string `json:"status"`
				StartTs int64  `json:"startTs"`
				EndTs   int64  `json:"endTs"`
			} `json:"pipelineExecutionSummary"`
			ExecutionGraph ExecutionGraph `json:"executionGraph"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ExecutionFull{}, fmt.Errorf("decoding execution: %w", err)
	}
	return ExecutionFull{
		Graph:          envelope.Data.ExecutionGraph,
		PipelineStatus: envelope.Data.Summary.Status,
		StartTs:        envelope.Data.Summary.StartTs,
		EndTs:          envelope.Data.Summary.EndTs,
	}, nil
}
