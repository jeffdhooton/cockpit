package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

const (
	sessionsSplitWidth = 90 // at or above: sessions left, detail right
	sessionsListWidth  = 32
	previewLines       = 40
)

// panePreview is the bounded screen read of the selected pane.
type panePreview struct {
	host, paneID, text, err string
	seq                     int
	loading                 bool
}

type panePreviewMsg struct {
	host, paneID, text string
	seq                int
	err                error
}

// sessionsModel is the session view: the workspace tree, a session
// selection held by key, a pane selection held by id, and the preview of
// the selected pane.
type sessionsModel struct {
	ws        sources.Workspace
	selected  string
	pane      string
	paneFocus bool
	filter    textinput.Model
	query     string
	preview   panePreview
	scroll    int
	message   string
}

func newSessionsModel() sessionsModel {
	f := textinput.New()
	f.Placeholder = "filter host or session"
	f.CharLimit = 64
	f.Width = 24
	return sessionsModel{filter: f}
}

// refresh installs a new tree and re-resolves both selections.
func (s *sessionsModel) refresh(ws sources.Workspace) {
	s.ws = ws
	s.resolve()
}

func (s *sessionsModel) matches(host string, sv sources.SessionView) bool {
	q := strings.ToLower(strings.TrimSpace(s.query))
	return q == "" || strings.Contains(strings.ToLower(hostLabel(host)+" "+sv.Name), q)
}

// sessions flattens the hosts in order, honouring the filter.
func (s *sessionsModel) sessions() []sources.SessionView {
	var out []sources.SessionView
	for _, h := range s.ws.Hosts {
		for _, sv := range h.Sessions {
			if s.matches(h.Name, sv) {
				out = append(out, sv)
			}
		}
	}
	return out
}

func (s *sessionsModel) index() int {
	for i, sv := range s.sessions() {
		if sv.Key == s.selected {
			return i
		}
	}
	return -1
}

// resolve re-finds the selection by key and the pane by id; a vanished
// session lands the cursor on the neighbour at the same index.
func (s *sessionsModel) resolve() {
	list := s.sessions()
	if len(list) == 0 {
		s.selected, s.pane = "", ""
		return
	}
	i := s.index()
	if i < 0 {
		i = clampInt(s.scroll, 0, len(list)-1)
		s.selected = list[i].Key
		s.pane = ""
	}
	cur := list[i]
	found := false
	for _, p := range cur.Panes {
		if p.PaneID == s.pane {
			found = true
		}
	}
	if !found {
		s.pane = defaultPane(cur)
	}
}

// defaultPane is the session's active pane, else the first.
func defaultPane(sv sources.SessionView) string {
	for _, p := range sv.Panes {
		if p.Active {
			return p.PaneID
		}
	}
	if len(sv.Panes) > 0 {
		return sv.Panes[0].PaneID
	}
	return ""
}

func (s *sessionsModel) current() *sources.SessionView {
	for _, sv := range s.sessions() {
		if sv.Key == s.selected {
			c := sv
			return &c
		}
	}
	return nil
}

func (s *sessionsModel) currentPane() *sources.PaneView {
	cur := s.current()
	if cur == nil {
		return nil
	}
	for _, p := range cur.Panes {
		if p.PaneID == s.pane {
			c := p
			return &c
		}
	}
	return nil
}

// hostOf returns the host view a session belongs to.
func (s *sessionsModel) hostOf(name string) *sources.HostView {
	for i := range s.ws.Hosts {
		if s.ws.Hosts[i].Name == name {
			return &s.ws.Hosts[i]
		}
	}
	return nil
}

// move steps the session or the pane selection, by focus.
func (s *sessionsModel) move(delta int) {
	s.message = ""
	if s.paneFocus {
		cur := s.current()
		if cur == nil || len(cur.Panes) == 0 {
			return
		}
		i := 0
		for j, p := range cur.Panes {
			if p.PaneID == s.pane {
				i = j
			}
		}
		i = clampInt(i+delta, 0, len(cur.Panes)-1)
		if cur.Panes[i].PaneID != s.pane {
			s.pane = cur.Panes[i].PaneID
			s.preview = panePreview{}
		}
		return
	}
	list := s.sessions()
	if len(list) == 0 {
		return
	}
	i := clampInt(s.index()+delta, 0, len(list)-1)
	if list[i].Key != s.selected {
		s.selected = list[i].Key
		s.pane = defaultPane(list[i])
		s.preview = panePreview{}
	}
	s.scroll = i
}

