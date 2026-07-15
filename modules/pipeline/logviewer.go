// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/harness/cli/pkg/cmdctx"
	"github.com/harness/cli/pkg/execgraph"
	"github.com/harness/cli/pkg/format"
	"github.com/harness/cli/pkg/logstream"
	"github.com/harness/cli/pkg/tui"
)

const lvPollIntervalSecs = 2

// --- messages ---

type lvStepsLoadedMsg struct {
	steps          []execgraph.GraphNode
	pipelineStatus string
	err            error
}

type lvLogLoadedMsg struct {
	nodeUUID string
	content  *logContent
	err      error
}

type lvCountdownTickMsg struct{}
type lvPollMsg struct{}

type lvLogStreamLineMsg struct {
	nodeUUID string
	unitIdx  int
	lines    []string
}

type lvLogStreamDoneMsg struct {
	nodeUUID string
	unitIdx  int // which unit just finished; -1 means all done
}

// --- styles ---

type lvStyles struct {
	header     lipgloss.Style
	selected   lipgloss.Style
	normal     lipgloss.Style
	dim        lipgloss.Style
	border     lipgloss.Style
	errStyle   lipgloss.Style
	scrollHint lipgloss.Style
	divider    lipgloss.Style
}

func newLVStyles() lvStyles {
	return lvStyles{
		header:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(tui.CLIAccent)).Padding(0, 1),
		selected:   lipgloss.NewStyle().Background(lipgloss.Color(tui.CLIBgElevated)).Foreground(lipgloss.Color(tui.CLIAccent)).Bold(true),
		normal:     lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLIText)),
		dim:        lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLITextMuted)),
		border:     lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false).BorderForeground(lipgloss.Color(tui.CLIBorder)),
		errStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLIError)),
		scrollHint: lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLITextMuted)),
		divider:    lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLIBorder)),
	}
}

// --- log content ---

// logContent accumulates per-unit log text and caches the last rendered string.
// Only re-concatenates when a unit has been appended to since the last render.
type logContent struct {
	units []string // one entry per log unit (indexed same as LogUnits)
	names []string // unit label for each entry
	dirty bool
	cache string
}

func newLogContent(units []execgraph.LogUnit) *logContent {
	lc := &logContent{
		units: make([]string, len(units)),
		names: make([]string, len(units)),
		dirty: true,
	}
	for i, u := range units {
		lc.names[i] = u.Unit
	}
	return lc
}

func (lc *logContent) append(unitIdx int, text string) {
	lc.units[unitIdx] += text
	lc.dirty = true
}

func (lc *logContent) rendered() string {
	if len(lc.units) == 1 {
		return lc.units[0]
	}
	if !lc.dirty {
		return lc.cache
	}
	var b strings.Builder
	for i, text := range lc.units {
		if i > 0 || text != "" {
			fmt.Fprintf(&b, "──── %s ────\n", lc.names[i])
		}
		b.WriteString(text)
	}
	lc.cache = b.String()
	lc.dirty = false
	return lc.cache
}

// --- SSE stream handle ---

type sseStream struct {
	cancel  context.CancelFunc
	ch      chan logstream.Event
	unitIdx int // which unit is currently streaming
}

// --- model ---

type lvState int

const (
	lvStateLoading lvState = iota
	lvStateReady
	lvStateError
)

type logViewModel struct {
	st     lvStyles
	state  lvState
	errMsg string

	execLabel string // e.g. "sawka_test2 / 2QPmypuy..."
	steps     []execgraph.GraphNode
	// selectedUUID is the UUID of the highlighted step; stable across polls.
	selectedUUID    string
	logCache        map[string]*logContent // nodeUUID → accumulated log content
	activeStreams   map[string]sseStream   // nodeUUID → live SSE stream (running steps only)
	leftPanelW      int
	leftPanelOffset int
	pipelineDone    bool
	pollCountdown   int  // seconds remaining until next poll (counts down from lvPollIntervalSecs)
	pollRefreshing  bool // poll fetch currently in flight

	spin    spinner.Model
	vp      viewport.Model
	vpReady bool
	loading bool // log fetch in flight

	ctx *cmdctx.Ctx

	width  int
	height int

	activeTab rightTab

	saveModal   bool
	saveInput   string
	saveStatus  string // "" = typing, error message, or "saved to <file>"
	saveDone    bool   // true after successful write, waiting for dismiss
	saveConfirm bool   // true when prompting y/n/a overwrite confirmation
}

