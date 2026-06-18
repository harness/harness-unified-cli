// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package logstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/execgraph"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/hlog"
)

// SseTerminalDrainDelay is how long we wait after the pipeline reaches a terminal state
// before cancelling SSE streams, to allow straggling log events to arrive.
const SseTerminalDrainDelay = 5 * time.Second

// BaseSkipStepTypes are container nodes with no meaningful log content.
// Callers can extend with their own module-specific types via FollowMulti's extraSkipTypes param.
var BaseSkipStepTypes = map[string]bool{
	"PIPELINE_SECTION":        true,
	"STAGES_STEP":             true,
	"NG_EXECUTION":            true,
	"STRATEGY":                true,
	"IntegrationStageStepPMS": true,
}

type EventKind int

const (
	EvStart   EventKind = iota // >>> started
	EvEnd                      // <<< completed / failed / aborted / expired
	EvLogLine                  // single rendered log line (from SSE, one per frame)
	EvBlob                     // full blob content sent as one chunk
)

type Event struct {
	Kind    EventKind
	Source  string
	StartTs int64               // EvStart
	Node    execgraph.GraphNode // EvEnd
	Lines   []string            // EvLogLine / EvBlob
}

type LogKeyEntry struct {
	LogKey string
	Name   string
	Status string
}

func RenderLogLinesToWriter(text, fmtFlag string, isPty bool, w io.Writer) error {
	if fmtFlag == "json" || fmtFlag == "jsonl" {
		_, err := fmt.Fprint(w, text)
		return err
	}

	type logLine struct {
		Level string `json:"level"`
		Out   string `json:"out"`
		Time  string `json:"time"`
	}

	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var ll logLine
		if err := json.Unmarshal([]byte(line), &ll); err != nil {
			fmt.Fprintln(w, line)
			continue
		}
		ts := ll.Time
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = t.UTC().Format("2006-01-02 15:04:05")
		}
		out := strings.TrimRight(ll.Out, "\n")
		if !isPty {
			out = console.StripANSI(out)
			fmt.Fprintf(w, "%s [%s] %s\n", ts, ll.Level, out)
		} else {
			fmt.Fprintf(w, "%s [%s] %s\033[0m\n", ts, ll.Level, out)
		}
	}
	return sc.Err()
}

// FetchAndPrintLog fetches a log blob and writes it to out.
// Returns (false, nil) when the blob exists but is empty.
func FetchAndPrintLog(hc *http.Client, a *auth.ResolvedAuth, shortKey, fmtFlag string, isPty bool, out io.Writer) (bool, error) {
	logKey := shortKey
	if !strings.Contains(logKey, "/pipeline/") {
		logKey = a.AccountID + "/pipeline/" + logKey
	}

	blobURL, err := url.Parse(a.APIUrl + "/gateway/log-service/blob")
	if err != nil {
		return false, fmt.Errorf("building URL: %w", err)
	}
	q := blobURL.Query()
	q.Set("accountID", a.AccountID)
	q.Set("key", logKey)
	blobURL.RawQuery = q.Encode()

	hlog.Debug("fetching pipeline log", "url", blobURL.String(), "key", logKey)

	req, err := http.NewRequest("GET", blobURL.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("x-api-key", a.PATToken)

	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetching pipeline log: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("reading pipeline log response: %w", err)
	}
	hlog.Debug("pipeline log response", "status", resp.StatusCode, "bytes", len(body), "key", logKey)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("pipeline log error %d: %s", resp.StatusCode, string(body))
	}
	if len(body) == 0 {
		return false, nil
	}
	if fmtFlag == "json" || fmtFlag == "jsonl" {
		_, err = fmt.Fprint(out, string(body))
		return true, err
	}
	return true, RenderLogLinesToWriter(string(body), fmtFlag, isPty, out)
}

