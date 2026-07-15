// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/harness/cli/pkg/execgraph"
	"github.com/harness/cli/pkg/format"
	"github.com/harness/cli/pkg/tui"
)

type rightTab int

const (
	tabLogs rightTab = iota
	tabDetails
	tabInputs
	tabOutputs
)

type tabDef struct {
	label string
	key   string
	tab   rightTab
}

var tabDefs = []tabDef{
	{"Logs", "l", tabLogs},
	{"Details", "d", tabDetails},
	{"Inputs", "i", tabInputs},
	{"Outputs", "o", tabOutputs},
}

func (m logViewModel) rightPanelWidth() int {
	w := m.width - leftPanelWidth - 1
	if w < 0 {
		return 0
	}
	return w
}

func (m logViewModel) renderRightPanel() string {
	var b strings.Builder
	b.WriteString(m.renderTabBar() + "\n")
	b.WriteString(m.st.divider.Render(strings.Repeat("─", m.rightPanelWidth())) + "\n")
	switch m.activeTab {
	case tabLogs:
		b.WriteString(m.renderTabLogs())
	case tabDetails:
		b.WriteString(m.renderTabDetails())
	case tabInputs:
		b.WriteString(m.renderTabInputs())
	case tabOutputs:
		b.WriteString(m.renderTabOutputs())
	}
	return b.String()
}

func (m logViewModel) renderTabBar() string {
	st := m.st
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLITextMuted))
	tabs := make([]string, len(tabDefs))
	for i, td := range tabDefs {
		hotkey := keyStyle.Render("(" + td.key + ")")
		if td.tab == m.activeTab {
			tabs[i] = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(tui.CLIAccent)).Render(td.label) + " " + hotkey
		} else {
			tabs[i] = st.dim.Render(td.label) + " " + hotkey
		}
	}
	return strings.Join(tabs, st.divider.Render("  ·  "))
}

func (m logViewModel) renderTabLogs() string {
	if m.loading {
		return m.spin.View() + " loading…"
	}
	return m.vp.View()
}

func (m logViewModel) renderTabDetails() string {
	return m.vp.View()
}

func (m logViewModel) renderTabInputs() string {
	return m.vp.View()
}

func (m logViewModel) renderTabOutputs() string {
	return m.vp.View()
}

func (m *logViewModel) renderDetailsContent(node *execgraph.GraphNode) string {
	st := m.st
	if node == nil {
		return st.dim.Render("(no step selected)")
	}

	label := func(s string) string {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(tui.CLIText)).Render(s)
	}
	val := func(s string) string { return st.normal.Render(s) }
	dim := func(s string) string { return st.dim.Render(s) }

	fmtTs := func(ms int64) string {
		if ms == 0 {
			return dim("—")
		}
		return val(time.UnixMilli(ms).Local().Format("1/2/2006, 3:04:05 PM"))
	}

	var b strings.Builder
	b.WriteString(label("FQN:       ") + "  " + val(node.BaseFQN) + "\n")
	ss := format.BucketStyles[format.ClassifyExecutionStatus(node.Status)]
	statusVal := lipgloss.NewStyle().Foreground(lipgloss.Color(ss.LipglossColor)).Render(node.Status)
	b.WriteString(label("Status:    ") + "  " + statusVal + "\n")
	b.WriteString(label("Started at:") + "  " + fmtTs(node.StartTs) + "\n")
	b.WriteString(label("Ended at:  ") + "  " + fmtTs(node.EndTs) + "\n")

	dur := dim("—")
	if node.StartTs > 0 && node.EndTs > node.StartTs {
		d := time.Duration(node.EndTs-node.StartTs) * time.Millisecond
		dur = val(d.Round(time.Second).String())
	}
	b.WriteString(label("Duration:  ") + "  " + dur + "\n")

	// Parse timeout from stepParameters JSON.
	timeout := dim("—")
	if len(node.StepParameters) > 0 {
		var params map[string]any
		if err := json.Unmarshal(node.StepParameters, &params); err == nil {
			if t, ok := params["timeout"]; ok {
				timeout = val(fmt.Sprintf("%v", t))
			}
		}
	}
	b.WriteString(label("Timeout:   ") + "  " + timeout + "\n")

	if len(node.DelegateInfoList) > 0 {
		b.WriteString("\n" + label("Delegates:") + "\n")
		for _, d := range node.DelegateInfoList {
			if d.Name != "" {
				b.WriteString("  " + val(d.Name) + "\n")
			}
		}
	}

	return b.String()
}

func prettyJSON(raw string, st lvStyles) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return st.dim.Render(raw)
	}
	return buf.String()
}

// syncViewportForTab updates the viewport content for non-log tabs.
// Call this whenever the active tab or selected step changes.
func (m *logViewModel) syncViewportForTab() {
	if m.activeTab == tabLogs {
		return
	}
	st := m.st
	node := m.selectedNode()

	switch m.activeTab {
	case tabDetails:
		m.vp.SetContent(m.renderDetailsContent(node))
	case tabInputs:
		if node == nil || len(node.StepParameters) == 0 {
			m.vp.SetContent(st.dim.Render("(no inputs)"))
		} else {
			m.vp.SetContent(prettyJSON(string(node.StepParameters), st))
		}
	case tabOutputs:
		if node == nil || len(node.Outcomes) == 0 {
			m.vp.SetContent(st.dim.Render("(no outputs)"))
		} else {
			if b, err := json.Marshal(node.Outcomes); err == nil {
				m.vp.SetContent(prettyJSON(string(b), st))
			}
		}
	}
	m.vp.GotoTop()
}