const leftPanelWidth = 32

func (m *logViewModel) clampLeftOffset() {
	maxOffset := m.leftPanelW - leftPanelWidth
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.leftPanelOffset > maxOffset {
		m.leftPanelOffset = maxOffset
	}
	if m.leftPanelOffset < 0 {
		m.leftPanelOffset = 0
	}
}

func calcLeftPanelWidth(steps []execgraph.GraphNode, activeStreams map[string]sseStream) int {
	w := 0
	for _, s := range steps {
		n := s.Depth + 3 + len(execgraph.NodeName(s))
		if _, streaming := activeStreams[s.UUID]; streaming {
			n += 2
		}
		if n > w {
			w = n
		}
	}
	return w
}

func newLogViewModel(execLabel string, ctx *cmdctx.Ctx) logViewModel {
	st := newLVStyles()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = st.header

	vp := viewport.New(viewport.WithWidth(60), viewport.WithHeight(20))
	vp.SoftWrap = true

	return logViewModel{
		st:            st,
		state:         lvStateLoading,
		execLabel:     execLabel,
		logCache:      make(map[string]*logContent),
		activeStreams: make(map[string]sseStream),
		spin:          sp,
		vp:            vp,
		ctx:           ctx,
		width:         80,
		height:        24,
	}
}

func (m logViewModel) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return m.spin.Tick() },
		m.loadSteps(),
	)
}

func (m logViewModel) loadSteps() tea.Cmd {
	ctx := m.ctx
	execID := bareExecID(m.execLabel)
	return func() tea.Msg {
		exec, err := execgraph.FetchExecutionFull(ctx, execID)
		if err != nil {
			return lvStepsLoadedMsg{err: err}
		}
		steps := execgraph.WalkNodes(exec.Graph, skipStepTypes)
		return lvStepsLoadedMsg{steps: steps, pipelineStatus: exec.PipelineStatus}
	}
}

// countdownTick returns a Cmd that waits 1s then fires lvCountdownTickMsg.
func countdownTick() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(1 * time.Second)
		return lvCountdownTickMsg{}
	}
}