// FetchAndPrintLogWithRetry wraps FetchAndPrintLog with retries for recently-completed steps
// whose blob may not yet be written. endTs is the step's end timestamp in milliseconds;
// if the blob is empty and the step finished within 60s, it retries up to 3 times (2s apart).
func FetchAndPrintLogWithRetry(hc *http.Client, a *auth.ResolvedAuth, shortKey, fmtFlag string, isPty bool, out io.Writer, endTs int64) (bool, error) {
	const (
		maxRetries  = 3
		retryDelay  = 2 * time.Second
		retryWindow = 60 * time.Second
	)
	age := time.Duration(-1)
	if endTs > 0 {
		age = time.Since(time.UnixMilli(endTs)).Truncate(time.Millisecond)
	}
	recentlyEnded := endTs > 0 && age < retryWindow
	hlog.Debug("blob fetch with retry", "key", shortKey, "endTs", endTs, "age", age, "recentlyEnded", recentlyEnded)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if !recentlyEnded {
				hlog.Debug("blob retry skipped: step not recent enough", "key", shortKey, "age", age)
				break
			}
			hlog.Debug("blob empty, retrying", "key", shortKey, "attempt", attempt)
			time.Sleep(retryDelay)
		}
		var buf strings.Builder
		hasContent, err := FetchAndPrintLog(hc, a, shortKey, fmtFlag, isPty, &buf)
		hlog.Debug("blob fetch result", "key", shortKey, "attempt", attempt, "hasContent", hasContent, "err", err)
		if err != nil || hasContent {
			if hasContent {
				_, werr := fmt.Fprint(out, buf.String())
				if werr != nil {
					return true, werr
				}
			}
			return hasContent, err
		}
	}
	hlog.Debug("blob fetch gave up: no content after retries", "key", shortKey)
	return false, nil
}

// FmtDuration formats a millisecond duration for event lines.
func FmtDuration(ms int64) string {
	if ms < 1000 {
		return "<1s"
	}
	secs := ms / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%ds", secs/60, secs%60)
}

// ShortLogKey strips the leading accountId/pipeline/ prefix from a logBaseKey.
func ShortLogKey(key string) string {
	if idx := strings.Index(key, "pipeline/"); idx >= 0 {
		return key[idx+len("pipeline/"):]
	}
	return key
}

// FindNodeForFollow finds the graph node whose logBaseKey best matches the given short key.
func FindNodeForFollow(g execgraph.ExecutionGraph, shortKey string) (execgraph.GraphNode, bool) {
	for _, node := range g.NodeMap {
		if node.LogBaseKey == "" {
			continue
		}
		if ShortLogKey(node.LogBaseKey) == shortKey {
			return node, true
		}
	}
	return execgraph.GraphNode{}, false
}

// IsTerminalStatus returns true when a step or pipeline has reached a final state.
func IsTerminalStatus(status string) bool {
	switch format.ClassifyExecutionStatus(status) {
	case format.StatusSuccess, format.StatusSkipped, format.StatusFailed:
		return true
	}
	return false
}

// WriteEndEvent writes the <<< end event line for a node.
func WriteEndEvent(w io.Writer, source string, node execgraph.GraphNode) {
	ts := node.EndTs
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	t := time.UnixMilli(ts).UTC()
	status := strings.ToLower(node.Status)
	var dur string
	if node.StartTs > 0 {
		dur = FmtDuration(node.EndTs - node.StartTs)
	} else {
		dur = "0s"
	}
	switch status {
	case "skipped":
		fmt.Fprintf(w, "<<< completed %s %s skipped\n", source, t.Format("15:04:05"))
	case "success", "succeeded", "ignorefailed":
		fmt.Fprintf(w, "<<< completed %s %s success %s\n", source, t.Format("15:04:05"), dur)
	case "aborted", "abortedbyfreeze":
		fmt.Fprintf(w, "<<< aborted %s %s %s\n", source, t.Format("15:04:05"), dur)
	case "expired":
		fmt.Fprintf(w, "<<< expired %s %s %s\n", source, t.Format("15:04:05"), dur)
	default:
		msg := node.FailureInfo.Message
		if msg == "" {
			msg = status
		}
		msg = strings.ReplaceAll(msg, `"`, `\"`)
		fmt.Fprintf(w, "<<< failed %s %s \"%s\"\n", source, t.Format("15:04:05"), msg)
	}
}

