// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

// Package telemetry defines event shapes and helpers for CLI usage telemetry.
//
// Two event types are emitted per command invocation:
//
//   - [CommandIntent]: fired before execution captures command shape and
//     runtime environment — never user-supplied values.
//   - [CommandError]: fired only on failure, after CommandIntent, captures
//     an error category enum and elapsed time.
//
// Neither event records flag values, positional args, env var values, or
// any other user-supplied data. [FlagsSet] contains flag names only, and
// cobra's Visit function means only explicitly-set declared flags appear.
//
// Usage:
//
//	telemetry.SetBackend(myBackend)       // once at startup
//	env := telemetry.NewEnv()             // once at startup
//	telemetry.RecordIntent(CommandIntent{...})
//	if err != nil {
//	    telemetry.RecordError(CommandError{...})
//	}
package telemetry

import (
	"os"
	"runtime"
	"syscall"

	"golang.org/x/term"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/hbase"
)

// ErrorCategory is a coarse, enum-safe classification of a command failure.
// It must never contain user-supplied text.
type ErrorCategory string

const (
	ErrorCategoryAuth       ErrorCategory = "auth_error"
	ErrorCategoryAPI        ErrorCategory = "api_error"
	ErrorCategoryNotFound   ErrorCategory = "not_found"
	ErrorCategoryValidation ErrorCategory = "validation_error"
	ErrorCategoryTimeout    ErrorCategory = "timeout"
	ErrorCategoryUnknown    ErrorCategory = "unknown"
)

// Env captures static facts about the runtime environment. Call [NewEnv]
// once at startup and reuse the result across all events.
type Env struct {
	OS      string // runtime.GOOS
	Arch    string // runtime.GOARCH
	Version string // hbase.Version

	IsDev               bool
	IsTTY               bool // stdout is an interactive terminal
	IsPipelineExecution bool
	PipelineID          string // HARNESS_PIPELINEID; empty when IsPipelineExecution is false
}

// NewEnv captures the current runtime environment. Call once at startup.
func NewEnv() Env {
	pipelineID := os.Getenv(hbase.EnvPipelineID)
	return Env{
		OS:                  runtime.GOOS,
		Arch:                runtime.GOARCH,
		Version:             hbase.Version,
		IsDev:               hbase.IsDev(),
		IsTTY:               term.IsTerminal(int(syscall.Stdout)),
		IsPipelineExecution: pipelineID != "",
		PipelineID:          pipelineID,
	}
}

// CommandIntent is emitted once per invocation before the command executes.
// It records who is running what and which flags were explicitly set.
type CommandIntent struct {
	// Verb/Noun/Module describe the command shape, e.g. "execute"/"pipeline"/"pipeline".
	Verb   string
	Noun   string
	Module string

	// FlagsSet holds the names of flags the user explicitly passed.
	// Collected via cobra's cmd.Flags().Visit — only declared flags, never values.
	FlagsSet []string

	// AccountID from resolved auth. Empty for commands that skip auth.
	AccountID string

	Env Env
}

// CommandError is emitted when a command exits with an error, paired with
// a prior [CommandIntent] for the same invocation.
type CommandError struct {
	// Mirror of CommandIntent identity fields for correlation.
	Verb      string
	Noun      string
	Module    string
	AccountID string

	Category   ErrorCategory
	DurationMs int64

	Env Env
}

// Backend is implemented by telemetry sinks (Segment, debug-stdout, etc.).
type Backend interface {
	RecordIntent(e CommandIntent)
	RecordError(e CommandError)
}

var activeBackend Backend
var disabled bool

// SetBackend registers the active sink. Pass nil to disable. Call before
// any command executes.
func SetBackend(b Backend) {
	activeBackend = b
}

// SetDisabled sets the disabled flag from config.yaml's disable_telemetry field.
// Call once at startup after loading config.
func SetDisabled(v bool) {
	disabled = v
}

// RecordIntent emits a [CommandIntent]. No-op when no backend is set,
// HARNESS_NO_TELEMETRY=1, or the build is a dev build.
func RecordIntent(e CommandIntent) {
	if !shouldRecord(e.Env) {
		return
	}
	activeBackend.RecordIntent(e)
}

// RecordError emits a [CommandError]. Same gating as [RecordIntent].
func RecordError(e CommandError) {
	if !shouldRecord(e.Env) {
		return
	}
	activeBackend.RecordError(e)
}

// ClassifyError maps err to an [ErrorCategory] without inspecting any
// user-supplied message text. It relies on typed sentinel errors.
//
// Currently handles:
//   - [cmdctx.TimeoutError] → [ErrorCategoryTimeout]
//
// To classify API errors (401/403/404), the client package needs to expose
// a typed error carrying the HTTP status code. Until then those fall through
// to [ErrorCategoryUnknown].
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return ""
	}
	if cmdctx.IsTimeout(err) {
		return ErrorCategoryTimeout
	}
	return ErrorCategoryUnknown
}

func shouldRecord(env Env) bool {
	if activeBackend == nil {
		return false
	}
	if env.IsDev {
		return false
	}
	if disabled {
		return false
	}
	if os.Getenv(hbase.EnvNoTelemetry) == "1" {
		return false
	}
	return true
}
