// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	pkgauth "github.com/harness/harness-cli/pkg/auth"
	hclient "github.com/harness/harness-cli/pkg/client"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/hlog"
	"github.com/harness/harness-cli/pkg/tui"
)

// WizardResult is returned by RunLoginWizard on success.
type WizardResult struct {
	APIURL      string
	Token       string
	Account     string
	RegURL      string
	OrgID       string
	Project     string
	ScopeNotSet    bool // true when org/project not configured — caller should prompt setscope
	ScopeSkipped   bool // true when user explicitly chose to save without selecting org/project
}

// errNoEnumPerms is set as cancelReason when a token can't list orgs/projects.
// RunLoginWizard treats this as a partial success rather than a user cancel.
var errNoEnumPerms = fmt.Errorf("token lacks permission to list organizations — run 'harness auth setscope' to configure org and project")

// WizardExisting carries values from an already-saved profile so the wizard
// can offer "use existing" options instead of requiring re-entry.
type WizardExisting struct {
	APIURL string
	Token  string
}

type wizardStep int

const (
	stepURL wizardStep = iota
	stepToken
	stepValidating
	stepOrgLoad
	stepOrgPick
	stepProjectLoad
	stepProjectPick
	stepDone
)

const defaultAPIURL = "https://app.harness.io"

// urlOpt represents one entry in the URL picker.
type urlOpt struct {
	label string
	value string // "" means "enter custom"
}

// --- styles ---

type wizardStyles struct {
	title, subtle, errStyle, selected, prompt, box lipgloss.Style
}

func newWizardStyles() wizardStyles {
	return wizardStyles{
		title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(tui.CLIAccent)),
		subtle:   lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLITextMuted)),
		errStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLIError)),
		selected: lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLIAccent)).Bold(true),
		prompt:   lipgloss.NewStyle().Foreground(lipgloss.Color(tui.CLIAccent)),
		box:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(tui.CLIBorder)).Padding(0, 1),
	}
}

// --- list item ---

type orgItem struct{ id, name string }

func (o orgItem) Title() string       { return o.name }
func (o orgItem) Description() string { return o.id }
func (o orgItem) FilterValue() string { return o.name + " " + o.id }

// --- messages ---

type validateDoneMsg struct {
	apiURL    string
	accountID string
	regURL    string
	err       error
}

type orgsDoneMsg struct {
	orgs []orgItem
	err  error
}

type projectsDoneMsg struct {
	projects []orgItem
	err      error
}

// --- model ---

type wizardModel struct {
	st   wizardStyles
	step wizardStep

	urlInput   textinput.Model
	tokenInput textinput.Model
	spin       spinner.Model
	orgList    list.Model
	projList   list.Model

	// URL step state
	urlOpts     []urlOpt
	urlPickIdx  int
	urlInCustom bool // true = text input active

	// Token step state
	existingToken    string
	tokenHasExisting bool
	tokenPickIdx     int
	tokenInCustom    bool // true = text input active (or no existing token)

	apiURL    string
	token     string
	accountID string
	regURL    string
	orgID     string
	projectID string

	// pre-selected values (for set-wizard mode)
	currentOrgID     string
	currentProjectID string
	setMode          bool             // started at org pick; no URL/token steps
	authType         pkgauth.AuthType // AuthTypePAT or AuthTypeSSO

	cmdCtx       *cmdctx.Ctx
	err          string
	canceled     bool
	cancelReason error // set when canceled due to an internal error, not user action
	scopeSkipped bool  // user chose to save without selecting org/project
	width        int
	height       int
}

func buildURLOpts(existingAPIURL string) (opts []urlOpt, defaultIdx int) {
	opts = []urlOpt{
		{label: "app.harness.io  (default)", value: defaultAPIURL},
	}
	defaultIdx = 0
	if existingAPIURL != "" && existingAPIURL != defaultAPIURL {
		opts = append(opts, urlOpt{
			label: existingAPIURL + "  (existing)",
			value: existingAPIURL,
		})
		defaultIdx = len(opts) - 1 // pre-select the existing URL
	}
	opts = append(opts, urlOpt{label: "Enter custom URL...", value: ""})
	return opts, defaultIdx
}

