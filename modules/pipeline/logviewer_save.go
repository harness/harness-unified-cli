// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/tui"
)

// doSaveLog writes the current step's log to m.saveInput.
// append=true opens the file in append mode; false truncates/creates.
func (m *logViewModel) doSaveLog(appendMode bool) {
	node := m.selectedNode()
	logKey := ""
	if node != nil {
		logKey = node.LogBaseKey
	}
	content := console.StripANSI(m.logCache[logKey])
	flag := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(m.saveInput, flag, 0o644)
	if err != nil {
		m.saveStatus = "error: " + err.Error()
		return
	}
	_, err = f.WriteString(content)
	f.Close()
	if err != nil {
		m.saveStatus = "error: " + err.Error()
		return
	}
	m.saveStatus = "saved to " + m.saveInput
	m.saveDone = true
}

// overlayCenter composites the modal box over the background by replacing lines
// at the vertical/horizontal center without blanking the rest of the screen.
func overlayCenter(background, box string, w, h int) string {
	bgLines := strings.Split(background, "\n")
	boxLines := strings.Split(box, "\n")

	boxH := len(boxLines)
	boxW := 0
	for _, l := range boxLines {
		if lw := lipgloss.Width(l); lw > boxW {
			boxW = lw
		}
	}

	startRow := (h - boxH) / 2
	startCol := (w - boxW) / 2
	if startCol < 0 {
		startCol = 0
	}

	out := make([]string, len(bgLines))
	copy(out, bgLines)

	for i, boxLine := range boxLines {
		row := startRow + i
		if row < 0 || row >= len(out) {
			continue
		}
		bg := out[row]
		runes := []rune(bg)
		prefix := ""
		if startCol <= len(runes) {
			prefix = string(runes[:startCol])
		} else {
			prefix = bg + strings.Repeat(" ", startCol-len(runes))
		}
		out[row] = prefix + boxLine
	}

	return strings.Join(out, "\n")
}

func (m logViewModel) renderSaveModal() string {
	st := m.st
	var body string

	if m.saveConfirm {
		prompt := st.normal.Render("File already exists:") + " " + st.header.Render(m.saveInput) + "\n\n" +
			st.normal.Render("Overwrite?") + "\n\n" +
			lipgloss.NewStyle().Bold(true).Render("y") + st.dim.Render(" yes  ") +
			lipgloss.NewStyle().Bold(true).Render("a") + st.dim.Render(" append  ") +
			lipgloss.NewStyle().Bold(true).Render("n") + st.dim.Render("/") +
			lipgloss.NewStyle().Bold(true).Render("esc") + st.dim.Render(" cancel")
		body = prompt
	} else {
		title := "Save log to file"
		inputLine := "> " + m.saveInput + "█"
		var statusLine string
		if m.saveStatus != "" {
			if m.saveDone {
				statusLine = "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLISuccess)).Render(m.saveStatus) +
					st.dim.Render("  (enter/esc to close)")
			} else {
				statusLine = "\n" + st.errStyle.Render(m.saveStatus)
			}
		}
		hint := "\n" + st.dim.Render("enter to save · esc to cancel")
		body = st.normal.Render(title) + "\n\n" + inputLine + statusLine + hint
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(tui.CLIAccent)).
		Padding(1, 3).
		Width(50).
		Render(body)
}