// bareExecID extracts the execution ID from the label "pipeline / execId" or just "execId".
func bareExecID(label string) string {
	parts := strings.SplitN(label, " / ", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return label
}

func (m logViewModel) fetchLog(node execgraph.GraphNode) tea.Cmd {
	hc := &http.Client{Timeout: 30 * time.Second}
	a := m.ctx.Auth
	units := execgraph.GetLogUnits(node)
	nodeUUID := node.UUID
	fmtFlag := m.ctx.FormatFlags.Format
	isPty := m.ctx.IsPty
	return func() tea.Msg {
		if len(units) == 0 {
			return lvLogLoadedMsg{nodeUUID: nodeUUID, content: nil}
		}
		lc := newLogContent(units)
		for i, lu := range units {
			var buf strings.Builder
			_, err := logstream.FetchAndPrintLog(hc, a, lu.Key, fmtFlag, isPty, &buf)
			if err != nil {
				return lvLogLoadedMsg{nodeUUID: nodeUUID, err: err}
			}
			lc.append(i, buf.String())
		}
		return lvLogLoadedMsg{nodeUUID: nodeUUID, content: lc}
	}
}

// selectedIndex returns the slice index of the currently selected step, or 0.
func (m *logViewModel) selectedIndex() int {
	for i, s := range m.steps {
		if s.UUID == m.selectedUUID {
			return i
		}
	}
	return 0
}

// selectedNode returns a pointer to the currently selected node, or nil.
func (m *logViewModel) selectedNode() *execgraph.GraphNode {
	for i := range m.steps {
		if m.steps[i].UUID == m.selectedUUID {
			return &m.steps[i]
		}
	}
	return nil
}

func (m logViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComponents()
		m.vpReady = true
		return m, nil

	case tea.KeyPressMsg:
		// Modal intercepts all keys when open.
		if m.saveModal {
			switch msg.String() {
			case "ctrl+c":
				for _, ss := range m.activeStreams {
					ss.cancel()
				}
				return m, tea.Quit
			case "esc", "n":
				if m.saveConfirm {
					// "n" or esc on confirm → back to filename input
					m.saveConfirm = false
					m.saveStatus = ""
					if msg.String() == "esc" {
						// esc from confirm dismisses the whole modal
						m.saveModal = false
						m.saveInput = ""
						m.saveDone = false
					}
				} else {
					m.saveModal = false
					m.saveInput = ""
					m.saveStatus = ""
					m.saveDone = false
					m.saveConfirm = false
				}
			case "enter":
				if m.saveDone {
					m.saveModal = false
					m.saveInput = ""
					m.saveStatus = ""
					m.saveDone = false
					m.saveConfirm = false
				} else if !m.saveConfirm {
					if m.saveInput == "" {
						break
					}
					if _, err := os.Stat(m.saveInput); err == nil {
						// file exists — ask overwrite/append
						m.saveConfirm = true
					} else {
						m.doSaveLog(false)
					}
				}
			case "y":
				if m.saveConfirm {
					m.doSaveLog(false)
					m.saveConfirm = false
				} else if !m.saveDone {
					m.saveInput += "y"
					m.saveStatus = ""
				}
			case "a":
				if m.saveConfirm {
					m.doSaveLog(true)
					m.saveConfirm = false
				} else if !m.saveDone {
					m.saveInput += "a"
					m.saveStatus = ""
				}
			case "backspace", "delete":
				if !m.saveDone && !m.saveConfirm && len(m.saveInput) > 0 {
					m.saveInput = m.saveInput[:len(m.saveInput)-1]
					m.saveStatus = ""
				}
			default:
				if !m.saveDone && !m.saveConfirm && len(msg.String()) == 1 {
					m.saveInput += msg.String()
					m.saveStatus = ""
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			for _, ss := range m.activeStreams {
				ss.cancel()
			}
			return m, tea.Quit
		case "s":
			if m.state == lvStateReady && m.activeTab == tabLogs && m.selectedUUID != "" {
				node := m.selectedNode()
				if node != nil {
					if _, ok := m.logCache[node.UUID]; ok {
						m.saveModal = true
						m.saveInput = ""
						m.saveStatus = ""
						m.saveDone = false
					}
				}
			}
			return m, nil
		case "r":
			if m.state == lvStateReady && m.selectedUUID != "" {
				node := m.selectedNode()
				if node != nil {
					delete(m.logCache, node.UUID)
					if ss, ok := m.activeStreams[node.UUID]; ok {
						ss.cancel()
						delete(m.activeStreams, node.UUID)
					}
				}
				return m, m.maybeLoadLog()
			}
			return m, nil
		case "up", "k":
			if m.state == lvStateReady {
				idx := m.selectedIndex()
				if idx > 0 {
					m.selectedUUID = m.steps[idx-1].UUID
					m.syncViewportForTab()
					return m, m.maybeLoadLog()
				}
			}
			return m, nil
		case "down", "j":
			if m.state == lvStateReady {
				idx := m.selectedIndex()
				if idx < len(m.steps)-1 {
					m.selectedUUID = m.steps[idx+1].UUID
					m.syncViewportForTab()
					return m, m.maybeLoadLog()
				}
			}
			return m, nil
		case "left":
			if m.state == lvStateReady {
				m.leftPanelOffset--
				m.clampLeftOffset()
			}
			return m, nil
		case "right":
			if m.state == lvStateReady {
				m.leftPanelOffset++
				m.clampLeftOffset()
			}
			return m, nil
		case "tab":
			if m.state == lvStateReady {
				m.activeTab = (m.activeTab + 1) % rightTab(len(tabDefs))
				m.syncViewportForTab()
				return m, m.maybeLoadLog()
			}
			return m, nil
		case "l", "d", "i", "o":
			if m.state == lvStateReady {
				for _, td := range tabDefs {
					if td.key == msg.String() {
						m.activeTab = td.tab
						m.syncViewportForTab()
						return m, m.maybeLoadLog()
					}
				}
			}
			return m, nil
		}
		// forward all other keys to viewport for scroll (pgup/pgdn etc.)
		if m.state == lvStateReady {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}

	case lvStepsLoadedMsg:
		if msg.err != nil {
			m.state = lvStateError
			m.errMsg = msg.err.Error()
			return m, nil
		}

		// Merge: update statuses and append new steps.
		existing := make(map[string]int, len(m.steps))
		for i, s := range m.steps {
			existing[s.UUID] = i
		}
		for _, s := range msg.steps {
			if i, ok := existing[s.UUID]; ok {
				m.steps[i] = s
			} else {
				m.steps = append(m.steps, s)
			}
		}

		m.pipelineDone = logstream.IsTerminalStatus(msg.pipelineStatus)
		m.leftPanelW = calcLeftPanelWidth(m.steps, m.activeStreams)
		m.clampLeftOffset()
		m.state = lvStateReady

		// Set initial selection to first step.
		if m.selectedUUID == "" && len(m.steps) > 0 {
			m.selectedUUID = m.steps[0].UUID
		}

		m.pollRefreshing = false
		if !m.pipelineDone {
			m.pollCountdown = lvPollIntervalSecs
		}

		var cmds []tea.Cmd
		cmds = append(cmds, m.maybeLoadLog())
		if !m.pipelineDone {
			cmds = append(cmds, countdownTick())
		}
		return m, tea.Batch(cmds...)

	case lvCountdownTickMsg:
		if m.pipelineDone {
			return m, nil
		}
		m.pollCountdown--
		if m.pollCountdown <= 0 {
			m.pollRefreshing = true
			return m, tea.Batch(
				func() tea.Msg { return m.spin.Tick() },
				m.loadSteps(),
			)
		}
		return m, countdownTick()

	case lvPollMsg: // kept for safety, not used by countdown path
		if m.pipelineDone {
			return m, nil
		}
		m.pollRefreshing = true
		return m, m.loadSteps()

	case lvLogLoadedMsg:
		m.loading = false
		if msg.err != nil {
			node := m.selectedNode()
			if node != nil && node.UUID == msg.nodeUUID && m.activeTab == tabLogs {
				m.vp.SetContent(m.st.errStyle.Render("error: " + msg.err.Error()))
				m.vp.GotoTop()
			}
			return m, nil
		}
		if msg.content == nil {
			m.logCache[msg.nodeUUID] = &logContent{units: []string{m.st.dim.Render("(no log content)")}, names: []string{""}, dirty: true}
		} else {
			m.logCache[msg.nodeUUID] = msg.content
		}
		node := m.selectedNode()
		if node != nil && node.UUID == msg.nodeUUID && m.activeTab == tabLogs {
			m.vp.SetContent(m.logCache[msg.nodeUUID].rendered())
			m.vp.GotoTop()
		}
		return m, nil

	case lvLogStreamLineMsg:
		lc := m.logCache[msg.nodeUUID]
		if lc != nil {
			for _, line := range msg.lines {
				lc.append(msg.unitIdx, line)
			}
		}
		node := m.selectedNode()
		if node != nil && node.UUID == msg.nodeUUID && lc != nil && m.activeTab == tabLogs {
			atBottom := m.vp.AtBottom()
			m.vp.SetContent(lc.rendered())
			if atBottom {
				m.vp.GotoBottom()
			}
		}
		// Re-arm: read next event from the channel.
		if ss, ok := m.activeStreams[msg.nodeUUID]; ok {
			return m, waitForSSEEvent(msg.nodeUUID, ss.ch, ss.unitIdx)
		}
		return m, nil

	case lvLogStreamDoneMsg:
		if msg.unitIdx >= 0 {
			// Unit boundary: advance unitIdx and re-arm to read the next unit's lines.
			if ss, ok := m.activeStreams[msg.nodeUUID]; ok {
				ss.unitIdx = msg.unitIdx
				m.activeStreams[msg.nodeUUID] = ss
				return m, waitForSSEEvent(msg.nodeUUID, ss.ch, msg.unitIdx)
			}
			return m, nil
		}
		if ss, ok := m.activeStreams[msg.nodeUUID]; ok {
			ss.cancel()
			delete(m.activeStreams, msg.nodeUUID)
		}
		// If cache is empty (SSE produced nothing), fall back to blob fetch.
		lc := m.logCache[msg.nodeUUID]
		if lc == nil || lc.rendered() == "" {
			node := m.selectedNode()
			if node != nil && node.UUID == msg.nodeUUID {
				m.loading = true
				m.vp.SetContent(m.st.dim.Render("loading…"))
				return m, tea.Batch(
					func() tea.Msg { return m.spin.Tick() },
					m.fetchLog(*node),
				)
			}
		}
		return m, nil
	}

	// spinner tick
	if m.state == lvStateLoading || m.loading || m.pollRefreshing || len(m.activeStreams) > 0 {
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *logViewModel) resizeComponents() {
	headerH := 2 // title line + blank line + help/hint line at bottom
	leftW := leftPanelWidth
	rightW := m.width - leftW - 1 // -1 for border
	vpH := m.height - headerH - 2 // -2 for tab bar + divider
	if vpH < 1 {
		vpH = 1
	}
	m.vp.SetWidth(rightW)
	m.vp.SetHeight(vpH)
}

// startSSEStream opens SSE connections for all log units of a running step,
// streaming them sequentially. Registers the stream in activeStreams and returns
// the first waitForSSEEvent Cmd.
func (m *logViewModel) startSSEStream(node execgraph.GraphNode) tea.Cmd {
	units := execgraph.GetLogUnits(node)
	if len(units) == 0 {
		return nil
	}
	nodeUUID := node.UUID
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan logstream.Event, 64)
	m.activeStreams[nodeUUID] = sseStream{cancel: cancel, ch: ch, unitIdx: 0}
	// Pre-populate logContent so append() has valid slots.
	if m.logCache[nodeUUID] == nil {
		m.logCache[nodeUUID] = newLogContent(units)
	}

	hc := &http.Client{Timeout: 90 * time.Minute}
	a := m.ctx.Auth
	fmtFlag := m.ctx.FormatFlags.Format
	isPty := m.ctx.IsPty

	go func() {
		for i, lu := range units {
			// Signal unit boundary so the model can advance unitIdx.
			if i > 0 {
				ch <- logstream.Event{Kind: logstream.EvStart, Source: fmt.Sprintf("%d", i)}
			}
			logstream.StreamSSEToChannel(ctx, hc, a, lu.Key, "", fmtFlag, isPty, ch) //nolint
		}
		close(ch)
	}()

	return waitForSSEEvent(nodeUUID, ch, 0)
}

// waitForSSEEvent reads one event from the SSE channel and returns it as a
// bubbletea message. Called recursively via Cmd until the channel closes.
func waitForSSEEvent(nodeUUID string, ch <-chan logstream.Event, unitIdx int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return lvLogStreamDoneMsg{nodeUUID: nodeUUID, unitIdx: -1}
		}
		// EvStart with numeric source is our unit-boundary sentinel.
		if ev.Kind == logstream.EvStart {
			newIdx := unitIdx + 1
			return lvLogStreamDoneMsg{nodeUUID: nodeUUID, unitIdx: newIdx}
		}
		return lvLogStreamLineMsg{nodeUUID: nodeUUID, unitIdx: unitIdx, lines: ev.Lines}
	}
}