func newWizardModel(existing *WizardExisting) wizardModel {
	url := textinput.New()
	url.Placeholder = defaultAPIURL
	url.SetWidth(50)

	tok := textinput.New()
	tok.Placeholder = "pat.xxxxxxxx.xxxxxxxx.xxxxxxxx or sat.xxxxxxxx.xxxxxxxx.xxxxxxxx"
	tok.EchoMode = textinput.EchoPassword
	tok.EchoCharacter = '•'
	tok.SetWidth(60)

	st := newWizardStyles()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = st.prompt

	newList := func(title string) list.Model {
		delegate := list.NewDefaultDelegate()
		delegate.ShowDescription = false
		delegate.SetHeight(1)
		delegate.SetSpacing(0)
		delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(lipgloss.Color(tui.CLIAccent)).BorderLeftForeground(lipgloss.Color(tui.CLIAccent))
		l := list.New(nil, delegate, 60, 20)
		l.Title = title
		l.Styles.Title = st.title
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(true)
		return l
	}

	var existingAPIURL, existingToken string
	if existing != nil {
		existingAPIURL = existing.APIURL
		existingToken = existing.Token
	}

	urlOpts, urlPickIdx := buildURLOpts(existingAPIURL)

	tokenHasExisting := existingToken != ""
	tokenInCustom := !tokenHasExisting // go straight to text input if no existing token

	if tokenInCustom {
		tok.Focus()
	}

	return wizardModel{
		st:         st,
		step:       stepURL,
		urlInput:   url,
		tokenInput: tok,
		spin:       sp,
		orgList:    newList("Select an organization"),
		projList:   newList("Select a project"),

		urlOpts:    urlOpts,
		urlPickIdx: urlPickIdx,

		existingToken:    existingToken,
		tokenHasExisting: tokenHasExisting,
		tokenInCustom:    tokenInCustom,

		width:  80,
		height: 24,
	}
}

func (m wizardModel) Init() tea.Cmd { return nil }