// selectIndex picks the i-th live session (digit hotkeys), returning it.
func (s *sessionsModel) selectIndex(i int) *sources.SessionView {
	list := s.sessions()
	if i < 0 || i >= len(list) {
		return nil
	}
	s.selected = list[i].Key
	s.pane = defaultPane(list[i])
	s.preview = panePreview{}
	s.scroll = i
	return s.current()
}

// applyPreview folds a preview reply in, dropping one for another pane or
// an older request.
func (s *sessionsModel) applyPreview(msg panePreviewMsg) bool {
	if msg.paneID != s.pane || msg.seq < s.preview.seq {
		return false
	}
	s.preview.loading = false
	s.preview.paneID = msg.paneID
	if msg.err != nil {
		s.preview.err = msg.err.Error()
		return true
	}
	s.preview.err = ""
	s.preview.text = trimPadding(sources.StripControl(msg.text))
	return true
}

// --- rendering ---

func statusDotFor(st sources.AgentStatusName) string {
	switch st {
	case "needs_input":
		return StatusDot("needs you", VariantWarning)
	case "working":
		return StatusDot("working", VariantAccent)
	case "idle":
		return StatusDot("idle", VariantNeutral)
	}
	return StatusRing("unknown", VariantMuted)
}

func sessionTitle(sv *sources.SessionView) string {
	if sv == nil {
		return "Select a session"
	}
	return hostLabel(sv.Host) + " / " + clean(sv.Name)
}

// view renders the whole view into width × height, panels included.
func (s *sessionsModel) view(width, height int, now time.Time, filtering bool) string {
	if width >= sessionsSplitWidth {
		left := s.listView(sessionsListWidth-4, height-3, now, filtering)
		right := s.detailView(width-sessionsListWidth-4, height-3, now)
		lp := RenderPanel("Sessions", left, sessionsListWidth, height, !s.paneFocus)
		rp := RenderPanel(sessionTitle(s.current()), right, width-sessionsListWidth, height, s.paneFocus)
		return lipgloss.JoinHorizontal(lipgloss.Top, lp, rp)
	}
	listH := height / 2
	if listH < 5 {
		listH = 5
	}
	if height-listH < 4 {
		listH = height - 4
	}
	if listH < 3 {
		listH = 3
	}
	left := s.listView(width-4, listH-3, now, filtering)
	right := s.detailView(width-4, height-listH-3, now)
	return lipgloss.JoinVertical(lipgloss.Left,
		RenderPanel("Sessions", left, width, listH, !s.paneFocus),
		RenderPanel(sessionTitle(s.current()), right, width, height-listH, s.paneFocus))
}

