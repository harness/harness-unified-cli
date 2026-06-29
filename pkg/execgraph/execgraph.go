// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package execgraph

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
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

type ChildPipelineExecutionDetails struct {
	PlanExecutionID string `json:"planExecutionId"`
	OrgID           string `json:"orgId"`
	ProjectID       string `json:"projectId"`
}

type StepDetails struct {
	ChildPipelineExecutionDetails ChildPipelineExecutionDetails `json:"childPipelineExecutionDetails"`
}

type TaskExecutableResponse struct {
	TaskID       string   `json:"taskId"`
	TaskName     string   `json:"taskName"`
	TaskCategory string   `json:"taskCategory"`
	LogKeys      []string `json:"logKeys"`
	Units        []string `json:"units"`
}

type TaskChainExecutableResponse struct {
	TaskID       string   `json:"taskId"`
	TaskName     string   `json:"taskName"`
	TaskCategory string   `json:"taskCategory"`
	ChainEnd     bool     `json:"chainEnd"`
	LogKeys      []string `json:"logKeys"`
	Units        []string `json:"units"`
}

type AsyncExecutableResponse struct {
	CallbackIDs                           []string `json:"callbackIds"`
	LogKeys                               []string `json:"logKeys"`
	Units                                 []string `json:"units"`
	Status                                string   `json:"status"`
	Timeout                               int64    `json:"timeout,string"`
	ShouldRemoveAlreadyProcessedNotifyIDs bool     `json:"shouldRemoveAlreadyProcessedNotifyIds"`
}

type AsyncChainExecutableResponse struct {
	CallbackID  string   `json:"callbackId"`
	CallbackIDs []string `json:"callbackIds"`
	ChainEnd    bool     `json:"chainEnd"`
	LogKeys     []string `json:"logKeys"`
	Units       []string `json:"units"`
	Status      string   `json:"status"`
	Timeout     int64    `json:"timeout,string"`
}

type SyncExecutableResponse struct {
	LogKeys []string `json:"logKeys"`
	Units   []string `json:"units"`
}

type ChildExecutableResponse struct {
	ChildNodeID string   `json:"childNodeId"`
	Skip        bool     `json:"skip"`
	LogKeys     []string `json:"logKeys"`
	Units       []string `json:"units"`
}

type ChildrenExecutableResponse struct {
	MaxConcurrency        int64    `json:"maxConcurrency,string"`
	ShouldProceedIfFailed bool     `json:"shouldProceedIfFailed"`
	LogKeys               []string `json:"logKeys"`
	Units                 []string `json:"units"`
}

type ChildChainExecutableResponse struct {
	NextChildID     string `json:"nextChildId"`
	PreviousChildID string `json:"previousChildId"`
	LastLink        bool   `json:"lastLink"`
	Suspend         bool   `json:"suspend"`
}

type SkipTaskExecutableResponse struct {
	Message string `json:"message"`
}

type FacilitatorExecutableResponse struct {
	Type             string   `json:"type"`
	Status           string   `json:"status"`
	StartTs          int64    `json:"startTs"`
	CallbackIDs      []string `json:"callbackIds"`
	TimeoutInSeconds int64    `json:"timeoutInSeconds"`
}

type ExecutableResponse struct {
	ResponseCase string                         `json:"responseCase"`
	Task         *TaskExecutableResponse        `json:"task"`
	TaskChain    *TaskChainExecutableResponse   `json:"taskChain"`
	Async        *AsyncExecutableResponse       `json:"async"`
	Sync         *SyncExecutableResponse        `json:"sync"`
	Child        *ChildExecutableResponse       `json:"child"`
	Children     *ChildrenExecutableResponse    `json:"children"`
	ChildChain   *ChildChainExecutableResponse  `json:"childChain"`
	AsyncChain   *AsyncChainExecutableResponse  `json:"asyncChain"`
	SkipTask     *SkipTaskExecutableResponse    `json:"skipTask"`
	Facilitator  *FacilitatorExecutableResponse `json:"facilitator"`
}

type GraphNode struct {
	UUID                string               `json:"uuid"`
	SetupID             string               `json:"setupId"`
	Identifier          string               `json:"identifier"`
	Name                string               `json:"name"`
	BaseFQN             string               `json:"baseFqn"`
	StepType            string               `json:"stepType"`
	Status              string               `json:"status"`
	LogBaseKey          string               `json:"logBaseKey"`
	StartTs             int64                `json:"startTs"`
	EndTs               int64                `json:"endTs"`
	DelegateInfoList    []DelegateInfo       `json:"delegateInfoList"`
	FailureInfo         FailureInfo          `json:"failureInfo"`
	StepDetails         StepDetails          `json:"stepDetails"`
	StepParameters      json.RawMessage      `json:"stepParameters"`
	Outcomes            map[string]any       `json:"outcomes"`
	ExecutableResponses []ExecutableResponse `json:"executableResponses"`

	Raw   json.RawMessage `json:"-"` // full wire JSON, populated by FetchExecutionGraph
	Rank  int             // computed, not from JSON
	Depth int             // computed, not from JSON
}