func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		listH := msg.Height - 10
		listH = max(listH, 6)
		m.orgList.SetSize(msg.Width-4, listH)
		m.projList.SetSize(msg.Width-4, listH)
		return m, nil

	case tea.KeyPressMsg:
		// When a list is actively filtering, forward all keys to it so that
		// characters like "s" and "esc" don't trigger our shortcut handlers.
		switch m.step {
		case stepOrgPick:
			if m.orgList.FilterState() == list.Filtering {
				var cmd tea.Cmd
				m.orgList, cmd = m.orgList.Update(msg)
				return m, cmd
			}
		case stepProjectPick:
			if m.projList.FilterState() == list.Filtering {
				var cmd tea.Cmd
				m.projList, cmd = m.projList.Update(msg)
				return m, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c":
			m.canceled = true
			return m, tea.Quit

		case "up":
			switch m.step {
			case stepURL:
				if !m.urlInCustom && m.urlPickIdx > 0 {
					m.urlPickIdx--
					return m, nil
				}
			case stepToken:
				if m.tokenHasExisting && !m.tokenInCustom && m.tokenPickIdx > 0 {
					m.tokenPickIdx--
					return m, nil
				}
			}

		case "down":
			switch m.step {
			case stepURL:
				if !m.urlInCustom && m.urlPickIdx < len(m.urlOpts)-1 {
					m.urlPickIdx++
					return m, nil
				}
			case stepToken:
				if m.tokenHasExisting && !m.tokenInCustom && m.tokenPickIdx < 1 {
					m.tokenPickIdx++
					return m, nil
				}
			}

		case "esc":
			switch m.step {
			case stepURL:
				if m.urlInCustom {
					m.urlInCustom = false
					m.urlInput.Blur()
					m.err = ""
					return m, nil
				}
				m.canceled = true
				return m, tea.Quit
			case stepToken:
				if m.tokenInCustom && m.tokenHasExisting {
					m.tokenInCustom = false
					m.tokenInput.Blur()
					m.err = ""
					return m, nil
				}
				// go back to URL step (picker mode)
				m.step = stepURL
				m.urlInCustom = false
				m.urlInput.Blur()
				m.tokenInput.Blur()
				m.err = ""
				return m, nil
			case stepOrgPick:
				if m.setMode {
					m.canceled = true
					return m, tea.Quit
				}
				m.step = stepToken
				m.tokenInCustom = true
				m.tokenInput.Focus()
				m.err = ""
				return m, textinput.Blink
			case stepProjectPick:
				m.step = stepOrgPick
				m.err = ""
				return m, nil
			default:
				m.canceled = true
				return m, tea.Quit
			}

		case "s":
			if !m.setMode {
				switch m.step {
				case stepOrgPick:
					m.scopeSkipped = true
					m.step = stepDone
					return m, tea.Quit
				case stepProjectPick:
					m.scopeSkipped = true
					m.step = stepDone
					return m, tea.Quit
				}
			}

		case "enter":
			return m.handleEnter()
		}

	case validateDoneMsg:
		if msg.err != nil {
			m.step = stepToken
			m.tokenInCustom = true
			m.tokenInput.Focus()
			m.err = msg.err.Error()
			return m, textinput.Blink
		}
		m.apiURL = msg.apiURL
		m.accountID = msg.accountID
		m.regURL = msg.regURL
		m.err = ""
		m.step = stepOrgLoad
		return m, tea.Batch(func() tea.Msg { return m.spin.Tick() }, m.fetchOrgs())

	case orgsDoneMsg:
		if msg.err != nil {
			if m.setMode {
				m.err = msg.err.Error()
				m.canceled = true
				m.cancelReason = msg.err
				return m, tea.Quit
			}
			// SAT/PAT tokens may lack org-list permissions — treat as partial success.
			tokenKind := pkgauth.TokenType(m.token)
			if (tokenKind == pkgauth.TokenKindSAT || tokenKind == pkgauth.TokenKindPAT) && strings.Contains(msg.err.Error(), "403") {
				m.canceled = true
				m.cancelReason = errNoEnumPerms
				return m, tea.Quit
			}
			m.step = stepToken
			m.tokenInCustom = true
			m.tokenInput.Focus()
			m.err = msg.err.Error()
			return m, textinput.Blink
		}
		// SAT/PAT token with no visible orgs — same treatment as 403.
		tokenKind := pkgauth.TokenType(m.token)
		if len(msg.orgs) == 0 && (tokenKind == pkgauth.TokenKindSAT || tokenKind == pkgauth.TokenKindPAT) {
			m.canceled = true
			m.cancelReason = errNoEnumPerms
			return m, tea.Quit
		}
		items := make([]list.Item, len(msg.orgs))
		for i, o := range msg.orgs {
			items[i] = o
		}
		m.orgList.SetItems(items)
		if m.currentOrgID != "" {
			for i, o := range msg.orgs {
				if o.id == m.currentOrgID {
					m.orgList.Select(i)
					break
				}
			}
		}
		m.step = stepOrgPick
		return m, nil

	case projectsDoneMsg:
		if msg.err != nil {
			// SAT/PAT tokens may lack project-list permissions — treat as partial success.
			tokenKind := pkgauth.TokenType(m.token)
			if (tokenKind == pkgauth.TokenKindSAT || tokenKind == pkgauth.TokenKindPAT) && strings.Contains(msg.err.Error(), "403") {
				m.canceled = true
				m.cancelReason = errNoEnumPerms
				return m, tea.Quit
			}
			m.step = stepOrgPick
			m.err = msg.err.Error()
			return m, nil
		}
		items := make([]list.Item, len(msg.projects))
		for i, p := range msg.projects {
			items[i] = p
		}
		m.projList.SetItems(items)
		if m.currentProjectID != "" {
			for i, p := range msg.projects {
				if p.id == m.currentProjectID {
					m.projList.Select(i)
					break
				}
			}
		}
		m.step = stepProjectPick
		return m, nil
	}

	// delegate to active sub-component
	switch m.step {
	case stepURL:
		if m.urlInCustom {
			var cmd tea.Cmd
			m.urlInput, cmd = m.urlInput.Update(msg)
			return m, cmd
		}
	case stepToken:
		if m.tokenInCustom {
			var cmd tea.Cmd
			m.tokenInput, cmd = m.tokenInput.Update(msg)
			return m, cmd
		}
	case stepValidating, stepOrgLoad, stepProjectLoad:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case stepOrgPick:
		var cmd tea.Cmd
		m.orgList, cmd = m.orgList.Update(msg)
		return m, cmd
	case stepProjectPick:
		var cmd tea.Cmd
		m.projList, cmd = m.projList.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m wizardModel) handleEnter() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepURL:
		if !m.urlInCustom {
			opt := m.urlOpts[m.urlPickIdx]
			if opt.value == "" {
				// "Enter custom URL..." selected
				m.urlInCustom = true
				m.urlInput.Focus()
				m.err = ""
				return m, textinput.Blink
			}
			m.apiURL = opt.value
			m.step = stepToken
			m.err = ""
			if !m.tokenHasExisting {
				m.tokenInCustom = true
				m.tokenInput.Focus()
				return m, textinput.Blink
			}
			return m, nil
		}
		// custom text input
		val := m.urlInput.Value()
		if strings.TrimSpace(val) == "" {
			val = m.urlInput.Placeholder
		}
		val = pkgauth.NormalizeAPIURL(val)
		if err := pkgauth.ValidateAPIURL(val); err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.apiURL = val
		m.urlInput.Blur()
		m.step = stepToken
		m.err = ""
		if !m.tokenHasExisting {
			m.tokenInCustom = true
			m.tokenInput.Focus()
			return m, textinput.Blink
		}
		return m, nil

	case stepToken:
		if m.tokenHasExisting && !m.tokenInCustom {
			if m.tokenPickIdx == 0 {
				// use existing token
				m.token = m.existingToken
				m.step = stepValidating
				m.err = ""
				return m, tea.Batch(func() tea.Msg { return m.spin.Tick() }, m.doValidate())
			}
			// "Enter new token..." selected
			m.tokenInCustom = true
			m.tokenInput.Focus()
			m.err = ""
			return m, textinput.Blink
		}
		// text input
		tok := strings.TrimSpace(m.tokenInput.Value())
		if tok == "" {
			m.err = "token is required"
			return m, nil
		}
		m.token = tok
		m.tokenInput.Blur()
		m.step = stepValidating
		m.err = ""
		return m, tea.Batch(func() tea.Msg { return m.spin.Tick() }, m.doValidate())

	case stepOrgPick:
		if m.orgList.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.orgList, cmd = m.orgList.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return m, cmd
		}
		sel, ok := m.orgList.SelectedItem().(orgItem)
		if !ok {
			return m, nil
		}
		m.orgID = sel.id
		m.step = stepProjectLoad
		return m, tea.Batch(func() tea.Msg { return m.spin.Tick() }, m.fetchProjects())

	case stepProjectPick:
		if m.projList.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.projList, cmd = m.projList.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return m, cmd
		}
		sel, ok := m.projList.SelectedItem().(orgItem)
		if !ok {
			return m, nil
		}
		m.projectID = sel.id
		m.step = stepDone
		return m, tea.Quit
	}
	return m, nil
}

