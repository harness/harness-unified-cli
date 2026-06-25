// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package hlog

import (
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/lmittmann/tint"
	"golang.org/x/term"
)

// logState holds all settings that influence how the logger is built.
// reinit() reads this and rebuilds the active logger from scratch.
type logState struct {
	file       *os.File  // nil means stderr
	level      slog.Level
	pluginName string
}

var state logState
var active atomic.Pointer[slog.Logger]

func init() {
	active.Store(slog.New(slog.DiscardHandler))
}

func reinit() {
	w := state.file
	if w == nil {
		w = os.Stderr
	}

	var h slog.Handler
	if state.file == nil && term.IsTerminal(int(w.Fd())) {
		h = tint.NewHandler(w, &tint.Options{
			Level:      state.level,
			TimeFormat: "15:04:05",
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Value.Kind() == slog.KindAny {
					if _, ok := a.Value.Any().(error); ok {
						return tint.Attr(9, a)
					}
				}
				return a
			},
		})
	} else {
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: state.level})
	}

	l := slog.New(h)
	if state.pluginName != "" {
		l = l.With("plugin", state.pluginName)
	}
	active.Store(l)
}

// SetDebug switches the logger to DEBUG level. If no log file is configured, output goes to stderr.
func SetDebug() {
	state.level = slog.LevelDebug
	reinit()
}

// SetDebugFile opens path for append and switches the logger to DEBUG level writing to that file.
// If the file cannot be opened, the logger is left as-is.
func SetDebugFile(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	state.level = slog.LevelDebug
	state.file = f
	reinit()
}

// SetLogFile redirects log output (at INFO level) to path. Used when HARNESS_CLI_LOGFILE is set.
// If the file cannot be opened, the logger is left as-is.
func SetLogFile(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	state.file = f
	reinit()
}

// SetPlugin records the plugin name so it appears on every subsequent log line.
func SetPlugin(name string) {
	state.pluginName = name
	reinit()
}

// SilenceForTUI suppresses log output while a Bubble Tea program owns the
// terminal. Returns the previous logger so RestoreAfterTUI can swap it back.
// When logging to a file there is no stderr conflict, so this is a no-op.
func SilenceForTUI() *slog.Logger {
	if state.file != nil {
		return active.Load()
	}
	return active.Swap(slog.New(slog.DiscardHandler))
}

// RestoreAfterTUI restores the logger saved by SilenceForTUI.
func RestoreAfterTUI(prev *slog.Logger) {
	if state.file != nil {
		return
	}
	active.Store(prev)
}

func Debug(msg string, args ...any) { active.Load().Debug(msg, args...) }
func Info(msg string, args ...any)  { active.Load().Info(msg, args...) }
func Warn(msg string, args ...any)  { active.Load().Warn(msg, args...) }
func Error(msg string, args ...any) { active.Load().Error(msg, args...) }