// UnmarshalJSON captures the raw bytes for full-fidelity JSON output, then
// decodes the typed fields the CLI actually needs.
func (n *GraphNode) UnmarshalJSON(data []byte) error {
	n.Raw = json.RawMessage(data)
	type plain GraphNode // avoids infinite recursion
	return json.Unmarshal(data, (*plain)(n))
}

// ToMap returns the full wire JSON of the node as a map, suitable for output
// rendering. Computed fields (Rank, Depth) are included. Callers may patch
// additional enrichments on top.
func (n GraphNode) ToMap() map[string]any {
	var m map[string]any
	json.Unmarshal(n.Raw, &m) //nolint
	if m == nil {
		m = make(map[string]any)
	}
	m["rank"] = n.Rank
	m["depth"] = n.Depth
	return m
}

// HasLogs reports whether the node has any log content to fetch.
func HasLogs(node GraphNode) bool {
	return node.LogBaseKey != ""
}

// GetLogKey returns the correct log service key to use when fetching logs for
// a node. Steps like ShellScript and Http store their logs at a sub-key
// embedded in executableResponses rather than at logBaseKey directly.
func GetLogKey(node GraphNode) string {
	keys := GetAllLogKeys(node)
	if len(keys) > 0 {
		return keys[0]
	}
	return node.LogBaseKey
}

// GetAllLogKeys returns all log keys from a node's executableResponses.
func GetAllLogKeys(node GraphNode) []string {
	var keys []string
	for _, er := range node.ExecutableResponses {
		for _, lk := range erLogKeys(er) {
			if lk != "" {
				keys = append(keys, lk)
			}
		}
	}
	return keys
}

func erLogKeys(er ExecutableResponse) []string {
	if er.Task != nil {
		return er.Task.LogKeys
	}
	if er.TaskChain != nil {
		return er.TaskChain.LogKeys
	}
	if er.Async != nil {
		return er.Async.LogKeys
	}
	if er.AsyncChain != nil {
		return er.AsyncChain.LogKeys
	}
	if er.Sync != nil {
		return er.Sync.LogKeys
	}
	if er.Child != nil {
		return er.Child.LogKeys
	}
	if er.Children != nil {
		return er.Children.LogKeys
	}
	return nil
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

func FetchExecutionGraph(ctx *cmdctx.Ctx, execId string) (ExecutionGraph, error) {
	path := fmt.Sprintf("/pipeline/api/pipelines/execution/v2/%s", url.PathEscape(execId))
	hlog.Debug("fetching execution graph", "execId", execId)
	resp, err := client.New(ctx).DoRaw(client.Request{
		Method: "GET",
		Path:   path,
		QueryParams: map[string]string{
			"orgIdentifier":         ctx.Auth.OrgID,
			"projectIdentifier":     ctx.Auth.ProjectID,
			"renderFullBottomGraph": "true",
		},
	})
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

// WalkNodes traverses the execution graph in display order and returns a flat slice of
// GraphNode values with Depth set. Nodes whose StepType is in skipTypes are not included
// in the output but their children are still walked at the same depth.
func WalkNodes(g ExecutionGraph, skipTypes map[string]bool) []GraphNode {
	visited := make(map[string]bool)
	var result []GraphNode
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if visited[id] {
			return
		}
		visited[id] = true
		node := g.NodeMap[id]
		adj := g.NodeAdjacencyListMap[id]
		nextDepth := depth
		if !skipTypes[node.StepType] {
			node.Depth = depth
			result = append(result, node)
			nextDepth = depth + 1
		}
		for _, child := range adj.Children {
			walk(child, nextDepth)
		}
		for _, next := range adj.NextIDs {
			walk(next, depth)
		}
	}
	if g.RootNodeID != "" {
		walk(g.RootNodeID, 0)
	}
	return result
}

// FetchExecutionFull fetches the execution graph and pipeline-level status in one call.
func FetchExecutionFull(ctx *cmdctx.Ctx, execId string) (ExecutionFull, error) {
	path := fmt.Sprintf("/pipeline/api/pipelines/execution/v2/%s", url.PathEscape(execId))
	hlog.Debug("fetching execution full", "execId", execId)
	resp, err := client.New(ctx).DoRaw(client.Request{
		Method: "GET",
		Path:   path,
		QueryParams: map[string]string{
			"orgIdentifier":         ctx.Auth.OrgID,
			"projectIdentifier":     ctx.Auth.ProjectID,
			"renderFullBottomGraph": "true",
		},
	})
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