func renderPicker(b *strings.Builder, st wizardStyles, opts []string, selectedIdx int) {
	for i, label := range opts {
		if i == selectedIdx {
			b.WriteString(st.selected.Render("  > "+label) + "\n")
		} else {
			b.WriteString(st.subtle.Render("    "+label) + "\n")
		}
	}
}

func (m wizardModel) View() tea.View {
	var b strings.Builder

	st := m.st
	b.WriteString(st.title.Render("  Harness Login") + "\n\n")

	switch m.step {
	case stepURL:
		b.WriteString(st.prompt.Render("API URL") + "\n")
		if !m.urlInCustom {
			labels := make([]string, len(m.urlOpts))
			for i, o := range m.urlOpts {
				labels[i] = o.label
			}
			renderPicker(&b, st, labels, m.urlPickIdx)
			b.WriteString(st.subtle.Render("  ↑/↓ to move · enter to select · esc to cancel") + "\n")
		} else {
			b.WriteString(m.urlInput.View() + "\n")
			b.WriteString(st.subtle.Render("  press enter to continue, esc to go back") + "\n")
		}

	case stepToken:
		b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("API URL: "+m.apiURL) + "\n\n")
		b.WriteString(st.prompt.Render("Harness PAT/SAT") + "\n")
		if m.tokenHasExisting && !m.tokenInCustom {
			tokenLabels := []string{
				"Use existing  (" + pkgauth.MaskedToken(m.existingToken) + ")",
				"Enter new token...",
			}
			renderPicker(&b, st, tokenLabels, m.tokenPickIdx)
			b.WriteString(st.subtle.Render("  ↑/↓ to move · enter to select · esc to go back") + "\n")
		} else {
			b.WriteString(m.tokenInput.View() + "\n")
			b.WriteString(st.subtle.Render("  press enter to continue, esc to go back") + "\n")
		}

	case stepValidating:
		b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("API URL: "+m.apiURL) + "\n")
		b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("Token: "+pkgauth.Masked(m.token)) + "\n\n")
		b.WriteString(m.spin.View() + " Validating credentials…\n")

	case stepOrgLoad, stepOrgPick, stepProjectLoad:
		if !m.setMode {
			b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("API URL: "+m.apiURL) + "\n")
			b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("Token: "+pkgauth.Masked(m.token)) + "\n\n")
		}
		if m.step == stepOrgLoad {
			b.WriteString(m.spin.View() + " Loading organizations…\n")
		} else if m.step == stepProjectLoad {
			b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("Org: "+m.orgID) + "\n\n")
			b.WriteString(m.spin.View() + " Loading projects…\n")
		} else {
			b.WriteString(st.box.Render(m.orgList.View()) + "\n")
			escHint := "esc to go back"
			if m.setMode {
				escHint = "esc to cancel"
			}
			saveHint := " · s to save without org/project"
			if m.setMode {
				saveHint = ""
			}
			b.WriteString(st.subtle.Render("  / to filter · enter to select · "+escHint+saveHint) + "\n")
		}

	case stepProjectPick:
		if !m.setMode {
			b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("API URL: "+m.apiURL) + "\n")
			b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("Token: "+pkgauth.Masked(m.token)) + "\n")
		}
		b.WriteString(st.selected.Render("✓ ") + st.subtle.Render("Org: "+m.orgID) + "\n\n")
		b.WriteString(st.box.Render(m.projList.View()) + "\n")
		b.WriteString(st.subtle.Render("  / to filter · enter to select · esc to change org · s to save without project") + "\n")
	}

	if m.err != "" {
		b.WriteString("\n" + st.errStyle.Render("  ✗ "+m.err) + "\n")
	}

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// --- async commands ---

