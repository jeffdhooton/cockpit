package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// attentionInput assembles the queue's input from the model's polls: this
// machine as one host report, each configured host from its last poll, and
// GitHub and Hermes as read. It is the TUI half of "same collector": the
// daemon assembles the identical structure synchronously and both call
// sources.DeriveAttention.
func (m Model) attentionInput() sources.AttentionInput {
	now := m.now()
	in := sources.AttentionInput{
		Config:      m.config.Signals,
		SelfSession: m.config.General.SessionName,
		Now:         now,
		GitHub:      m.github,
		GitHubAt:    m.githubAt,
		HermesAt:    m.hermesAt,
		Spine:       m.spine,
		SpineAt:     m.spineAt,
	}

	local := sources.HostReport{
		Host:        "",
		Outcome:     m.localOutcome,
		Err:         m.localErr,
		ObservedAt:  m.localObservedAt,
		Sessions:    m.sessions.Sessions,
		Panes:       m.localPanes,
		Git:         m.repos.Repos,
		Processes:   m.procObs,
		ProcessErrs: m.procErrs,
	}
	if m.localOutcome == "" {
		local.Outcome = sources.ObservationUnavailable
	}
	in.Hosts = append(in.Hosts, local)

	for _, h := range m.config.Hosts {
		st, polled := m.hosts[h.Name]
		rep := st.poll.Report
		rep.Host = h.Name
		switch {
		case !polled:
			rep.Outcome = sources.ObservationUnavailable
			rep.Err = "not polled yet"
		case st.unreachable:
			rep.Outcome = sources.ObservationUnavailable
			if st.poll.Err != nil {
				rep.Err = st.poll.Err.Error()
			}
		}
		in.Hosts = append(in.Hosts, rep)
	}

	for _, h := range m.config.Hermes {
		if st, ok := m.hermes[h.Label]; ok {
			st.Host = h.Host
			in.Hermes = append(in.Hermes, st)
		}
	}
	return in
}

// recomputeAttention re-derives the queue and the workspace tree from
// current state. It runs on every data message so the badge is always
// current; the queue and session views read the same derivations. It
// returns the preview fetch the session view needs when it is open.
func (m *Model) recomputeAttention() tea.Cmd {
	m.attn.remember()
	in := m.attentionInput()
	m.attn.refresh(sources.DeriveAttention(in), m.now())
	m.sess.refresh(sources.BuildWorkspace(in))
	if m.view == ViewSessions && m.sess.preview.text == "" && !m.sess.preview.loading {
		return m.fetchPanePreview()
	}
	return nil
}

// refreshAll re-polls every source, coalescing into the existing commands.
func (m Model) refreshAll() tea.Cmd {
	return tea.Batch(m.fetchTmux(), m.fetchGit(), m.fetchGitHub(), m.fetchProcesses(), m.fetchHosts(), m.fetchHermes(), m.fetchSpine())
}

// openAttention shows the queue over every host, whatever grid was open.
func (m *Model) openAttention() tea.Cmd {
	m.pushReturn()
	m.view = ViewAttention
	m.attn.detail = nil
	m.attn.message = ""
	return tea.Batch(m.recomputeAttention(), m.refreshAll())
}

// openProcessesFor opens the panel for a project key or a bare session. A
// host box is not a project. Opening only reads.
func (m *Model) openProcessesFor(host, label, selectProcess string) tea.Cmd {
	if label == "" {
		return nil
	}
	repo, configured := m.config.RepoOn(host, label)
	key := ""
	if configured {
		key = repo.Key()
	}
	m.pushReturn()
	m.view = ViewProcesses
	m.procs.open(key, host, label, !configured, selectProcess)
	if configured && m.procObs != nil && host == "" {
		if obs, ok := m.procObs[key]; ok && m.procs.obs == nil {
			o := obs
			m.procs.obs = &o
			m.procs.observedAt = obs.ObservedAt
			m.procs.resolveSelection()
		}
	}
	return tea.Batch(m.fetchProcObs())
}

// --- attention keys ---

func (m *Model) handleAttentionKey(msg tea.KeyMsg) tea.Cmd {
	a := &m.attn
	if a.detail != nil {
		switch msg.String() {
		case "esc", "q", "backspace":
			a.detail = nil
			return nil
		case "o":
			if a.detail.Target.Type == "ci" {
				return openURLCmd(a.detail.Target.RunURL)
			}
		case "enter":
			item := *a.detail
			switch item.Target.Type {
			case "session", "pane", "project":
				if !item.Actionable() {
					a.message = "This item is stale; refresh first"
					return nil
				}
				return m.attachCmd(itemTarget(item))
			case "spine":
				return m.openSpine()
			}
		}
		return nil
	}
	switch msg.String() {
	case "j", "down":
		a.move(1)
		a.message = ""
	case "k", "up":
		a.move(-1)
		a.message = ""
	case "tab":
		a.switchTab()
	case "/":
		m.mode = ModeAttentionFilter
		a.filter.SetValue(a.query)
		a.filter.Focus()
	case "r":
		return m.refreshAll()
	case "esc", "q":
		m.popReturn()
		return tea.Batch(m.fetchPreview(), m.fetchPanePreview())
	case "enter":
		if a.resolved != nil {
			// A queued Enter must never act on the row that took the
			// resolved item's place.
			return nil
		}
		item := a.current()
		if item == nil {
			return nil
		}
		return m.attentionAction(*item)
	}
	return nil
}

