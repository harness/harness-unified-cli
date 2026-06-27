// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/logstream"
	"github.com/harness/harness-cli/pkg/tui"
)

const lvPollIntervalSecs = 2

// --- messages ---

type lvStepsLoadedMsg struct {
	steps          []lvStep
	pipelineStatus string
	err            error
}

type lvLogLoadedMsg struct {
	logKey string
	body   string
	err    error
}

type lvCountdownTickMsg struct{}
type lvPollMsg struct{}

type lvLogStreamLineMsg struct {
	logKey string
	lines  []string
}

type lvLogStreamDoneMsg struct {
	logKey string
}

// --- step item ---

type lvStep struct {
	depth  int
	name   string
	status string
	logKey string // empty for non-loggable nodes (STRATEGY etc.)
	endTs  int64
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

// --- SSE stream handle ---

type sseStream struct {
	cancel context.CancelFunc
	ch     chan logstream.Event
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
	steps     []lvStep
	// selectedKey is the logKey of the highlighted step; stable across polls.
	selectedKey     string
	logCache        map[string]string    // logKey → rendered log text
	activeStreams   map[string]sseStream // logKey → live SSE stream (running steps only)
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

func calcLeftPanelWidth(steps []lvStep, activeStreams map[string]sseStream) int {
	w := 0
	for _, s := range steps {
		n := s.depth + 3 + len(s.name)
		if _, streaming := activeStreams[s.logKey]; streaming {
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
		logCache:      make(map[string]string),
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
	execID := bareExecID(ctx.Auth, m.execLabel)
	return func() tea.Msg {
		entries, pipelineStatus, err := logstream.FetchLogKeys(ctx, execID)
		if err != nil {
			return lvStepsLoadedMsg{err: err}
		}
		steps := make([]lvStep, 0, len(entries))
		for _, e := range entries {
			steps = append(steps, lvStep{
				depth:  keyDepth(e.LogKey),
				name:   e.Name,
				status: e.Status,
				logKey: e.LogKey,
				endTs:  e.EndTs,
			})
		}
		normalizeDepths(steps)
		return lvStepsLoadedMsg{steps: steps, pipelineStatus: pipelineStatus}
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
func bareExecID(_ *auth.ResolvedAuth, label string) string {
	parts := strings.SplitN(label, " / ", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return label
}

// normalizeDepths remaps step depths so they start at 0 and have no gaps.
// e.g. [2, 4, 4, 6] → [0, 1, 1, 2]
func normalizeDepths(steps []lvStep) {
	seen := make(map[int]struct{})
	for _, s := range steps {
		seen[s.depth] = struct{}{}
	}
	sorted := make([]int, 0, len(seen))
	for d := range seen {
		sorted = append(sorted, d)
	}
	// sort ascending
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	rank := make(map[int]int, len(sorted))
	for i, d := range sorted {
		rank[d] = i
	}
	for i := range steps {
		steps[i].depth = rank[steps[i].depth]
	}
}

// keyDepth returns a visual depth for a log key based on segment count beyond the base 3 (pipeline/run/-execId).
func keyDepth(logKey string) int {
	parts := strings.Split(logKey, "/")
	if len(parts) <= 4 {
		return 0
	}
	return len(parts) - 4
}

func (m logViewModel) fetchLog(logKey string) tea.Cmd {
	hc := &http.Client{Timeout: 30 * time.Second}
	a := m.ctx.Auth
	return func() tea.Msg {
		var buf strings.Builder
		_, err := logstream.FetchAndPrintLog(hc, a, logKey, "", true, &buf)
		return lvLogLoadedMsg{logKey: logKey, body: buf.String(), err: err}
	}
}

// selectedIndex returns the slice index of the currently selected step, or 0.
func (m *logViewModel) selectedIndex() int {
	for i, s := range m.steps {
		if s.logKey == m.selectedKey {
			return i
		}
	}
	return 0
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
		switch msg.String() {
		case "ctrl+c", "q":
			for _, ss := range m.activeStreams {
				ss.cancel()
			}
			return m, tea.Quit
		case "r":
			if m.state == lvStateReady && m.selectedKey != "" {
				delete(m.logCache, m.selectedKey)
				return m, m.maybeLoadLog()
			}
			return m, nil
		case "up", "k":
			if m.state == lvStateReady {
				idx := m.selectedIndex()
				if idx > 0 {
					m.selectedKey = m.steps[idx-1].logKey
					return m, m.maybeLoadLog()
				}
			}
			return m, nil
		case "down", "j":
			if m.state == lvStateReady {
				idx := m.selectedIndex()
				if idx < len(m.steps)-1 {
					m.selectedKey = m.steps[idx+1].logKey
					return m, m.maybeLoadLog()
				}
			}
			return m, nil
		case "left", "h":
			if m.state == lvStateReady {
				m.leftPanelOffset--
				m.clampLeftOffset()
			}
			return m, nil
		case "right", "l":
			if m.state == lvStateReady {
				m.leftPanelOffset++
				m.clampLeftOffset()
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
		existing := make(map[string]int, len(m.steps)) // logKey → index in m.steps
		for i, s := range m.steps {
			existing[s.logKey] = i
		}
		for _, s := range msg.steps {
			if i, ok := existing[s.logKey]; ok {
				m.steps[i].status = s.status
				m.steps[i].endTs = s.endTs
			} else {
				m.steps = append(m.steps, s)
			}
		}

		m.pipelineDone = logstream.IsTerminalStatus(msg.pipelineStatus)
		m.leftPanelW = calcLeftPanelWidth(m.steps, m.activeStreams)
		m.clampLeftOffset()
		m.state = lvStateReady

		// Set initial selection to first loggable step.
		if m.selectedKey == "" && len(m.steps) > 0 {
			for _, s := range m.steps {
				if s.logKey != "" {
					m.selectedKey = s.logKey
					break
				}
			}
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
			if msg.logKey == m.selectedKey {
				m.vp.SetContent(m.st.errStyle.Render("error: " + msg.err.Error()))
				m.vp.GotoTop()
			}
			return m, nil
		}
		if msg.body == "" {
			m.logCache[msg.logKey] = m.st.dim.Render("(no log content)")
		} else {
			m.logCache[msg.logKey] = msg.body
		}
		if msg.logKey == m.selectedKey {
			m.vp.SetContent(m.logCache[msg.logKey])
			m.vp.GotoTop()
		}
		return m, nil

	case lvLogStreamLineMsg:
		for _, line := range msg.lines {
			m.logCache[msg.logKey] += line
		}
		if msg.logKey == m.selectedKey {
			atBottom := m.vp.AtBottom()
			m.vp.SetContent(m.logCache[msg.logKey])
			if atBottom {
				m.vp.GotoBottom()
			}
		}
		// Re-arm: read next event from the channel.
		if ss, ok := m.activeStreams[msg.logKey]; ok {
			return m, waitForSSEEvent(msg.logKey, ss.ch)
		}
		return m, nil

	case lvLogStreamDoneMsg:
		if ss, ok := m.activeStreams[msg.logKey]; ok {
			ss.cancel()
			delete(m.activeStreams, msg.logKey)
		}
		// If cache is empty (SSE produced nothing), fall back to blob fetch.
		if _, hasCached := m.logCache[msg.logKey]; !hasCached {
			var step *lvStep
			for i := range m.steps {
				if m.steps[i].logKey == msg.logKey {
					step = &m.steps[i]
					break
				}
			}
			if step != nil && msg.logKey == m.selectedKey {
				m.loading = true
				m.vp.SetContent(m.st.dim.Render("loading…"))
				return m, tea.Batch(
					func() tea.Msg { return m.spin.Tick() },
					m.fetchLog(step.logKey),
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
	headerH := 3 // title + blank + help
	leftW := leftPanelWidth
	rightW := m.width - leftW - 1 // -1 for border
	vpH := m.height - headerH
	if vpH < 1 {
		vpH = 1
	}
	m.vp.SetWidth(rightW)
	m.vp.SetHeight(vpH)
}

// startSSEStream opens an SSE connection for a running step, registers it in
// activeStreams, and returns the first waitForSSEEvent Cmd.
func (m *logViewModel) startSSEStream(logKey string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan logstream.Event, 64)
	m.activeStreams[logKey] = sseStream{cancel: cancel, ch: ch}

	hc := &http.Client{Timeout: 90 * time.Minute}
	a := m.ctx.Auth
	fmtFlag := m.ctx.FormatFlags.Format
	isPty := m.ctx.IsPty

	go func() {
		logstream.StreamSSEToChannel(ctx, hc, a, logKey, "", fmtFlag, isPty, ch) //nolint
		close(ch)
	}()

	return waitForSSEEvent(logKey, ch)
}

// waitForSSEEvent reads one event from the SSE channel and returns it as a
// bubbletea message. Called recursively via Cmd until the channel closes.
func waitForSSEEvent(logKey string, ch <-chan logstream.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return lvLogStreamDoneMsg{logKey: logKey}
		}
		return lvLogStreamLineMsg{logKey: logKey, lines: ev.Lines}
	}
}

func (m *logViewModel) maybeLoadLog() tea.Cmd {
	if len(m.steps) == 0 || m.selectedKey == "" {
		return nil
	}
	var step *lvStep
	for i := range m.steps {
		if m.steps[i].logKey == m.selectedKey {
			step = &m.steps[i]
			break
		}
	}
	if step == nil {
		return nil
	}
	if step.logKey == "" {
		m.vp.SetContent(m.st.dim.Render("(no logs for this step)"))
		m.vp.GotoTop()
		return nil
	}

	terminal := logstream.IsTerminalStatus(step.status)

	if terminal {
		// Blob path: show cache if present, otherwise fetch.
		if body, ok := m.logCache[step.logKey]; ok {
			m.vp.SetContent(body)
			m.vp.GotoTop()
			return nil
		}
		m.loading = true
		m.vp.SetContent(m.st.dim.Render("loading…"))
		return tea.Batch(
			func() tea.Msg { return m.spin.Tick() },
			m.fetchLog(step.logKey),
		)
	}

	// Running path: show current cache (may be empty) and ensure stream is live.
	if body, ok := m.logCache[step.logKey]; ok {
		m.vp.SetContent(body)
		m.vp.GotoBottom()
	} else {
		m.vp.SetContent(m.st.dim.Render("connecting…"))
	}

	if _, streaming := m.activeStreams[step.logKey]; streaming {
		// Stream already running in background — nothing to start.
		return nil
	}
	return tea.Batch(
		func() tea.Msg { return m.spin.Tick() },
		m.startSSEStream(step.logKey),
	)
}

func (m logViewModel) View() tea.View {
	var b strings.Builder
	st := m.st

	// header
	b.WriteString(st.header.Render("Execution Logs  "+st.dim.Render(m.execLabel)) + "\n\n")

	switch m.state {
	case lvStateLoading:
		b.WriteString(m.spin.View() + " Loading steps…\n")
	case lvStateError:
		b.WriteString(st.errStyle.Render("  ✗ "+m.errMsg) + "\n")
	case lvStateReady:
		m.renderSplit(&b)
	}

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

const lvGlyphSentinel = "\x01"
const lvSpinSentinel = "\x02"

func (m logViewModel) renderLeftPanelRow(s lvStep, selected bool, leftW int) string {
	st := m.st
	ss := format.BucketStyles[format.ClassifyExecutionStatus(s.status)]
	_, streaming := m.activeStreams[s.logKey]

	// Build plain sentinel string, apply scroll offset, truncate, then append spinner if needed.
	indent := strings.Repeat(" ", s.depth)
	plain := indent + lvGlyphSentinel + " " + s.name
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
	headerH := 3 // title line + blank line + help/hint line at bottom
	listH := m.height - headerH
	if listH < 1 {
		listH = 1
	}

	leftW := leftPanelWidth
	selectedIdx := m.selectedIndex()

	// build left panel lines
	leftLines := make([]string, 0, len(m.steps))
	for i, s := range m.steps {
		leftLines = append(leftLines, m.renderLeftPanelRow(s, i == selectedIdx, leftW))
	}

	// scroll left panel so selected is visible
	start := 0
	if selectedIdx >= listH {
		start = selectedIdx - listH + 1
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

	// build right panel: viewport or loading indicator
	var rightContent string
	if m.loading {
		rightContent = m.spin.View() + " loading…"
	} else {
		rightContent = m.vp.View()
	}
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
	helpLeft := "  ↑/↓ select step · pgup/pgdn scroll log · r refresh · q quit"

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