// WriteEvents drains ch and writes events to w.
// Emits switching markers ("==> source") before log content when the source changes.
func WriteEvents(ch <-chan Event, w io.Writer) {
	lastSource := ""
	for ev := range ch {
		switch ev.Kind {
		case EvStart:
			ts := ev.StartTs
			if ts == 0 {
				ts = time.Now().UnixMilli()
			}
			fmt.Fprintf(w, ">>> started %s %s\n", ev.Source, time.UnixMilli(ts).UTC().Format("15:04:05.000"))
		case EvEnd:
			WriteEndEvent(w, ev.Source, ev.Node)
		case EvLogLine, EvBlob:
			if ev.Source != lastSource {
				fmt.Fprintf(w, "==> %s\n", ev.Source)
				lastSource = ev.Source
			}
			for _, line := range ev.Lines {
				fmt.Fprint(w, line)
			}
		}
	}
}

// StreamSSEToChannel opens the log-service SSE stream and sends each rendered log frame
// as an EvLogLine event. Returns (hadContent, error): hadContent is true if at least one
// log line was sent to ch. Returns when the server closes with an eof event, on error,
// or when ctx is cancelled.
func StreamSSEToChannel(ctx context.Context, hc *http.Client, a *auth.ResolvedAuth, logKey, source, fmtFlag string, isPty bool, ch chan<- Event) (bool, error) {
	u, err := url.Parse(a.APIUrl + "/gateway/log-service/stream")
	if err != nil {
		return false, fmt.Errorf("building SSE URL: %w", err)
	}
	q := u.Query()
	q.Set("accountID", a.AccountID)
	q.Set("key", logKey)
	u.RawQuery = q.Encode()
	hlog.Debug("SSE stream", "url", u.String())

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("x-api-key", a.PATToken)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("SSE error %d: %s", resp.StatusCode, string(body))
	}

	var hadContent bool
	sc := bufio.NewScanner(resp.Body)
	var eventType, dataLine string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, ":") {
			continue
		}
		if line == "" {
			if eventType == "error" && dataLine == "eof" {
				return hadContent, nil
			}
			if (eventType == "" || eventType == "message") && dataLine != "" {
				var buf strings.Builder
				RenderLogLinesToWriter(dataLine, fmtFlag, isPty, &buf) //nolint
				if buf.Len() > 0 {
					hadContent = true
					ch <- Event{Kind: EvLogLine, Source: source, Lines: []string{buf.String()}}
				}
			}
			eventType = ""
			dataLine = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return hadContent, sc.Err()
}

// FetchBlobToChannel fetches a completed log blob and sends it as a single EvBlob event.
// endTs is the step's end timestamp in milliseconds, used to decide whether to retry on empty.
func FetchBlobToChannel(hc *http.Client, a *auth.ResolvedAuth, shortKey, source, fmtFlag string, isPty bool, ch chan<- Event, endTs int64) (bool, error) {
	var buf strings.Builder
	hasContent, err := FetchAndPrintLogWithRetry(hc, a, shortKey, fmtFlag, isPty, &buf, endTs)
	if err != nil || !hasContent {
		return hasContent, err
	}
	lines := strings.Split(buf.String(), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] += "\n"
	}
	ch <- Event{Kind: EvBlob, Source: source, Lines: lines}
	return true, nil
}

// ExecIdFromArg extracts the bare execId from args[0] which may be "pipelineId/execId" or just "execId".
func ExecIdFromArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	id := strings.TrimRight(args[0], "/")
	parts := strings.SplitN(id, "/", 4)
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[1]
	case 3, 4:
		return strings.TrimPrefix(parts[2], "-")
	}
	return ""
}

// FetchLogKeys fetches the execution graph for execId and returns all short log keys
// along with the pipeline's current status string.
func FetchLogKeys(hc *http.Client, a *auth.ResolvedAuth, execId string) ([]LogKeyEntry, string, error) {
	exec, err := execgraph.FetchExecutionFull(hc, a, execId)
	if err != nil {
		return nil, "", err
	}
	g := exec.Graph

	seenNode := make(map[string]bool)
	seenKey := make(map[string]bool)
	var entries []LogKeyEntry
	var walk func(id string)
	walk = func(id string) {
		if seenNode[id] {
			return
		}
		seenNode[id] = true
		node := g.NodeMap[id]
		if node.LogBaseKey != "" {
			lk := node.LogBaseKey
			if idx := strings.Index(lk, "pipeline/"); idx >= 0 {
				lk = lk[idx+len("pipeline/"):]
			}
			if !seenKey[lk] {
				seenKey[lk] = true
				entries = append(entries, LogKeyEntry{LogKey: lk, Name: execgraph.NodeName(node), Status: node.Status})
			}
		}
		for _, child := range g.NodeAdjacencyListMap[id].Children {
			walk(child)
		}
		for _, next := range g.NodeAdjacencyListMap[id].NextIDs {
			walk(next)
		}
	}
	if g.RootNodeID != "" {
		walk(g.RootNodeID)
	}
	return entries, exec.PipelineStatus, nil
}