func (m *Model) handleAttentionFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.attn.filter.Blur()
		m.attn.filter.SetValue("")
		m.attn.query = ""
		m.mode = ModeNavigation
		m.attn.resolveSelection()
	case "enter":
		m.attn.filter.Blur()
		m.mode = ModeNavigation
	}
	return nil
}

// attentionAction performs a row's displayed primary action. None of the
// branches starts, restarts or reconciles anything.
func (m *Model) attentionAction(item sources.AttentionItem) tea.Cmd {
	if !item.Actionable() {
		m.attn.message = "Source unavailable; showing last-known state"
		return nil
	}
	switch item.Action {
	case sources.ActionOpenAgent:
		return m.attachCmd(itemTarget(item))
	case sources.ActionInspectProcess:
		return m.openProcessesFor(item.Target.Host, item.Target.Session, item.Target.Process)
	case sources.ActionOpenSpine:
		return m.openSpine()
	default:
		i := item
		m.attn.detail = &i
		return nil
	}
}

// --- processes keys ---

func (m *Model) handleProcessesKey(msg tea.KeyMsg) tea.Cmd {
	p := &m.procs
	if p.fullOut {
		visible := m.height - 2
		switch msg.String() {
		case "esc", "q", "l":
			p.fullOut = false
		case "j", "down":
			p.scrollOutput(1, visible)
		case "k", "up":
			p.scrollOutput(-1, visible)
		case "d", "pgdown":
			p.scrollOutput(visible/2, visible)
		case "u", "pgup":
			p.scrollOutput(-visible/2, visible)
		case "G":
			p.output.scroll = -1
		case "L":
			return m.fetchProcOutput(2000)
		case "r":
			return tea.Batch(m.fetchProcObs(), m.fetchProcOutput(p.output.lines))
		}
		return nil
	}
	switch msg.String() {
	case "j", "down":
		p.move(1)
		p.message = ""
		return m.fetchProcOutput(0)
	case "k", "up":
		p.move(-1)
		p.message = ""
		return m.fetchProcOutput(0)
	case "esc", "q":
		m.popReturn()
		return tea.Batch(m.recomputeAttention(), m.fetchPreview(), m.fetchPanePreview())
	case "r":
		return tea.Batch(m.fetchProcObs(), m.fetchProcOutput(p.output.lines))
	case "c":
		p.showCmd = !p.showCmd
	case "l":
		cur := p.current()
		if cur == nil || (cur.info.WindowID == "" && cur.info.WindowIndex < 0) {
			p.message = "Not started; there is no output to read"
			return nil
		}
		p.fullOut = true
		return m.fetchProcOutput(0)
	case "enter":
		cur := p.current()
		if cur == nil {
			return nil
		}
		if cur.info.WindowID == "" && cur.info.WindowIndex < 0 {
			p.message = "Not started: press s to start it first"
			return nil
		}
		if p.err != "" {
			p.message = "Last read failed; refresh before attaching"
			return nil
		}
		return m.attachCmd(sources.AttachTarget{
			Host: p.host, Generation: cur.info.Generation, Session: p.label,
			WindowID: cur.info.WindowID, PaneID: cur.info.PaneID,
		})
	case "s":
		if !p.can("s") {
			p.message = m.whyNot("s")
			return nil
		}
		return m.runProcAction("start", p.current().info.Name, "")
	case "x":
		if !p.can("x") {
			p.message = m.whyNot("x")
			return nil
		}
		p.confirm = &procConfirm{kind: "stop", process: p.current().info.Name, windowID: p.current().info.WindowID}
		m.mode = ModeConfirm
	case "R":
		if !p.can("R") {
			p.message = m.whyNot("R")
			return nil
		}
		p.confirm = &procConfirm{kind: "restart", process: p.current().info.Name, windowID: p.current().info.WindowID}
		m.mode = ModeConfirm
	case "A":
		if !p.can("A") {
			p.message = m.whyNot("A")
			return nil
		}
		info := p.current().info
		p.confirm = &procConfirm{
			kind: "adopt", process: info.Name, windowID: info.WindowID,
			detail: fmt.Sprintf("window %s (index %d, pane %s, pid %d, %d pane(s))", info.WindowID, info.WindowIndex, info.PaneID, info.PanePID, info.Panes),
		}
		m.mode = ModeConfirm
	}
	return nil
}

