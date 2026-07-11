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
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/config"
	"github.com/harness/harness-cli/pkg/hbase"
	"github.com/harness/harness-cli/pkg/hlog"
)

// ErrorCategory is a coarse, enum-safe classification of a command failure.
// It must never contain user-supplied text.
type ErrorCategory string

const (
	ErrorCategoryAuth        ErrorCategory = "auth_error"
	ErrorCategoryAPI         ErrorCategory = "api_error"
	ErrorCategoryNotFound    ErrorCategory = "not_found"
	ErrorCategoryValidation  ErrorCategory = "validation_error"
	ErrorCategoryInvalidVerb ErrorCategory = "invalid_verb"
	ErrorCategoryInvalidNoun ErrorCategory = "invalid_noun"
	ErrorCategoryInvalidFlag ErrorCategory = "invalid_flag"
	ErrorCategoryBadUsage    ErrorCategory = "bad_usage" // fallback when more specific bucket can't be determined
	ErrorCategoryTimeout     ErrorCategory = "timeout"
	ErrorCategoryUnknown     ErrorCategory = "unknown"
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

	// AIAgent is a standardized identifier for the coding agent the CLI is
	// running under (e.g. "claude-code", "cursor"), or "" if none is detected.
	// See [DetectAgent].
	AIAgent string

	Locale string // from LANG/LC_ALL/LC_CTYPE, e.g. "en_US.UTF-8"
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
		AIAgent:             DetectAgent(),
		Locale:              locale(),
	}
}

// localePattern matches a bare POSIX/BCP-47-ish locale with the
// encoding/modifier suffix already stripped: a 2-3 letter language code
// optionally followed by a territory, e.g. "en", "en_US", "zh_Hans_CN".
// The special POSIX locales "C" and "POSIX" are also accepted.
var localePattern = regexp.MustCompile(`^(?:[a-z]{2,3}(?:_[A-Za-z]{2,4}){0,2}|C|POSIX)$`)

// locale returns the first non-empty of LC_ALL, LC_CTYPE, LANG — the
// standard POSIX precedence order for locale resolution — with the
// encoding/modifier suffix stripped (e.g. "en_US.UTF-8" -> "en_US").
// Returns "" if the result doesn't look like a valid locale, since these
// env vars are user-controlled and can contain arbitrary garbage.
func locale() string {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		if i := strings.IndexAny(v, ".@"); i >= 0 {
			v = v[:i]
		}
		if localePattern.MatchString(v) {
			return v
		}
		return ""
	}
	return ""
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

	// UserDomain is the domain portion of the user's profile email (e.g. "harness.io").
	// Never the full email address.
	UserDomain string

	// TokenKind is the type of credential in use: "pat", "sat", "jwt", or "".
	TokenKind string

	// AuthSource is "profile" when auth came from config file, "env" when from env vars.
	AuthSource string

	// RunID correlates all API calls from this invocation. Mirrors hbase.RunID.
	RunID string

	Env Env
}

// CommandError is emitted when a command exits with an error, paired with
// a prior [CommandIntent] for the same invocation.
type CommandError struct {
	// Mirror of CommandIntent identity fields for correlation.
	Verb       string
	Noun       string
	Module     string
	AccountID  string
	UserDomain string
	TokenKind  string
	AuthSource string
	RunID      string

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

// Init sets up the telemetry backend from config and returns a flush function
// to defer in main. Safe to call unconditionally — no-ops when telemetry is
// disabled or no write key is present.
func Init() (flush func()) {
	cfg, err := config.LoadConfig()
	if err != nil || cfg.DisableTelemetry {
		SetDisabled(true)
		return func() {}
	}
	seg := newSegmentBackend(config.GetOrCreateTelemetryID())
	if seg == nil {
		return func() {}
	}
	SetBackend(seg)
	return func() { seg.Close() }
}

// RecordIntent emits a [CommandIntent]. No-op when the user has opted out
// (disable_telemetry or HARNESS_NO_TELEMETRY=1) — in that case it returns
// before even logging the debug line. When opted in but no backend is set
// (dev build, no write key), it still logs for debugging but sends nothing.
func RecordIntent(e CommandIntent) {
	if Disabled() {
		return
	}
	hlog.Debug("telemetry: intent",
		"verb", e.Verb, "noun", e.Noun, "module", e.Module,
		"flags", e.FlagsSet, "account", e.AccountID, "domain", e.UserDomain,
		"token_kind", e.TokenKind, "auth_source", e.AuthSource,
		"run_id", e.RunID, "os", e.Env.OS, "arch", e.Env.Arch,
		"version", e.Env.Version, "is_tty", e.Env.IsTTY,
		"is_pipeline", e.Env.IsPipelineExecution,
		"aiagent", e.Env.AIAgent, "locale", e.Env.Locale,
		"backend", activeBackend != nil)
	if activeBackend == nil {
		return
	}
	activeBackend.RecordIntent(e)
}

// RecordError emits a [CommandError]. Same gating as [RecordIntent].
func RecordError(e CommandError) {
	if Disabled() {
		return
	}
	hlog.Debug("telemetry: error",
		"verb", e.Verb, "noun", e.Noun, "module", e.Module,
		"category", e.Category, "duration_ms", e.DurationMs,
		"account", e.AccountID, "token_kind", e.TokenKind,
		"auth_source", e.AuthSource, "run_id", e.RunID,
		"backend", activeBackend != nil)
	if activeBackend == nil {
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

// UserDomainFromEmail extracts the domain portion of an email address (e.g. "harness.io").
// Returns empty string if email is empty or malformed.
func UserDomainFromEmail(email string) string {
	if i := strings.LastIndex(email, "@"); i >= 0 {
		return email[i+1:]
	}
	return ""
}

// Disabled reports whether the user has opted out of telemetry, via either
// disable_telemetry in config.yaml or HARNESS_NO_TELEMETRY=1. Distinct from
// having no active backend (e.g. a dev build with no write key) — that case
// still logs debug output, whereas an explicit opt-out logs nothing.
func Disabled() bool {
	if disabled {
		return true
	}
	if os.Getenv(hbase.EnvNoTelemetry) == "1" {
		return true
	}
	return false
}