func (m wizardModel) doValidate() tea.Cmd {
	apiURL := m.apiURL
	token := m.token
	return func() tea.Msg {
		accountID, regURL, err := validateAndFetch(apiURL, token)
		return validateDoneMsg{apiURL: apiURL, accountID: accountID, regURL: regURL, err: err}
	}
}

func (m wizardModel) fetchOrgs() tea.Cmd {
	cmdCtx := m.cmdCtx
	apiURL := m.apiURL
	token := m.token
	accountID := m.accountID
	return func() tea.Msg {
		orgs, err := fetchOrgItems(cmdCtx, apiURL, token, accountID, m.authType)
		return orgsDoneMsg{orgs: orgs, err: err}
	}
}

func (m wizardModel) fetchProjects() tea.Cmd {
	cmdCtx := m.cmdCtx
	apiURL := m.apiURL
	token := m.token
	accountID := m.accountID
	orgID := m.orgID
	return func() tea.Msg {
		projects, err := fetchProjectItems(cmdCtx, apiURL, token, accountID, orgID, m.authType)
		return projectsDoneMsg{projects: projects, err: err}
	}
}

// --- API helpers ---

func fetchOrgItems(ctx *cmdctx.Ctx, apiURL, token, accountID string, authType pkgauth.AuthType) ([]orgItem, error) {
	cs := ctx.Resolver.GetSpec("list", "organization")
	if cs == nil || cs.Endpoint == nil || cs.Endpoint.Paging == nil {
		return nil, fmt.Errorf("no spec found for list organization")
	}
	fetchCtx := *ctx
	fetchCtx.Verb = "list"
	fetchCtx.Noun = "organization"
	fetchCtx.Auth = newLoginResolvedAuth(apiURL, token, accountID, "", authType)
	items, err := ctx.Resolver.FetchItems(&fetchCtx, cs.Endpoint, cmdctx.PagingFlags{All: true})
	if err != nil {
		return nil, fmt.Errorf("fetching organizations: %w", err)
	}
	return orgItemsFromRaw(items, "it.organization.identifier", "it.organization.name")
}