// whyNot explains a refused key on the selected row.
func (m *Model) whyNot(key string) string {
	p := &m.procs
	cur := p.current()
	switch {
	case p.unmanaged:
		return "Open a configured project to manage its processes"
	case cur == nil:
		return "Nothing selected"
	case p.err != "":
		return "Last read failed; actions are disabled until a fresh read"
	case !cur.info.Configured:
		return "Unmanaged window: inspection only"
	}
	if _, busy := p.pending[cur.key]; busy {
		return "An action is already in progress for this process"
	}
	info := cur.info
	switch key {
	case "s":
		if info.Outcome == sources.OutcomeRunning {
			return "Already running"
		}
		if info.Adoptable {
			return "A window of this name exists that cockpit did not start; press A to adopt it"
		}
		if info.Split {
			return "The window is split; restarting it would kill the other panes"
		}
	case "x", "R":
		if info.WindowID == "" && info.WindowIndex < 0 {
			return "Not running; press s to start it"
		}
		if info.Outcome == sources.OutcomeExited || info.Outcome == sources.OutcomeCompleted {
			return "Not running (exited); press s to relaunch it in its window"
		}
		if info.Adoptable {
			return "Not started by cockpit: inspect only, or press A to adopt it"
		}
		if info.Split {
			return "The window is split; stop/restart are disabled to protect the other panes"
		}
		if info.Ambiguous {
			return "Several windows claim this process; close the extras first"
		}
		if key == "R" && info.Outcome != sources.OutcomeRunning {
			return "Not running; press s to start it"
		}
	case "A":
		if info.Managed {
			return "Already managed by cockpit"
		}
		return "No unmanaged window named for this process"
	}
	return "Not available for this row"
}

func (m *Model) handleConfirmKey(msg tea.KeyMsg) tea.Cmd {
	p := &m.procs
	c := p.confirm
	if c == nil {
		m.mode = ModeNavigation
		return nil
	}
	switch msg.String() {
	case "esc", "n", "q":
		p.confirm = nil
		m.mode = ModeNavigation
	case "left", "right", "h", "l", "tab":
		c.armed = !c.armed
	case "y":
		c.armed = true
		fallthrough
	case "enter":
		if !c.armed {
			p.confirm = nil
			m.mode = ModeNavigation
			return nil
		}
		p.confirm = nil
		m.mode = ModeNavigation
		return m.runProcAction(c.kind, c.process, c.windowID)
	}
	return nil
}

// --- views ---

func (m Model) attentionView() string {
	body := m.height - 1
	if body < 3 {
		body = 3
	}
	content := m.attn.view(m.width, body-3, m.now(), m.mode == ModeAttentionFilter)
	title := "Attention"
	if m.attn.detail != nil {
		title = "Attention · details"
	} else if m.attn.tab == 1 {
		title = "Housekeeping"
	}
	page := RenderPanel(title, content, m.width, body, true)
	hints := AttentionKeyhintsView(m.width, m.attn.detail != nil, m.mode == ModeAttentionFilter)
	if m.transientErr != "" {
		hints = WarningText.Render(m.transientErr)
	}
	return lipgloss.JoinVertical(lipgloss.Left, page, hints)
}

func (m Model) processesView() string {
	body := m.height - 1
	if body < 3 {
		body = 3
	}
	content := m.procs.view(m.width, body-3, m.now())
	title := "Processes"
	if m.procs.fullOut {
		title = "Output"
	}
	page := RenderPanel(title, content, m.width, body, true)
	hints := ProcessKeyhintsView(m.width, m.procs.fullOut, m.procs.actions())
	if m.transientErr != "" {
		hints = WarningText.Render(m.transientErr)
	}
	return lipgloss.JoinVertical(lipgloss.Left, page, hints)
}

// jumpRemoteCmd prepares a remote project under its lock — session, then
// reconciled processes — and opens the local view onto it.
func (m Model) jumpRemoteCmd(host config.HostConfig, repo config.RepoConfig) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		ctx := context.Background()
		local := sources.DefaultRunner()
		remote, err := svc.RunnerFor(repo)
		if err != nil {
			return tmuxSwitchResultMsg{Err: err}
		}
		created, _, err := svc.Prepare(ctx, remote, repo)
		if err != nil {
			return tmuxSwitchResultMsg{Err: fmt.Errorf("jump %s: %w", repo.Key(), err)}
		}
		if created {
			_, _ = remote.Run(ctx, sources.SelectFirstWindowArgs(repo.Label)...)
		}
		return tmuxSwitchResultMsg{Err: sources.OpenRemoteView(ctx, local, host, repo.Label)}
	}
}

var _ = time.Second
