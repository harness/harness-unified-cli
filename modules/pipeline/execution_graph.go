// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/execgraph"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/spec"
)

const listExecutionStepsFetchFnID = "list_execution_steps_fetch"

func assignRanks(id string, depth int, nodes map[string]execgraph.GraphNode, adj map[string]execgraph.AdjacencyEntry) {
	execgraph.AssignRanks(id, depth, nodes, adj)
}

func fetchExecutionGraph(ctx *cmdctx.Ctx, execId string) (execgraph.ExecutionGraph, error) {
	return execgraph.FetchExecutionGraph(ctx, execId)
}

func reUnmarshal[T any](data any) (T, error) {
	return execgraph.ReUnmarshal[T](data)
}

// skipStepTypes are internal Harness plumbing nodes that add no value for the user.
var skipStepTypes = map[string]bool{
	"PIPELINE_SECTION": true,
	"STAGES_STEP":      true,
	"NG_EXECUTION":     true,
}

// followSkipStepTypes extends skipStepTypes with container nodes that have no
// meaningful log content of their own and should not get SSE streams.
var followSkipStepTypes = map[string]bool{
	"PIPELINE_SECTION":        true,
	"STAGES_STEP":             true,
	"NG_EXECUTION":            true,
	"STRATEGY":                true,
	"IntegrationStageStepPMS": true,
}

func statusIcon(status string) string {
	s := format.BucketStyles[format.ClassifyExecutionStatus(status)]
	return console.WithColor(console.Color(s.AnsiCode), s.NodeGlyph)
}

func fmtNodeDuration(startTs, endTs int64) string {
	if startTs == 0 || endTs == 0 {
		return ""
	}
	secs := (endTs - startTs) / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%ds", secs/60, secs%60)
}

func printExecutionTree(w io.Writer, g execgraph.ExecutionGraph) {
	if g.RootNodeID == "" {
		return
	}
	assignRanks(g.RootNodeID, 1, g.NodeMap, g.NodeAdjacencyListMap)
	visited := make(map[string]bool)
	printTreeNode(w, g.RootNodeID, 0, visited, g.NodeMap, g.NodeAdjacencyListMap)
}

func printTreeNode(w io.Writer, id string, depth int, visited map[string]bool, nodes map[string]execgraph.GraphNode, adj map[string]execgraph.AdjacencyEntry) {
	if visited[id] {
		return
	}
	visited[id] = true
	node := nodes[id]

	skip := skipStepTypes[node.StepType]
	nextDepth := depth
	if !skip {
		indent := strings.Repeat("  ", depth)
		dur := fmtNodeDuration(node.StartTs, node.EndTs)
		icon := statusIcon(node.Status)
		childExecID := node.StepDetails.ChildPipelineExecutionDetails.PlanExecutionID
		suffix := ""
		if childExecID != "" {
			suffix = fmt.Sprintf(" (⇒ %s)", childExecID)
		}
		if dur != "" {
			fmt.Fprintf(w, "%s%s %s (%s)%s\n", indent, icon, execgraph.NodeName(node), dur, suffix)
		} else {
			fmt.Fprintf(w, "%s%s %s%s\n", indent, icon, execgraph.NodeName(node), suffix)
		}
		nextDepth = depth + 1
	}

	for _, childID := range adj[id].Children {
		printTreeNode(w, childID, nextDepth, visited, nodes, adj)
	}
	for _, nextID := range adj[id].NextIDs {
		printTreeNode(w, nextID, depth, visited, nodes, adj)
	}
}

func listExecutionStepsFetchFn(ctx *cmdctx.Ctx, _ *spec.EndpointSpec, _, _ int, _ any) (*cmdctx.PageResult, error) {
	execId := ctx.ParentId
	if execId == "" {
		return nil, fmt.Errorf("missing required argument <execution-id>")
	}
	if i := strings.LastIndex(execId, "/"); i >= 0 {
		execId = execId[i+1:]
	}

	g, err := fetchExecutionGraph(ctx, execId)
	if err != nil {
		return nil, err
	}

	assignRanks(g.RootNodeID, 1, g.NodeMap, g.NodeAdjacencyListMap)

	nodes := execgraph.WalkNodes(g, skipStepTypes)
	var rows []any
	for _, node := range nodes {
		indent := strings.Repeat("  ", node.Depth)
		delegate := ""
		if len(node.DelegateInfoList) > 0 {
			delegate = node.DelegateInfoList[0].Name
		}
		inputs := ""
		if len(node.StepParameters) > 0 {
			inputs = string(node.StepParameters)
		}
		outputs := ""
		if len(node.Outcomes) > 0 {
			if b, err := json.Marshal(node.Outcomes); err == nil {
				outputs = string(b)
			}
		}
		rows = append(rows, map[string]any{
			"name":               indent + execgraph.NodeName(node),
			"type":               node.StepType,
			"status":             node.Status,
			"duration":           fmtNodeDuration(node.StartTs, node.EndTs),
			"delegate":           delegate,
			"error":              node.FailureInfo.Message,
			"identifier":         node.Identifier,
			"fqn":                node.BaseFQN,
			"log_key":            node.LogBaseKey,
			"uuid":               node.UUID,
			"started":            node.StartTs,
			"child_execution_id": node.StepDetails.ChildPipelineExecutionDetails.PlanExecutionID,
			"inputs":             inputs,
			"outputs":            outputs,
		})
	}

	return &cmdctx.PageResult{
		Items:       rows,
		StartOffset: 0,
		Last:        true,
		HasTotal:    true,
		Total:       int64(len(rows)),
	}, nil
}
