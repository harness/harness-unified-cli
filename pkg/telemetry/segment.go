// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/harness/harness-cli/pkg/hbase"
)

// segmentWriteKey is injected at build time via ldflags:
//
//	-X github.com/harness/harness-cli/pkg/telemetry.segmentWriteKey=<key>
var segmentWriteKey string

const (
	eventCommandExecuted = "cli_command_executed"
	eventCommandFailed   = "cli_command_failed"

	segmentTrackURL = "https://api.segment.io/v1/track"

	// requestTimeout bounds a single HTTP call. It's generous because a slow
	// call never blocks CLI exit — see [SegmentBackend.Close].
	requestTimeout = 5 * time.Second

	// closeGracePeriod is the max time [SegmentBackend.Close] will wait for
	// in-flight requests before giving up and letting the process exit.
	// Intent events fire before any API calls, so they usually have several
	// seconds to land before Close runs; this grace period mainly covers
	// error events fired right before exit.
	closeGracePeriod = 500 * time.Millisecond
)

// SegmentBackend implements [Backend] by posting directly to Segment's HTTP
// tracking API, sending each event on its own goroutine so callers never
// block on network I/O. Create with [NewSegmentBackend] and call
// [SegmentBackend.Close] (or defer it) before the process exits to give any
// in-flight requests a brief chance to land — see [closeGracePeriod].
type SegmentBackend struct {
	httpClient  *http.Client
	writeKey    string
	anonymousID string
	wg          sync.WaitGroup

	// disabled is set once a send fails for network reasons, so we stop
	// attempting further sends for the rest of this run.
	disabled atomic.Bool
}

// newSegmentBackend returns a SegmentBackend using the write key injected at
// build time. Returns nil when the key is empty (dev builds, CI without ldflags).
func newSegmentBackend(anonymousID string) *SegmentBackend {
	if segmentWriteKey == "" {
		return nil
	}
	return &SegmentBackend{
		httpClient:  &http.Client{Timeout: requestTimeout},
		writeKey:    segmentWriteKey,
		anonymousID: anonymousID,
	}
}

// Close waits up to [closeGracePeriod] for any in-flight events to finish
// sending, then returns unconditionally. Call via defer in main before
// process exit.
func (s *SegmentBackend) Close() {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeGracePeriod):
	}
}

// send POSTs a single Segment track event on its own goroutine and returns
// immediately; errors are dropped since telemetry is best-effort.
func (s *SegmentBackend) send(event string, properties map[string]any) {
	if s.disabled.Load() {
		return
	}

	body, err := json.Marshal(map[string]any{
		"writeKey":    s.writeKey,
		"anonymousId": s.anonymousID,
		"event":       event,
		"properties":  properties,
		"messageId":   uuid.NewString(),
		"context": map[string]any{
			"channel": "server",
			"ip":      "0.0.0.0", // Explicit opt-out to prevent auto-population
			"library": map[string]any{
				"name":    "harness-cli",
				"version": hbase.Version,
			},
		},
	})
	if err != nil {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, segmentTrackURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			s.disabled.Store(true)
			return
		}
		_ = resp.Body.Close()
	}()
}

// RecordIntent sends a "Command Run" track event.
func (s *SegmentBackend) RecordIntent(e CommandIntent) {
	s.send(eventCommandExecuted, map[string]any{
		"verb":        e.Verb,
		"noun":        e.Noun,
		"module":      e.Module,
		"flags_set":   e.FlagsSet,
		"account_id":  e.AccountID,
		"user_domain": e.UserDomain,
		"token_kind":  e.TokenKind,
		"auth_source": e.AuthSource,
		"run_id":      e.RunID,
		"os":          e.Env.OS,
		"arch":        e.Env.Arch,
		"version":     e.Env.Version,
		"is_tty":      e.Env.IsTTY,
		"is_pipeline": e.Env.IsPipelineExecution,
		"is_dev":      e.Env.IsDev,
		"aiagent":     e.Env.AIAgent,
		"locale":      e.Env.Locale,
	})
}

// RecordError sends a "Command Error" track event.
func (s *SegmentBackend) RecordError(e CommandError) {
	s.send(eventCommandFailed, map[string]any{
		"verb":        e.Verb,
		"noun":        e.Noun,
		"module":      e.Module,
		"account_id":  e.AccountID,
		"user_domain": e.UserDomain,
		"token_kind":  e.TokenKind,
		"auth_source": e.AuthSource,
		"run_id":      e.RunID,
		"category":    string(e.Category),
		"duration_ms": e.DurationMs,
		"os":          e.Env.OS,
		"arch":        e.Env.Arch,
		"version":     e.Env.Version,
		"is_dev":      e.Env.IsDev,
		"aiagent":     e.Env.AIAgent,
		"locale":      e.Env.Locale,
	})
}