// FollowLog implements --follow for a single log key.
func FollowLog(ctx *cmdctx.Ctx, hc *http.Client, shortKey string) error {
	a := ctx.Auth
	fmtFlag := ctx.FormatFlags.Format
	w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
	if err != nil {
		return err
	}
	defer closeW()

	parts := strings.SplitN(shortKey, "/", 4)
	var execId string
	if len(parts) >= 3 {
		execId = strings.TrimPrefix(parts[2], "-")
	} else if len(parts) == 2 {
		execId = parts[1]
	} else {
		execId = parts[0]
	}

	exec, err := execgraph.FetchExecutionFull(hc, a, execId)
	if err != nil {
		return err
	}

	node, nodeFound := FindNodeForFollow(exec.Graph, shortKey)

	source := shortKey
	if len(parts) >= 4 {
		source = parts[3]
	} else if len(parts) == 3 {
		source = strings.TrimPrefix(parts[2], "-")
	}

	fullLogKey := shortKey
	if !strings.Contains(fullLogKey, "/pipeline/") {
		fullLogKey = a.AccountID + "/pipeline/" + fullLogKey
	}

	ch := make(chan Event, 256)
	var finalStatus string

	sseCtx, cancelSSE := context.WithCancel(context.Background())
	defer cancelSSE()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		if IsTerminalStatus(exec.PipelineStatus) || (nodeFound && IsTerminalStatus(node.Status)) {
			finalStatus = exec.PipelineStatus
			if nodeFound && node.StartTs > 0 {
				ch <- Event{Kind: EvStart, Source: source, StartTs: node.StartTs}
			}
			FetchBlobToChannel(hc, a, shortKey, source, fmtFlag, ctx.IsPty, ch, node.EndTs) //nolint
			if nodeFound {
				ch <- Event{Kind: EvEnd, Source: source, Node: node}
			}
			return
		}

		startTs := time.Now().UnixMilli()
		if nodeFound && node.StartTs > 0 {
			startTs = node.StartTs
		}
		ch <- Event{Kind: EvStart, Source: source, StartTs: startTs}

		sseHadContent, _ := StreamSSEToChannel(sseCtx, hc, a, fullLogKey, source, fmtFlag, ctx.IsPty, ch)

		exec2, err2 := execgraph.FetchExecutionFull(hc, a, execId)
		if err2 == nil {
			finalStatus = exec2.PipelineStatus
			if node2, ok := FindNodeForFollow(exec2.Graph, shortKey); ok {
				if !sseHadContent {
					hlog.Debug("SSE had no content, falling back to blob", "key", shortKey)
					FetchBlobToChannel(hc, a, shortKey, source, fmtFlag, ctx.IsPty, ch, node2.EndTs) //nolint
				}
				ch <- Event{Kind: EvEnd, Source: source, Node: node2}
				return
			}
		}
		ch <- Event{Kind: EvEnd, Source: source, Node: execgraph.GraphNode{
			Status: "Success",
			EndTs:  time.Now().UnixMilli(),
		}}
	}()

	go func() {
		wg.Wait()
		close(ch)
	}()

	WriteEvents(ch, w)

	if format.ClassifyExecutionStatus(finalStatus) == format.StatusFailed {
		return fmt.Errorf("execution %s", strings.ToLower(finalStatus))
	}
	return nil
}