func (m *logViewModel) maybeLoadLog() tea.Cmd {
	if m.activeTab != tabLogs {
		return nil
	}
	if len(m.steps) == 0 || m.selectedUUID == "" {
		return nil
	}
	node := m.selectedNode()
	if node == nil {
		return nil
	}
	if !execgraph.HasLogs(*node) {
		m.vp.SetContent(m.st.dim.Render("(no logs for this step)"))
		m.vp.GotoTop()
		return nil
	}

	terminal := logstream.IsTerminalStatus(node.Status)

	if terminal {
		// Blob path: show cache if present, otherwise fetch.
		if lc, ok := m.logCache[node.UUID]; ok {
			m.vp.SetContent(lc.rendered())
			m.vp.GotoTop()
			return nil
		}
		m.loading = true
		m.vp.SetContent(m.st.dim.Render("loading…"))
		return tea.Batch(
			func() tea.Msg { return m.spin.Tick() },
			m.fetchLog(*node),
		)
	}

	// Running path: show current cache (may be empty) and ensure stream is live.
	if lc, ok := m.logCache[node.UUID]; ok {
		m.vp.SetContent(lc.rendered())
		m.vp.GotoBottom()
	} else {
		m.vp.SetContent(m.st.dim.Render("connecting…"))
	}

	if _, streaming := m.activeStreams[node.UUID]; streaming {
		// Stream already running in background — nothing to start.
		return nil
	}
	return tea.Batch(
		func() tea.Msg { return m.spin.Tick() },
		m.startSSEStream(*node),
	)
}