func fetchProjectItems(ctx *cmdctx.Ctx, apiURL, token, accountID, orgID string, authType pkgauth.AuthType) ([]orgItem, error) {
	cs := ctx.Resolver.GetSpec("list", "project")
	if cs == nil || cs.Endpoint == nil || cs.Endpoint.Paging == nil {
		return nil, fmt.Errorf("no spec found for list project")
	}
	fetchCtx := *ctx
	fetchCtx.Verb = "list"
	fetchCtx.Noun = "project"
	fetchCtx.Auth = newLoginResolvedAuth(apiURL, token, accountID, orgID, authType)
	items, err := ctx.Resolver.FetchItems(&fetchCtx, cs.Endpoint, cmdctx.PagingFlags{All: true})
	if err != nil {
		return nil, fmt.Errorf("fetching projects: %w", err)
	}
	return orgItemsFromRaw(items, "it.project.identifier", "it.project.name")
}

func newLoginResolvedAuth(apiURL, token, accountID, orgID string, authType pkgauth.AuthType) *pkgauth.ResolvedAuth {
	ra := &pkgauth.ResolvedAuth{
		APIUrl:    apiURL,
		AuthType:  authType,
		AccountID: accountID,
		OrgID:     orgID,
	}
	if authType == pkgauth.AuthTypeSSO {
		ra.SSOToken = token
	} else {
		ra.PATToken = token
	}
	return ra
}

// orgItemsFromRaw maps raw FetchItems results to []orgItem using the completion exprs
// from the spec as a guide for which fields hold id and name.
func orgItemsFromRaw(items []any, idPath, namePath string) ([]orgItem, error) {
	out := make([]orgItem, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := deepGet(m, idPath)
		name := deepGet(m, namePath)
		if id == "" {
			continue
		}
		if name == "" {
			name = id
		}
		out = append(out, orgItem{id: id, name: name})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].name) < strings.ToLower(out[j].name) })
	return out, nil
}

