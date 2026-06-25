// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/harness/harness-cli/pkg/console"
)

// Column defines a table column header and its display width.
type Column struct {
	Header string
	Width  int
}

// Row is a slice of cell strings, one per column.
type Row []string

// TableModel is a bubbletea-compatible table with a custom renderer.
// It owns cursor/scroll state and supports colored cell values.
type TableModel struct {
	columns []Column
	rows    []Row

	cursor int
	scroll int
	height int // visible data rows (excludes header)
	width  int

	// Styles
	headerStyle lipgloss.Style
	cursorStyle lipgloss.Style
	cellStyle   lipgloss.Style
	gutterStyle lipgloss.Style
}

// GutterWidth is the reserved left gutter: "▶ " or "  ".
const GutterWidth = 2

// NewTable creates a TableModel with sensible defaults.
func NewTable(columns []Column, height, width int) TableModel {
	return TableModel{
		columns:     columns,
		height:      height,
		width:       width,
		headerStyle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(CLIAccent)),
		cursorStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(CLIAccent)).Bold(true),
		cellStyle:   lipgloss.NewStyle(),
		gutterStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(CLIAccent)).Bold(true),
	}
}

func (t *TableModel) SetColumns(cols []Column) {
	t.columns = cols
}

func (t *TableModel) SetRows(rows []Row) {
	t.rows = rows
	if t.cursor >= len(rows) {
		t.cursor = max(len(rows)-1, 0)
	}
	t.clampScroll()
}

func (t *TableModel) SetHeight(h int) {
	t.height = h
	t.clampScroll()
}

func (t *TableModel) SetWidth(w int) {
	t.width = w
}

func (t *TableModel) GotoTop() {
	t.cursor = 0
	t.scroll = 0
}

func (t *TableModel) Cursor() int {
	return t.cursor
}

func (t *TableModel) Rows() []Row {
	return t.rows
}

func (t TableModel) Update(msg tea.Msg) (TableModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if t.cursor > 0 {
				t.cursor--
				if t.cursor < t.scroll {
					t.scroll = t.cursor
				}
			}
		case "down", "j":
			if t.cursor < len(t.rows)-1 {
				t.cursor++
				if t.cursor >= t.scroll+t.height {
					t.scroll = t.cursor - t.height + 1
				}
			}
		}
	}
	return t, nil
}

// HeaderView returns just the header row (same styling as View).
func (t TableModel) HeaderView() string {
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", GutterWidth))
	for _, col := range t.columns {
		cell := padOrTruncate(col.Header, col.Width)
		b.WriteString(t.headerStyle.Render(cell))
	}
	b.WriteString("\n")
	return b.String()
}

func (t TableModel) View() string {
	var b strings.Builder

	// Header
	b.WriteString(strings.Repeat(" ", GutterWidth))
	for _, col := range t.columns {
		cell := padOrTruncate(col.Header, col.Width)
		b.WriteString(t.headerStyle.Render(cell))
	}
	b.WriteString("\n")

	// Data rows
	end := t.scroll + t.height
	if end > len(t.rows) {
		end = len(t.rows)
	}
	for i := t.scroll; i < end; i++ {
		selected := i == t.cursor
		if selected {
			b.WriteString(t.gutterStyle.Render("▶ "))
		} else {
			b.WriteString("  ")
		}
		for j, col := range t.columns {
			cell := ""
			if j < len(t.rows[i]) {
				cell = t.rows[i][j]
			}
			cell = truncateCell(cell, col.Width)
			cell = padRight(cell, col.Width)
			if selected && isPlain(cell) {
				cell = t.cursorStyle.Render(cell)
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// isPlain returns true if s contains no ANSI escape sequences.
func isPlain(s string) bool {
	return console.StripANSI(s) == s
}

// truncateCell clips s to width visible runes, appending "…" when truncated.
// Width here is the column display width including padding.
func truncateCell(s string, width int) string {
	// leave 1 char of right-pad (the padding is applied after)
	limit := width - 1
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	// measure visible length (strip ANSI for counting)
	visible := []rune(console.StripANSI(s))
	if len(visible) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	// for plain strings just slice runes directly
	if len(runes) == len(visible) {
		return string(runes[:limit-1]) + "…"
	}
	// colored strings: truncate visible chars, keep ANSI intact
	return truncateANSI(s, limit-1) + "…"
}

// padRight right-pads s with spaces to exactly width visible chars.
func padRight(s string, width int) string {
	visible := []rune(console.StripANSI(s))
	if pad := width - len(visible); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// TruncateANSI keeps the first n visible runes of s, preserving ANSI codes.
func TruncateANSI(s string, n int) string { return truncateANSI(s, n) }

// truncateANSI keeps the first n visible runes of s, preserving ANSI codes.
func truncateANSI(s string, n int) string {
	var out strings.Builder
	count := 0
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
			// consume the full escape sequence
			j := i + 2
			for j < len(runes) && runes[j] != 'm' {
				j++
			}
			if j < len(runes) {
				j++
			}
			out.WriteString(string(runes[i:j]))
			i = j
		} else {
			if count >= n {
				break
			}
			if !unicode.Is(unicode.Mn, runes[i]) {
				count++
			}
			out.WriteRune(runes[i])
			i++
		}
	}
	return out.String()
}

// padOrTruncate fits s to exactly width runes (padding or truncating).
func padOrTruncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) > width {
		if width <= 1 {
			return "…"
		}
		return string(runes[:width-1]) + "…"
	}
	if pad := width - len(runes); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func (t *TableModel) clampScroll() {
	if t.cursor < t.scroll {
		t.scroll = t.cursor
	}
	if t.cursor >= t.scroll+t.height {
		t.scroll = t.cursor - t.height + 1
	}
	if t.scroll < 0 {
		t.scroll = 0
	}
}