func (m logViewModel) View() tea.View {
	var b strings.Builder
	st := m.st

	// header
	b.WriteString(st.header.Render("Execution Logs  "+st.dim.Render(m.execLabel)) + "\n")

	switch m.state {
	case lvStateLoading:
		b.WriteString(m.spin.View() + " Loading steps…\n")
	case lvStateError:
		b.WriteString(st.errStyle.Render("  ✗ "+m.errMsg) + "\n")
	case lvStateReady:
		m.renderSplit(&b)
	}

	background := b.String()
	var out string
	if m.saveModal {
		out = overlayCenter(background, m.renderSaveModal(), m.width, m.height)
	} else {
		out = background
	}
	v := tea.NewView(out)
	v.AltScreen = true
	return v
}

const lvGlyphSentinel = "\x01"
const lvSpinSentinel = "\x02"

func (m logViewModel) renderLeftPanelRow(s execgraph.GraphNode, selected bool, leftW int) string {
	st := m.st
	ss := format.BucketStyles[format.ClassifyExecutionStatus(s.Status)]
	_, streaming := m.activeStreams[s.UUID]

	// Build plain sentinel string, apply scroll offset, truncate, then append spinner if needed.
	indent := strings.Repeat(" ", s.Depth)
	plain := indent + lvGlyphSentinel + " " + execgraph.NodeName(s)
	if m.leftPanelOffset > 0 && m.leftPanelOffset < len(plain) {
		plain = plain[m.leftPanelOffset:]
	} else if m.leftPanelOffset >= len(plain) {
		plain = ""
	}
	truncW := leftW - 1
	if streaming {
		truncW = leftW - 3
	}
	if len(plain) > truncW {
		plain = plain[:truncW]
	}
	if streaming {
		plain += lvSpinSentinel
	}

	spinView := m.spin.View()
	if selected {
		line := strings.ReplaceAll(plain, lvGlyphSentinel, ss.NodeGlyph)
		line = strings.ReplaceAll(line, lvSpinSentinel, spinView)
		return st.selected.Width(leftW).Render(line)
	}
	coloredGlyph := lipgloss.NewStyle().Foreground(lipgloss.Color(ss.LipglossColor)).Render(ss.NodeGlyph)
	line := strings.ReplaceAll(plain, lvGlyphSentinel, coloredGlyph)
	line = strings.ReplaceAll(line, lvSpinSentinel, st.dim.Render(spinView))
	return st.normal.Width(leftW).Render(line)
}