// FollowMulti follows all log keys for an execution (or a stage/step filtered subset).
// extraSkipTypes supplements BaseSkipStepTypes — pass nil to use the base set only.
func FollowMulti(ctx *cmdctx.Ctx, hc *http.Client, execId, stageFilter, stepFilter string, extraSkipTypes map[string]bool) error {
	a := ctx.Auth
	fmtFlag := ctx.FormatFlags.Format

	w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
	if err != nil {
		return err
	}
	defer closeW()

	skipTypes := BaseSkipStepTypes
	if len(extraSkipTypes) > 0 {
		merged := make(map[string]bool, len(BaseSkipStepTypes)+len(extraSkipTypes))
		for k, v := range BaseSkipStepTypes {
			merged[k] = v
		}
		for k, v := range extraSkipTypes {
			merged[k] = v
		}
		skipTypes = merged
	}

	ch := make(chan Event, 256)

	sseCtx, cancelSSE := context.WithCancel(context.Background())
	defer cancelSSE()

	nodeStarted := make(map[string]bool)
	var wg sync.WaitGroup
	var finalStatus string
	var hasSSE bool

	nodeMatchesFilter := func(logKey string) bool {
		if stageFilter == "" {
			return true
		}
		parts := strings.SplitN(logKey, "/", 6)
		if len(parts) < 4 || !strings.EqualFold(parts[3], stageFilter) {
			return false
		}
		if stepFilter != "" && (len(parts) < 5 || !strings.EqualFold(parts[4], stepFilter)) {
			return false
		}
		return true
	}

	sourceLabel := func(logKey string) string {
		parts := strings.SplitN(logKey, "/", 4)
		if len(parts) >= 4 {
			return parts[3]
		}
		if len(parts) == 3 {
			return strings.TrimPrefix(parts[2], "-")
		}
		return logKey
	}

	type nodeEntry struct {
		logKey string
		node   execgraph.GraphNode
		rank   int
	}

	pollOnce := func() (string, error) {
		exec, err := execgraph.FetchExecutionFull(hc, a, execId)
		if err != nil {
			return "", err
		}

		execgraph.AssignRanks(exec.Graph.RootNodeID, 1, exec.Graph.NodeMap, exec.Graph.NodeAdjacencyListMap)

		seenNode := make(map[string]bool)
		seenKey := make(map[string]bool)
		var newNodes []nodeEntry
		var walk func(id string)
		walk = func(id string) {
			if seenNode[id] {
				return
			}
			seenNode[id] = true
			node := exec.Graph.NodeMap[id]
			if node.LogBaseKey != "" && !skipTypes[node.StepType] {
				lk := node.LogBaseKey
				if _, after, ok := strings.Cut(lk, "pipeline/"); ok {
					lk = after
				}
				if !seenKey[lk] {
					seenKey[lk] = true
					if !nodeStarted[lk] && nodeMatchesFilter(lk) {
						bucket := format.ClassifyExecutionStatus(node.Status)
						if bucket == format.StatusRunning || bucket == format.StatusSuccess || bucket == format.StatusSkipped || bucket == format.StatusFailed {
							newNodes = append(newNodes, nodeEntry{logKey: lk, node: node, rank: node.Rank})
						}
					}
				}
			}
			for _, child := range exec.Graph.NodeAdjacencyListMap[id].Children {
				walk(child)
			}
			for _, next := range exec.Graph.NodeAdjacencyListMap[id].NextIDs {
				walk(next)
			}
		}
		if exec.Graph.RootNodeID != "" {
			walk(exec.Graph.RootNodeID)
		}

		hlog.Debug("pollOnce", "pipelineStatus", exec.PipelineStatus, "newNodes", len(newNodes))
		for _, e := range newNodes {
			age := time.Duration(-1)
			if e.node.EndTs > 0 {
				age = time.Since(time.UnixMilli(e.node.EndTs)).Truncate(time.Millisecond)
			}
			hlog.Debug("new node discovered", "key", e.logKey, "status", e.node.Status, "startTs", e.node.StartTs, "endTs", e.node.EndTs, "age", age, "rank", e.rank)
		}

		for i := 1; i < len(newNodes); i++ {
			for j := i; j > 0; j-- {
				a, b := newNodes[j-1], newNodes[j]
				aTs, bTs := a.node.StartTs, b.node.StartTs
				if aTs == 0 {
					aTs = 1<<62 - 1
				}
				if bTs == 0 {
					bTs = 1<<62 - 1
				}
				if aTs > bTs || (aTs == bTs && a.rank > b.rank) || (aTs == bTs && a.rank == b.rank && a.logKey > b.logKey) {
					newNodes[j-1], newNodes[j] = newNodes[j], newNodes[j-1]
				} else {
					break
				}
			}
		}

		type blobResult struct {
			ev  Event
			end Event
			err error
		}
		type pendingBlob struct {
			e      nodeEntry
			source string
			result chan blobResult
		}
		var blobs []pendingBlob
		for i := range newNodes {
			e := newNodes[i]
			nodeStarted[e.logKey] = true
			if !IsTerminalStatus(e.node.Status) {
				continue
			}
			source := sourceLabel(e.logKey)
			rc := make(chan blobResult, 1)
			blobs = append(blobs, pendingBlob{e: e, source: source, result: rc})
			go func() {
				var bev Event
				var buf strings.Builder
				hasContent, fetchErr := FetchAndPrintLogWithRetry(hc, a, e.logKey, fmtFlag, ctx.IsPty, &buf, e.node.EndTs)
				if fetchErr == nil && hasContent {
					lines := strings.Split(buf.String(), "\n")
					if len(lines) > 0 && lines[len(lines)-1] == "" {
						lines = lines[:len(lines)-1]
					}
					for i := range lines {
						lines[i] += "\n"
					}
					bev = Event{Kind: EvBlob, Source: source, Lines: lines}
				}
				rc <- blobResult{
					ev:  bev,
					end: Event{Kind: EvEnd, Source: source, Node: e.node},
					err: fetchErr,
				}
			}()
		}

		blobIdx := 0
		for i := range newNodes {
			e := newNodes[i]
			source := sourceLabel(e.logKey)
			fullKey := e.logKey
			if !strings.Contains(fullKey, "/pipeline/") {
				fullKey = a.AccountID + "/pipeline/" + fullKey
			}
			if format.ClassifyExecutionStatus(e.node.Status) != format.StatusSkipped {
				startTs := e.node.StartTs
				if startTs == 0 {
					startTs = time.Now().UnixMilli()
				}
				ch <- Event{Kind: EvStart, Source: source, StartTs: startTs}
			}

			if IsTerminalStatus(e.node.Status) {
				r := <-blobs[blobIdx].result
				blobIdx++
				if r.err != nil {
					fmt.Fprintf(os.Stderr, "error fetching log %s: %v\n", source, r.err)
				} else if len(r.ev.Lines) > 0 {
					ch <- r.ev
				}
				ch <- r.end
			} else {
				hasSSE = true
				lk, fk, src, nd := e.logKey, fullKey, source, e.node
				wg.Add(1)
				go func() {
					defer wg.Done()
					sseHadContent, _ := StreamSSEToChannel(sseCtx, hc, a, fk, src, fmtFlag, ctx.IsPty, ch)
					exec2, err2 := execgraph.FetchExecutionFull(hc, a, execId)
					if err2 == nil {
						if node2, ok := FindNodeForFollow(exec2.Graph, lk); ok {
							if !sseHadContent {
								hlog.Debug("SSE had no content, falling back to blob", "key", lk)
								FetchBlobToChannel(hc, a, lk, src, fmtFlag, ctx.IsPty, ch, node2.EndTs) //nolint
							}
							ch <- Event{Kind: EvEnd, Source: src, Node: node2}
							return
						}
					}
					ch <- Event{Kind: EvEnd, Source: src, Node: execgraph.GraphNode{
						Status: nd.Status,
						EndTs:  time.Now().UnixMilli(),
					}}
				}()
			}
		}

		return exec.PipelineStatus, nil
	}

	go func() {
		for {
			status, err := pollOnce()
			if err != nil {
				hlog.Debug("FollowMulti poll error", "err", err)
			} else {
				finalStatus = status
				if IsTerminalStatus(status) {
					break
				}
			}
			time.Sleep(2 * time.Second)
		}
		if hasSSE {
			time.Sleep(SseTerminalDrainDelay)
		}
		cancelSSE()
		wg.Wait()
		close(ch)
	}()

	WriteEvents(ch, w)

	if format.ClassifyExecutionStatus(finalStatus) == format.StatusFailed {
		return fmt.Errorf("execution %s", strings.ToLower(finalStatus))
	}
	return nil
}