// listView draws hosts, sessions and dormant repos, scrolled to the
// selection.
func (s *sessionsModel) listView(inner, height int, now time.Time, filtering bool) string {
	var lines []string
	if filtering || s.query != "" {
		lines = append(lines, clip("/ "+s.filter.View(), inner))
	}
	digit := 0
	selIdx := -1
	for _, h := range s.ws.Hosts {
		header := MutedText.Render(strings.ToUpper(clip(hostLabel(h.Name), 14)))
		switch h.Observation {
		case sources.ObservationUnavailable:
			header += " " + WarningText.Render("⚠ unavailable")
		case sources.ObservationStale:
			header += " " + WarningText.Render("⚠ stale "+ageOf(h.ObservedAt, now))
		default:
			if h.Name != "" {
				header += " " + SuccessText.Render("● up")
			}
		}
		lines = append(lines, clip(header, inner))
		for _, sv := range h.Sessions {
			if !s.matches(h.Name, sv) {
				continue
			}
			selected := sv.Key == s.selected
			if selected {
				selIdx = len(lines)
			}
			digit++
			key := "  "
			if lbl := hotkeyLabel(digit); lbl != "" {
				key = MutedText.Render(lbl) + " "
			}
			nameStyle := BoldText
			if selected {
				nameStyle = BoldText.Foreground(ColorAccent)
			}
			if h.Observation != sources.ObservationFresh {
				nameStyle = MutedText
			}
			extra := ""
			if sv.Agents > 0 {
				extra += fmt.Sprintf(" %d", sv.Agents)
				if sv.Agents == 1 {
					extra += " agent"
				} else {
					extra += " agents"
				}
			}
			if sv.ProcessesTotal > 0 {
				extra += fmt.Sprintf(" ⚙%d/%d", sv.ProcessesRunning, sv.ProcessesTotal)
			}
			nameW := 10
			if inner > 40 {
				nameW = 16
			}
			line := RowCursor(selected) + key + padRight(nameStyle.Render(clip(clean(sv.Name), nameW)), nameW) + " " + statusDotFor(sv.Status) + MutedText.Render(extra)
			lines = append(lines, clip(line, inner))
		}
		if len(h.Dormant) > 0 {
			names := make([]string, 0, len(h.Dormant))
			for _, d := range h.Dormant {
				names = append(names, clean(d.Name))
			}
			lines = append(lines, clip(MutedText.Render(fmt.Sprintf("  dormant (%d): %s", len(h.Dormant), strings.Join(names, " "))), inner))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, MutedText.Render("No sessions observed"))
	}
	if height < 1 {
		height = 1
	}
	start := 0
	if selIdx >= height {
		start = selIdx - height + 1
	}
	end := start + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

// detailView draws the selected session's header, panes and preview.
func (s *sessionsModel) detailView(inner, height int, now time.Time) string {
	cur := s.current()
	if cur == nil {
		return MutedText.Render("Nothing selected")
	}
	var lines []string
	head := ""
	if cur.Project != nil {
		head = PurpleText.Render(clean(cur.Project.Branch))
		if cur.Project.Dirty > 0 {
			head += " " + StatusDirty.Render(fmt.Sprintf("✗%d", cur.Project.Dirty))
		} else {
			head += " " + StatusClean.Render("✓")
		}
		if cur.Project.Unpushed > 0 {
			head += " " + StatusUnpushed.Render(fmt.Sprintf("↑%d", cur.Project.Unpushed))
		}
		head += "  "
	}
	meta := fmt.Sprintf("%s · server %s · %d windows", cur.ID, cur.Generation, cur.Windows)
	if cur.Attached {
		meta += " · attached"
	}
	if cur.LegacyReport {
		meta += " · session-level report (older hook)"
	}
	if h := s.hostOf(cur.Host); h != nil && h.Observation != sources.ObservationFresh {
		meta += " · " + string(h.Observation)
	}
	lines = append(lines, clip(head+MutedText.Render(meta), inner))
	narrow := inner < 60
	if !narrow {
		lines = append(lines, "")
	}
	nameW, statusW, qualW := 12, 18, 24
	if narrow {
		nameW, statusW, qualW = 9, 14, 8
	}
	for _, p := range cur.Panes {
		selected := p.PaneID == s.pane
		mark := " "
		if p.Attention {
			mark = WarningText.Render("!")
		}
		nameStyle := lipgloss.NewStyle().Foreground(ColorFg)
		if selected {
			nameStyle = AccentText
		}
		name := padRight(nameStyle.Render(clip(clean(p.WindowName), nameW)), nameW)
		id := padRight(MutedText.Render(p.WindowID), 5)
		var status, qual string
		switch p.Kind {
		case sources.PaneAgent:
			status = statusDotFor(p.Agent.Status)
			qual = p.Agent.Engine
			if p.Agent.Reported && !p.Agent.ReportedAt.IsZero() {
				qual += " · " + ageOf(p.Agent.ReportedAt, now)
			}
		case sources.PaneProcess:
			status = stateStyleFor(sources.ProcessInfo{Outcome: p.Process.Outcome}).Render(clip(p.Process.Display, statusW))
			qual = "process"
			if !p.Process.Managed {
				qual += " · unmanaged"
			}
		case sources.PaneShell:
			status = MutedText.Render("shell")
			qual = clean(p.Command)
		default:
			status = MutedText.Render(clip(clean(p.Command), statusW))
		}
		line := RowCursor(selected && s.paneFocus) + mark + id + name + " " + padRight(status, statusW) + " " + MutedText.Render(clip(qual, qualW))
		lines = append(lines, clip(line, inner))
	}
	if p := s.currentPane(); p != nil && height-len(lines) > 3 {
		title := "─── " + clean(p.WindowName) + " ── " + p.PaneID + " "
		if p.Path != "" && !narrow {
			title += "· " + clean(p.Path) + " "
		}
		lines = append(lines, MutedText.Render(clip(title+strings.Repeat("─", inner), inner)))
		room := height - len(lines)
		if s.message != "" {
			room--
		}
		switch {
		case s.preview.err != "":
			lines = append(lines, WarningText.Render(clip("preview unavailable: "+s.preview.err, inner)))
		case s.preview.text == "" && s.preview.loading:
			lines = append(lines, MutedText.Render("reading…"))
		case s.preview.text == "":
			lines = append(lines, MutedText.Render("(no preview)"))
		default:
			body := strings.Split(s.preview.text, "\n")
			if room < 1 {
				room = 1
			}
			if len(body) > room {
				body = body[len(body)-room:]
			}
			for _, l := range body {
				lines = append(lines, clip(l, inner))
			}
		}
	}
	if s.message != "" {
		lines = append(lines, WarningText.Render(clip(s.message, inner)))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// --- model wiring ---

// openSessions shows the session view, rebuilt from current state.
func (m *Model) openSessions() {
	m.view = ViewSessions
	m.sess.refresh(sources.BuildWorkspace(m.attentionInput()))
}

// fetchPanePreview reads the selected pane's visible screen through its
// host's runner, guarded by sequence and pane id. It never runs against a
// host whose last read failed.
func (m *Model) fetchPanePreview() tea.Cmd {
	p := m.sess.currentPane()
	cur := m.sess.current()
	if p == nil || cur == nil {
		return nil
	}
	if h := m.sess.hostOf(cur.Host); h == nil || h.Observation != sources.ObservationFresh {
		return nil
	}
	m.sess.preview.seq++
	m.sess.preview.loading = true
	seq, host, paneID := m.sess.preview.seq, cur.Host, p.PaneID
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := svc.RunnerFor(config.RepoConfig{Host: host})
		if err != nil {
			return panePreviewMsg{host: host, paneID: paneID, seq: seq, err: err}
		}
		out, err := r.Run(ctx, sources.CapturePaneVisibleArgs(paneID)...)
		if err != nil {
			return panePreviewMsg{host: host, paneID: paneID, seq: seq, err: err}
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > previewLines {
			lines = lines[len(lines)-previewLines:]
		}
		return panePreviewMsg{host: host, paneID: paneID, seq: seq, text: strings.Join(lines, "\n")}
	}
}

func (m *Model) handleSessionsKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.sess
	switch msg.String() {
	case "j", "down":
		s.move(1)
		return m.fetchPanePreview()
	case "k", "up":
		s.move(-1)
		return m.fetchPanePreview()
	case "tab":
		s.paneFocus = true
	case "shift+tab":
		s.paneFocus = false
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
		n := int(msg.String()[0] - '0')
		if n == 0 {
			n = 10
		}
		if sv := s.selectIndex(n - 1); sv != nil {
			return m.attachSession(*sv)
		}
	case "enter":
		if s.paneFocus {
			if p := s.currentPane(); p != nil {
				cur := s.current()
				return m.attachCmd(sources.AttachTarget{Host: cur.Host, Generation: p.Generation, Session: cur.Name, SessionID: cur.ID, WindowID: p.WindowID, PaneID: p.PaneID})
			}
			return nil
		}
		if cur := s.current(); cur != nil {
			return m.attachSession(*cur)
		}
	case "o":
		return m.openProjectCmd()
	case "p":
		if cur := s.current(); cur != nil {
			return m.openProcessesFor(cur.Host, cur.Name, "")
		}
	case "a":
		return m.openAttention()
	case "l":
		if p := s.currentPane(); p != nil {
			cur := s.current()
			m.pushReturn()
			m.procs.open("", cur.Host, cur.Name, true, "")
			if _, configured := m.config.RepoOn(cur.Host, cur.Name); configured {
				m.procs.open(config.RepoConfig{Host: cur.Host, Label: cur.Name}.Key(), cur.Host, cur.Name, false, "")
			}
			m.procs.selected = "win:" + p.WindowID
			m.procs.fullOut = true
			m.view = ViewProcesses
			return m.fetchProcObs()
		}
	case "n":
		m.mode = ModeNewSession
		m.newSessionStep = 0
		m.newSessionPath = ""
		m.newSessionErr = ""
		m.newSessionInput.SetValue("")
		m.newSessionInput.Placeholder = "~/workspace/my-project"
		m.newSessionInput.Focus()
	case "/":
		m.mode = ModeSessionsFilter
		s.filter.SetValue(s.query)
		s.filter.Focus()
	case "c":
		return m.startCapture()
	case "g":
		m.view = ViewGrid
		return m.fetchPreview()
	case "r":
		return m.refreshAll()
	case "esc":
		if s.query != "" {
			s.query = ""
			s.filter.SetValue("")
			s.resolve()
		}
	case "q":
		return tea.Quit
	}
	return nil
}

// attachSession attaches to a session's selected pane, creating nothing.
func (m *Model) attachSession(sv sources.SessionView) tea.Cmd {
	target := sources.AttachTarget{Host: sv.Host, Generation: sv.Generation, Session: sv.Name, SessionID: sv.ID}
	if p := m.sess.currentPane(); p != nil {
		target.WindowID, target.PaneID = p.WindowID, p.PaneID
	}
	return m.attachCmd(target)
}

// openProjectCmd is the explicit launch: the selected session's project,
// or the first dormant project, through the jump path with reconcile.
func (m *Model) openProjectCmd() tea.Cmd {
	if cur := m.sess.current(); cur != nil {
		repo, ok := m.config.RepoOn(cur.Host, cur.Name)
		if !ok {
			m.sess.message = "Not a configured project; nothing to start"
			return nil
		}
		return m.jumpCmd(repo)
	}
	for _, h := range m.sess.ws.Hosts {
		if len(h.Dormant) > 0 {
			if repo, ok := m.config.RepoOn(h.Name, h.Dormant[0].Name); ok {
				return m.jumpCmd(repo)
			}
		}
	}
	return nil
}

func (m *Model) jumpCmd(repo config.RepoConfig) tea.Cmd {
	if repo.Host != "" {
		host, ok := m.config.Host(repo.Host)
		if !ok {
			return nil
		}
		return m.jumpRemoteCmd(host, repo)
	}
	svc := m.svc
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpRepo(svc, repo)} }
}

func (m *Model) handleSessionsFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.sess.filter.Blur()
		m.sess.filter.SetValue("")
		m.sess.query = ""
		m.mode = ModeNavigation
		m.sess.resolve()
	case "enter":
		m.sess.filter.Blur()
		m.mode = ModeNavigation
	}
	return nil
}

func (m Model) sessionsView() string {
	body := m.height - 1
	if body < 5 {
		body = 5
	}
	content := m.sess.view(m.width, body, m.now(), m.mode == ModeSessionsFilter)
	hints := SessionsKeyhintsView(m.width, m.sess.paneFocus, m.attn.badge())
	switch {
	case m.mode == ModeCapture:
		hints = "  " + AccentText.Render("capture ›") + " " + m.captureInput.View()
	case m.transientErr != "":
		hints = WarningText.Render(m.transientErr)
	}
	return lipgloss.JoinVertical(lipgloss.Left, content, hints)
}