// deepGet extracts a nested value from a map using a dot-path like "it.organization.identifier".
// Strips a leading "it." prefix if present.
func deepGet(m map[string]any, path string) string {
	path = strings.TrimPrefix(path, "it.")
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		cm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = cm[p]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

func validateAndFetch(apiURL, token string) (accountID, regURL string, err error) {
	accountID = pkgauth.AccountIDFromToken(token)
	if accountID == "" {
		return "", "", fmt.Errorf("token does not look like a Harness PAT/SAT (expected pat.<accountID>.<...> or sat.<accountID>.<...>)")
	}
	c := hclient.NewWithAuth(context.Background(), &pkgauth.ResolvedAuth{
		APIUrl:    apiURL,
		AuthType:  pkgauth.AuthTypePAT,
		PATToken:  token,
		AccountID: accountID,
	})
	if pkgauth.TokenType(token) == pkgauth.TokenKindSAT {
		if _, _, err := c.PostRaw("/ng/api/token/validate", map[string]string{"accountIdentifier": accountID}, token, "text/plain"); err != nil {
			return "", "", err
		}
	} else {
		if _, _, err := c.Get(fmt.Sprintf("/ng/api/accounts/%s", accountID), map[string]string{"accountIdentifier": accountID}); err != nil {
			return "", "", err
		}
	}
	regURL, _ = fetchRegistryURL(apiURL, token, accountID)
	return accountID, regURL, nil
}

// --- RunLoginWizard / RunSetWizard ---

// RunLoginWizard runs the interactive TUI and returns the collected values.
// Pass existing profile values so the wizard can offer "use existing" options.
// Returns (nil, nil) if the user canceled.
func RunLoginWizard(ctx *cmdctx.Ctx, existing *WizardExisting) (*WizardResult, error) {
	m := newWizardModel(existing)
	m.cmdCtx = ctx
	prev := hlog.SilenceForTUI()
	defer hlog.RestoreAfterTUI(prev)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	fm := final.(wizardModel)
	if fm.canceled {
		if fm.cancelReason == errNoEnumPerms {
			return &WizardResult{
				APIURL:      fm.apiURL,
				Token:       fm.token,
				Account:     fm.accountID,
				RegURL:      fm.regURL,
				ScopeNotSet: true,
			}, nil
		}
		return nil, nil
	}
	if fm.step != stepDone {
		return nil, nil
	}
	if fm.scopeSkipped {
		return &WizardResult{
			APIURL:       fm.apiURL,
			Token:        fm.token,
			Account:      fm.accountID,
			RegURL:       fm.regURL,
			OrgID:        fm.orgID,
			ScopeNotSet:  true,
			ScopeSkipped: true,
		}, nil
	}
	return &WizardResult{
		APIURL:  fm.apiURL,
		Token:   fm.token,
		Account: fm.accountID,
		RegURL:  fm.regURL,
		OrgID:   fm.orgID,
		Project: fm.projectID,
	}, nil
}

// SetWizardInput carries the pre-filled credentials and current defaults for RunSetWizard.
type SetWizardInput struct {
	APIURL    string
	Token     string
	AccountID string
	AuthType  pkgauth.AuthType
	RegURL    string
	OrgID     string
	ProjectID string
}

// RunSetWizard starts the wizard at the org-pick step using already-validated credentials.
// Pre-selects the currently saved org and project. Returns (nil, nil) if canceled.
func RunSetWizard(ctx *cmdctx.Ctx, in *SetWizardInput) (*WizardResult, error) {
	orgs, err := fetchOrgItems(ctx, in.APIURL, in.Token, in.AccountID, in.AuthType)
	if err != nil {
		return nil, err
	}

	st := newWizardStyles()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = st.prompt

	newList := func(title string) list.Model {
		delegate := list.NewDefaultDelegate()
		delegate.ShowDescription = false
		delegate.SetHeight(1)
		delegate.SetSpacing(0)
		delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(lipgloss.Color(tui.CLIAccent)).BorderLeftForeground(lipgloss.Color(tui.CLIAccent))
		l := list.New(nil, delegate, 60, 20)
		l.Title = title
		l.Styles.Title = st.title
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(true)
		return l
	}

	orgListModel := newList("Select an organization")
	orgItems := make([]list.Item, len(orgs))
	for i, o := range orgs {
		orgItems[i] = o
	}
	orgListModel.SetItems(orgItems)
	for i, o := range orgs {
		if o.id == in.OrgID {
			orgListModel.Select(i)
			break
		}
	}

	m := wizardModel{
		st:               st,
		step:             stepOrgPick,
		spin:             sp,
		orgList:          orgListModel,
		projList:         newList("Select a project"),
		apiURL:           in.APIURL,
		token:            in.Token,
		accountID:        in.AccountID,
		authType:         in.AuthType,
		regURL:           in.RegURL,
		currentOrgID:     in.OrgID,
		currentProjectID: in.ProjectID,
		setMode:          true,
		cmdCtx:           ctx,
		width:            80,
		height:           24,
	}

	prev := hlog.SilenceForTUI()
	defer hlog.RestoreAfterTUI(prev)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	fm := final.(wizardModel)
	if fm.canceled || fm.step != stepDone {
		return nil, fm.cancelReason
	}
	return &WizardResult{
		APIURL:  fm.apiURL,
		Token:   fm.token,
		Account: fm.accountID,
		RegURL:  fm.regURL,
		OrgID:   fm.orgID,
		Project: fm.projectID,
	}, nil
}

// --- helpers ---


func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
