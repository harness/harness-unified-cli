// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"time"

	analytics "github.com/segmentio/analytics-go/v3"
)

// segmentWriteKey is injected at build time via ldflags:
//
//	-X github.com/harness/harness-cli/pkg/telemetry.segmentWriteKey=<key>
var segmentWriteKey string

const (
	eventCommandExecuted = "cli_command_executed"
	eventCommandFailed   = "cli_command_failed"
)

// SegmentBackend implements [Backend] using the Segment analytics SDK.
// Create with [NewSegmentBackend] and call [SegmentBackend.Close] (or defer it)
// before the process exits so queued events are flushed.
type SegmentBackend struct {
	client      analytics.Client
	anonymousID string
}

// newSegmentBackend returns a SegmentBackend using the write key injected at
// build time. Returns nil when the key is empty (dev builds, CI without ldflags).
func newSegmentBackend(anonymousID string) *SegmentBackend {
	if segmentWriteKey == "" {
		return nil
	}
	// BatchSize 1 makes the client's background sender dispatch each event
	// as soon as it's enqueued, instead of waiting for Interval/Close to
	// flush a batch — a CLI invocation only ever emits 1-2 events and exits
	// long before the default interval would fire. Enqueue itself still
	// just pushes onto a channel, so callers never block on this.
	client, err := analytics.NewWithConfig(segmentWriteKey, analytics.Config{
		BatchSize: 1,
		Interval:  50 * time.Millisecond,
	})
	if err != nil {
		return nil
	}
	return &SegmentBackend{
		client:      client,
		anonymousID: anonymousID,
	}
}

// Close flushes any queued events and closes the underlying Segment client.
// Call via defer in main before process exit.
func (s *SegmentBackend) Close() {
	_ = s.client.Close()
}

// RecordIntent sends a "Command Run" track event.
func (s *SegmentBackend) RecordIntent(e CommandIntent) {
	_ = s.client.Enqueue(analytics.Track{
		AnonymousId: s.anonymousID,
		Event:       eventCommandExecuted,
		Properties: analytics.NewProperties().
			Set("verb", e.Verb).
			Set("noun", e.Noun).
			Set("module", e.Module).
			Set("flags_set", e.FlagsSet).
			Set("account_id", e.AccountID).
			Set("user_domain", e.UserDomain).
			Set("token_kind", e.TokenKind).
			Set("auth_source", e.AuthSource).
			Set("run_id", e.RunID).
			Set("os", e.Env.OS).
			Set("arch", e.Env.Arch).
			Set("version", e.Env.Version).
			Set("is_tty", e.Env.IsTTY).
			Set("is_pipeline", e.Env.IsPipelineExecution).
			Set("is_dev", e.Env.IsDev).
			Set("aiagent", e.Env.AIAgent).
			Set("locale", e.Env.Locale),
	})
}

// RecordError sends a "Command Error" track event.
func (s *SegmentBackend) RecordError(e CommandError) {
	_ = s.client.Enqueue(analytics.Track{
		AnonymousId: s.anonymousID,
		Event:       eventCommandFailed,
		Properties: analytics.NewProperties().
			Set("verb", e.Verb).
			Set("noun", e.Noun).
			Set("module", e.Module).
			Set("account_id", e.AccountID).
			Set("user_domain", e.UserDomain).
			Set("token_kind", e.TokenKind).
			Set("auth_source", e.AuthSource).
			Set("run_id", e.RunID).
			Set("category", string(e.Category)).
			Set("duration_ms", e.DurationMs).
			Set("os", e.Env.OS).
			Set("arch", e.Env.Arch).
			Set("version", e.Env.Version).
			Set("is_dev", e.Env.IsDev).
			Set("aiagent", e.Env.AIAgent).
			Set("locale", e.Env.Locale),
	})
}