func (m logViewModel) renderSplit(b *strings.Builder) {
	st := m.st
	headerH := 2 // title line + blank line + help/hint line at bottom
	listH := m.height - headerH
	if listH < 1 {
		listH = 1
	}

	leftW := leftPanelWidth
	selectedIdx := m.selectedIndex()

	// build left panel lines (blank first row for visual padding)
	leftLines := make([]string, 0, len(m.steps)+1)
	leftLines = append(leftLines, strings.Repeat(" ", leftW))
	for i, s := range m.steps {
		leftLines = append(leftLines, m.renderLeftPanelRow(s, i == selectedIdx, leftW))
	}

	// scroll left panel so selected is visible (+1 for blank padding row)
	start := 0
	if selectedIdx+1 >= listH {
		start = selectedIdx + 1 - listH + 1
	}
	end := start + listH
	if end > len(leftLines) {
		end = len(leftLines)
	}
	visibleLeft := leftLines[start:end]
	// pad to listH
	for len(visibleLeft) < listH {
		visibleLeft = append(visibleLeft, strings.Repeat(" ", leftW))
	}

	// build right panel: tab bar + active tab content
	rightContent := m.renderRightPanel()
	rightLines := strings.Split(rightContent, "\n")
	for len(rightLines) < listH {
		rightLines = append(rightLines, "")
	}

	// render side by side
	for i := 0; i < listH; i++ {
		leftCell := ""
		if i < len(visibleLeft) {
			leftCell = visibleLeft[i]
		}
		rightCell := ""
		if i < len(rightLines) {
			rightCell = rightLines[i]
		}
		b.WriteString(st.border.Width(leftW).Render(leftCell) + " " + rightCell + "\n")
	}

	// help line: left side is fixed, right side shows poll state / scroll %
	helpLeft := "  ↑/↓ select · l/d/i/o/tab tab · pgup/pgdn scroll · r refresh · q quit"
	if m.activeTab == tabLogs {
		helpLeft = "  ↑/↓ select · l/d/i/o/tab tab · pgup/pgdn scroll · r refresh · s save · q quit"
	}

	var helpRight string
	if !m.pipelineDone {
		if m.pollRefreshing {
			helpRight = "refreshing " + m.spin.View()
		} else {
			helpRight = fmt.Sprintf("refresh in %ds", m.pollCountdown)
		}
	} else if pct := m.vp.ScrollPercent(); pct > 0 || !m.vp.AtBottom() {
		helpRight = fmt.Sprintf("%.0f%%", pct*100)
	}

	leftHint := st.scrollHint.Render(helpLeft)
	rightHint := st.dim.Render(helpRight)
	b.WriteString(leftHint + "  " + rightHint + "\n")
}

// RunLogViewer launches the full-screen log viewer TUI.
// execLabel is shown in the header (e.g. "sawka_test2 / 2QPmypuy...").
func RunLogViewer(execLabel string, ctx *cmdctx.Ctx) error {
	m := newLogViewModel(execLabel, ctx)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// execLabelFromID builds a human-readable label from the raw ID argument.
// Input may be "pipelineId/execId", "pipelineId/runNum/-execId", or just "execId".
func execLabelFromID(id string) string {
	id = strings.TrimRight(id, "/")
	parts := strings.SplitN(id, "/", 4)
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " / " + parts[1]
	default:
		return parts[0] + " / " + strings.TrimPrefix(parts[2], "-")
	}
}
