package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
)

var validLabel = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// PanelID identifies a panel.
type PanelID int

const (
	PanelSessions PanelID = iota
	PanelRepos
	PanelToday
	PanelInbox
	PanelSignals
	panelCount // sentinel
)

// Mode represents the TUI interaction mode.
type Mode int

const (
	ModeNavigation Mode = iota
	ModeCapture
	ModeNewSession
	ModeSearch
)

// Layout holds calculated panel dimensions.
type Layout struct {
	SessionsH    int // cards + preview
	MiddleH      int // repos | today row height
	BottomH      int // inbox | signals row height
	KeyhintsH    int
	LeftW        int // repos width
	RightW       int // today width
	BottomLeftW  int // notes width (2/3)
	BottomRightW int // signals width (1/3)
}

// CalculateLayout computes panel sizes based on terminal dimensions.
// It guarantees that SessionsH + MiddleH + BottomH + KeyhintsH == height.
func CalculateLayout(width, height, repoCount int) Layout {
	l := Layout{KeyhintsH: 1}

	usable := height - l.KeyhintsH
	if usable < 15 {
		// Absolute minimum: give each section something
		l.SessionsH = 5
		l.MiddleH = 5
		l.BottomH = usable - 10
		if l.BottomH < 3 {
			l.BottomH = 3
		}
		l.LeftW = width / 2
		l.RightW = width - l.LeftW
		l.BottomLeftW = width * 2 / 3
		l.BottomRightW = width - l.BottomLeftW
		return l
	}

	// Minimums — below these a panel is unusable
	const minSessions = 8
	const minMiddle = 6
	const minBottom = 5
	const minTotal = minSessions + minMiddle + minBottom

	// Desired ratios vary with terminal height
	sessionsPct := 45
	middlePct := 30
	bottomPct := 25
	switch {
	case height < 45:
		sessionsPct = 30
		middlePct = 38
		bottomPct = 32
	case height < 55:
		sessionsPct = 35
		middlePct = 35
		bottomPct = 30
	case height < 65:
		sessionsPct = 38
		middlePct = 33
		bottomPct = 29
	}
	_ = bottomPct // ratios are applied below

	if usable <= minTotal {
		// Not enough room for ratios — just use minimums
		l.SessionsH = minSessions
		l.MiddleH = minMiddle
		l.BottomH = usable - minSessions - minMiddle
		if l.BottomH < 3 {
			l.BottomH = 3
		}
	} else {
		// Apply ratios — bottom uses explicit pct, remainder goes to sessions
		l.SessionsH = usable * sessionsPct / 100
		l.MiddleH = usable * middlePct / 100
		l.BottomH = usable * bottomPct / 100

		// Enforce minimums, then redistribute excess back
		if l.SessionsH < minSessions {
			l.SessionsH = minSessions
		}
		if l.MiddleH < minMiddle {
			l.MiddleH = minMiddle
		}
		if l.BottomH < minBottom {
			l.BottomH = minBottom
		}

		// If minimums pushed us over budget, shrink largest section first
		for l.SessionsH+l.MiddleH+l.BottomH > usable {
			if l.SessionsH > minSessions && l.SessionsH >= l.MiddleH && l.SessionsH >= l.BottomH {
				l.SessionsH--
			} else if l.MiddleH > minMiddle && l.MiddleH >= l.BottomH {
				l.MiddleH--
			} else if l.BottomH > minBottom {
				l.BottomH--
			} else {
				// All at minimums but still over — shrink sessions (it's most resilient)
				l.SessionsH--
			}
		}

		// If under budget, give remainder to sessions (preview benefits most)
		remainder := usable - l.SessionsH - l.MiddleH - l.BottomH
		l.SessionsH += remainder
	}

	// Width: 50/50 split for middle row
	l.LeftW = width / 2
	l.RightW = width - l.LeftW

	// Bottom row: 2/3 notes, 1/3 signals
	l.BottomLeftW = width * 2 / 3
	l.BottomRightW = width - l.BottomLeftW

	return l
}

// Model is the root Bubbletea model.
type Model struct {
	config     *config.Config
	configPath string
	width      int
	height     int
	focused    PanelID
	mode       Mode
	layout     Layout

	sessions       SessionsModel
	repos          ReposModel
	tasks          TasksModel
	inbox          InboxModel
	signals        SignalsModel
	github         *sources.GitHubStatus
	sessionPreview string
	lastPreviewSession string

	staleThreshold time.Duration
	transientErr   string
	transientTimer int

	// New session dialog state
	newSessionInput textinput.Model
	newSessionStep  int    // 0=path, 1=label confirm
	newSessionPath  string // expanded path from step 0
	newSessionErr   string // inline validation error

	// Session search (/ key)
	searchInput   textinput.Model
	searchResults []int // indices into sessions.Sessions
	searchCursor  int
}

// NewModel creates a new root model with the given config.
func NewModel(cfg *config.Config, configPath string) Model {
	staleThreshold, _ := time.ParseDuration(cfg.Signals.StaleSessionThreshold)

	ti := textinput.New()
	ti.Placeholder = "~/workspace/my-project"
	ti.CharLimit = 512
	ti.Width = 50

	si := textinput.New()
	si.Placeholder = "search sessions..."
	si.CharLimit = 128
	si.Width = 40

	m := Model{
		config:          cfg,
		configPath:      configPath,
		focused:         PanelSessions, // default focus
		sessions:        NewSessionsModel(),
		repos:           NewReposModel(),
		tasks:           NewTasksModel(),
		inbox:           InboxModel{Loading: true, FilePath: cfg.Obsidian.InboxFile},
		signals:         NewSignalsModel(),
		staleThreshold:  staleThreshold,
		newSessionInput: ti,
		searchInput:     si,
	}
	return m
}

// Message types for source data
type (
	tmuxDataMsg    struct{ Sessions []sources.TmuxSession }
	gitDataMsg     struct{ Repos []sources.GitRepoStatus }
	tasksDataMsg   struct{ Tasks []sources.Task }
	inboxDataMsg   struct{ Items []sources.Task }
	githubDataMsg    struct{ Status *sources.GitHubStatus }
	sourceErrMsg     struct{ Source string; Err error }
	previewDataMsg      struct{ Content string; Session string }
	sessionStatusMsg    struct{ Snapshots map[string]string } // session name → pane content
	localTickMsg        struct{}
	remoteTickMsg       struct{}
	clearErrMsg         struct{}
	configSaveResultMsg struct{ Err error }
)

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchTmux(),
		m.fetchGit(),
		m.fetchTasks(),
		m.fetchInbox(),
		m.fetchGitHub(),
		m.localTick(),
		m.remoteTick(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	modeBefore := m.mode

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout = CalculateLayout(m.width, m.height, len(m.repos.Repos))

	case tea.KeyMsg:
		cmd := m.handleKey(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case tmuxDataMsg:
		// Filter out the cockpit session itself
		var filtered []sources.TmuxSession
		for _, s := range msg.Sessions {
			if s.Name != m.config.General.SessionName {
				filtered = append(filtered, s)
			}
		}
		m.sessions.Sessions = filtered
		m.sessions.Loading = false
		m.updateSignals()
		// Fetch preview for currently selected session + status for all sessions
		cmds = append(cmds, m.fetchPreview(), m.fetchSessionStatuses())

	case sessionStatusMsg:
		for name, content := range msg.Snapshots {
			m.sessions.UpdateStatus(name, content)
		}

	case previewDataMsg:
		if msg.Session == m.selectedSessionName() {
			m.sessionPreview = msg.Content
		}

	case gitDataMsg:
		m.repos.Repos = msg.Repos
		m.repos.Loading = false
		m.layout = CalculateLayout(m.width, m.height, len(m.repos.Repos))
		m.updateSignals()

	case tasksDataMsg:
		// Filter out completed tasks — they get cleaned from the view automatically
		var active []sources.Task
		for _, t := range msg.Tasks {
			if !t.Done {
				active = append(active, t)
			}
		}
		m.tasks.Tasks = active
		m.tasks.Loading = false
		if m.tasks.Cursor >= len(m.tasks.Tasks) && m.tasks.Cursor > 0 {
			m.tasks.Cursor = len(m.tasks.Tasks) - 1
		}

	case inboxDataMsg:
		// Filter out completed items
		var active []sources.Task
		for _, t := range msg.Items {
			if !t.Done {
				active = append(active, t)
			}
		}
		m.inbox.Items = active
		m.inbox.Loading = false
		if m.inbox.Cursor >= len(m.inbox.Items) && m.inbox.Cursor > 0 {
			m.inbox.Cursor = len(m.inbox.Items) - 1
		}

	case githubDataMsg:
		m.github = msg.Status
		m.updateSignals()

	case sourceErrMsg:
		m.transientErr = "⚠ " + msg.Source + ": " + msg.Err.Error()
		m.transientTimer = 3
		cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))

	case clearErrMsg:
		m.transientTimer--
		if m.transientTimer <= 0 {
			m.transientErr = ""
		} else {
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		}

	case sessionSavedMsg:
		// Add to in-memory config and refresh repos panel
		m.config.Repos = append(m.config.Repos, msg.Repo)
		m.transientErr = "✓ saved " + msg.Repo.Label + " to config"
		m.transientTimer = 3
		cmds = append(cmds, m.fetchGit())
		cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))

	case configSaveResultMsg:
		if msg.Err != nil {
			m.transientErr = "⚠ config save: " + msg.Err.Error()
			m.transientTimer = 3
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		}

	case localTickMsg:
		cmds = append(cmds,
			m.fetchTmux(),
			m.fetchGit(),
			m.fetchTasks(),
			m.fetchInbox(),
			m.localTick(),
		)

	case remoteTickMsg:
		cmds = append(cmds,
			m.fetchGitHub(),
			m.remoteTick(),
		)

	case tmuxSwitchResultMsg:
		if msg.Err != nil {
			m.transientErr = "⚠ tmux: " + msg.Err.Error()
			m.transientTimer = 3
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		}
		// On success: do nothing. The tmux client switched away but cockpit
		// keeps running in the background. User returns via prefix+S or `cockpit`.
	}

	// Update text input if already in capture mode — skip the key that entered the mode
	if modeBefore == ModeCapture {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.tasks.TextInput, cmd = m.tasks.TextInput.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// Forward messages to new session input — skip the key that entered the mode
	if modeBefore == ModeNewSession {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.newSessionInput, cmd = m.newSessionInput.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// Forward messages to search input — skip the key that entered the mode
	if modeBefore == ModeSearch {
		if _, ok := msg.(tea.KeyMsg); ok {
			prevVal := m.searchInput.Value()
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			// Re-filter when query changes
			if m.searchInput.Value() != prevVal {
				m.updateSearchResults()
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch m.mode {
	case ModeCapture:
		return m.handleCaptureKey(msg)
	case ModeNewSession:
		return m.handleNewSessionKey(msg)
	case ModeSearch:
		return m.handleSearchKey(msg)
	default:
		return m.handleNavKey(msg)
	}
}

func (m *Model) handleNavKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "tab":
		m.focused = (m.focused + 1) % panelCount
	case "shift+tab":
		m.focused = (m.focused - 1 + panelCount) % panelCount
	case "j":
		m.cursorDown()
		if m.focused == PanelSessions {
			return m.fetchPreview()
		}
	case "k":
		m.cursorUp()
		if m.focused == PanelSessions {
			return m.fetchPreview()
		}
	case "q":
		return tea.Quit
	case "r":
		return tea.Batch(
			m.fetchTmux(),
			m.fetchGit(),
			m.fetchTasks(),
			m.fetchInbox(),
			m.fetchGitHub(),
		)
	case "s":
		if m.focused == PanelSessions && len(m.sessions.Sessions) > 0 {
			return m.saveSessionAsRepo()
		}
	case "n":
		m.mode = ModeNewSession
		m.newSessionStep = 0
		m.newSessionPath = ""
		m.newSessionErr = ""
		m.newSessionInput.SetValue("")
		m.newSessionInput.Placeholder = "~/workspace/my-project"
		m.newSessionInput.Focus()
		return nil
	case "c":
		m.mode = ModeCapture
		m.focused = PanelToday
		m.tasks.Capturing = true
		m.tasks.TextInput.Focus()
		return nil
	case "x":
		if m.focused == PanelToday && len(m.tasks.Tasks) > 0 {
			task := m.tasks.Tasks[m.tasks.Cursor]
			err := sources.ToggleTask(m.config.Obsidian.TodayFile, task.Line)
			if err != nil {
				return func() tea.Msg {
					return sourceErrMsg{Source: "toggle", Err: err}
				}
			}
			// Remove completed task from view immediately
			m.tasks.Tasks = append(m.tasks.Tasks[:m.tasks.Cursor], m.tasks.Tasks[m.tasks.Cursor+1:]...)
			if m.tasks.Cursor >= len(m.tasks.Tasks) && m.tasks.Cursor > 0 {
				m.tasks.Cursor--
			}
		} else if m.focused == PanelInbox && len(m.inbox.Items) > 0 {
			item := m.inbox.Items[m.inbox.Cursor]
			err := sources.ToggleTask(m.config.Obsidian.InboxFile, item.Line)
			if err != nil {
				return func() tea.Msg {
					return sourceErrMsg{Source: "toggle", Err: err}
				}
			}
			// Remove completed item from view immediately
			m.inbox.Items = append(m.inbox.Items[:m.inbox.Cursor], m.inbox.Items[m.inbox.Cursor+1:]...)
			if m.inbox.Cursor >= len(m.inbox.Items) && m.inbox.Cursor > 0 {
				m.inbox.Cursor--
			}
		}
	case "/":
		m.mode = ModeSearch
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.updateSearchResults()
		return nil
	case "enter":
		return m.handleEnter()
	}
	return nil
}

func (m *Model) handleCaptureKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = ModeNavigation
		m.tasks.Capturing = false
		m.tasks.TextInput.Blur()
		m.tasks.TextInput.Reset()
	case "enter":
		text := m.tasks.TextInput.Value()
		if text != "" {
			err := sources.AppendInbox(m.config.Obsidian.TodayFile, text)
			if err != nil {
				return func() tea.Msg {
					return sourceErrMsg{Source: "capture", Err: err}
				}
			}
			m.tasks.TextInput.Reset()
			// Re-fetch tasks to show the new item
			return m.fetchTasks()
		}
	}
	return nil
}

func (m *Model) handleNewSessionKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if m.newSessionStep == 1 {
			// Go back to path step
			m.newSessionStep = 0
			m.newSessionErr = ""
			m.newSessionInput.SetValue(m.newSessionPath)
			m.newSessionInput.Placeholder = "~/workspace/my-project"
			return nil
		}
		// Cancel dialog
		m.mode = ModeNavigation
		m.newSessionInput.Blur()
		m.newSessionErr = ""
		return nil

	case "enter":
		if m.newSessionStep == 0 {
			return m.newSessionValidatePath()
		}
		return m.newSessionLaunch(false)

	case "ctrl+s":
		if m.newSessionStep == 1 {
			return m.newSessionLaunch(true)
		}
	}
	return nil
}

func (m *Model) newSessionValidatePath() tea.Cmd {
	raw := m.newSessionInput.Value()
	if raw == "" {
		m.newSessionErr = "path is required"
		return nil
	}

	expanded := config.ExpandTilde(raw)
	info, err := os.Stat(expanded)
	if err != nil {
		// Path doesn't exist — create it
		if mkErr := os.MkdirAll(expanded, 0755); mkErr != nil {
			m.newSessionErr = "failed to create: " + mkErr.Error()
			return nil
		}
	} else if !info.IsDir() {
		m.newSessionErr = "not a directory"
		return nil
	}

	m.newSessionPath = expanded
	m.newSessionStep = 1
	m.newSessionErr = ""

	// Auto-derive label from directory name
	label := filepath.Base(expanded)
	m.newSessionInput.SetValue(label)
	m.newSessionInput.Placeholder = "session-label"
	return nil
}

func (m *Model) newSessionLaunch(save bool) tea.Cmd {
	label := m.newSessionInput.Value()
	if label == "" {
		m.newSessionErr = "label is required"
		return nil
	}
	if !validLabel.MatchString(label) {
		m.newSessionErr = "alphanumeric, hyphens, underscores only"
		return nil
	}
	if m.labelExists(label) {
		m.newSessionErr = "label already in use"
		return nil
	}

	path := m.newSessionPath
	repo := config.RepoConfig{Path: path, Label: label}

	// Add to in-memory config so it shows in Repos panel immediately
	m.config.Repos = append(m.config.Repos, repo)

	// Exit dialog
	m.mode = ModeNavigation
	m.newSessionInput.Blur()
	m.newSessionErr = ""

	var cmds []tea.Cmd

	if save {
		configPath := m.configPath
		cmds = append(cmds, func() tea.Msg {
			err := config.AppendRepo(configPath, repo)
			return configSaveResultMsg{Err: err}
		})
	}

	cmds = append(cmds, func() tea.Msg {
		err := tmuxJump(label, path)
		return tmuxSwitchResultMsg{Err: err}
	})

	// Refresh git status to pick up the new repo
	cmds = append(cmds, m.fetchGit())

	return tea.Batch(cmds...)
}

func (m *Model) labelExists(label string) bool {
	for _, r := range m.config.Repos {
		if r.Label == label {
			return true
		}
	}
	for _, s := range m.sessions.Sessions {
		if s.Name == label {
			return true
		}
	}
	return false
}

func (m *Model) saveSessionAsRepo() tea.Cmd {
	session := m.sessions.Sessions[m.sessions.Cursor]
	label := session.Name

	configPath := m.configPath
	return func() tea.Msg {
		// Check the actual config file for duplicates, not in-memory state
		diskCfg, err := config.Load(configPath)
		if err == nil {
			for _, r := range diskCfg.Repos {
				if r.Label == label {
					return sourceErrMsg{Source: "save", Err: fmt.Errorf("%s is already in config", label)}
				}
			}
		}

		// Get the session's working directory from tmux
		out, err := exec.Command("tmux", "display-message", "-t", label, "-p", "#{pane_current_path}").Output()
		if err != nil {
			return sourceErrMsg{Source: "save", Err: fmt.Errorf("could not get session path: %w", err)}
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			return sourceErrMsg{Source: "save", Err: fmt.Errorf("empty path for session %s", label)}
		}

		repo := config.RepoConfig{Path: path, Label: label}
		if err := config.AppendRepo(configPath, repo); err != nil {
			return configSaveResultMsg{Err: err}
		}
		return sessionSavedMsg{Repo: repo}
	}
}

// sessionSavedMsg is sent after successfully saving a session to config.
type sessionSavedMsg struct{ Repo config.RepoConfig }

// tmuxSwitchResultMsg is sent after a tmux switch attempt.
type tmuxSwitchResultMsg struct{ Err error }

func (m *Model) handleEnter() tea.Cmd {
	switch m.focused {
	case PanelSessions:
		if len(m.sessions.Sessions) > 0 {
			name := m.sessions.Sessions[m.sessions.Cursor].Name
			return func() tea.Msg {
				err := tmuxSwitch(name)
				return tmuxSwitchResultMsg{Err: err}
			}
		}
	case PanelRepos:
		if len(m.repos.Repos) > 0 {
			repo := m.repos.Repos[m.repos.Cursor]
			return func() tea.Msg {
				err := tmuxJump(repo.Label, repo.Path)
				return tmuxSwitchResultMsg{Err: err}
			}
		}
	}
	return nil
}

func (m *Model) cursorUp() {
	switch m.focused {
	case PanelSessions:
		m.sessions.CursorUp()
	case PanelRepos:
		m.repos.CursorUp()
	case PanelToday:
		m.tasks.CursorUp()
	case PanelInbox:
		m.inbox.CursorUp()
	}
}

func (m *Model) cursorDown() {
	switch m.focused {
	case PanelSessions:
		m.sessions.CursorDown()
	case PanelRepos:
		m.repos.CursorDown()
	case PanelToday:
		m.tasks.CursorDown()
	case PanelInbox:
		m.inbox.CursorDown()
	}
}

func (m *Model) updateSignals() {
	m.signals.UpdateSignals(m.sessions.Sessions, m.repos.Repos, m.github, m.staleThreshold)
}

func (m Model) View() string {
	if m.width < 60 {
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			WarningText.Render("Terminal too narrow (need 60+ cols).\nResize or press q to quit."))
	}

	showLastCommit := m.layout.LeftW >= 50

	// === Sessions panel (cards + preview, fixed heights) ===
	sessionsContent := m.sessions.View(m.width, 4, m.focused == PanelSessions)
	if m.width < 80 {
		sessionsContent = m.sessions.CompactView(m.width, m.focused == PanelSessions)
	}

	// Preview lines derive from layout: sessions inner height minus cards (~5 lines) minus header (1)
	previewMaxLines := m.layout.SessionsH - 8
	if previewMaxLines < 2 {
		previewMaxLines = 2
	}
	if m.sessionPreview != "" {
		previewHeader := MutedText.Render("─── " + m.selectedSessionName() + " ")
		// Inner width: panel width minus border (2) minus padding (2)
		innerW := m.width - 4
		if innerW < 20 {
			innerW = 20
		}
		lines := strings.Split(m.sessionPreview, "\n")
		// Truncate long lines to prevent wrapping that blows the height budget
		for i, line := range lines {
			if len(line) > innerW {
				lines[i] = line[:innerW-1] + "…"
			}
		}
		if len(lines) > previewMaxLines {
			lines = lines[len(lines)-previewMaxLines:]
		}
		for len(lines) < previewMaxLines {
			lines = append(lines, "")
		}
		sessionsContent += "\n" + previewHeader + "\n" + strings.Join(lines, "\n")
	} else {
		emptyLines := make([]string, previewMaxLines+1)
		for i := range emptyLines {
			emptyLines[i] = ""
		}
		sessionsContent += "\n" + strings.Join(emptyLines, "\n")
	}

	sessionsPanel := RenderPanel("Sessions", sessionsContent, m.width, m.layout.SessionsH, m.focused == PanelSessions)

	// === Middle row: Projects | Today (side by side) ===
	repos := m.repos
	reposPanel := RenderPanel("Projects",
		repos.View(m.layout.LeftW, m.layout.MiddleH, m.focused == PanelRepos, showLastCommit),
		m.layout.LeftW, m.layout.MiddleH, m.focused == PanelRepos)

	tasks := m.tasks
	tasksPanel := RenderPanel("Today",
		tasks.View(m.layout.RightW, m.layout.MiddleH, m.focused == PanelToday),
		m.layout.RightW, m.layout.MiddleH, m.focused == PanelToday)

	middleRow := lipgloss.JoinHorizontal(lipgloss.Top, reposPanel, tasksPanel)

	// === Bottom row: Notes (2/3) | Signals (1/3) ===
	inboxPanel := RenderPanel("Notes",
		m.inbox.View(m.layout.BottomLeftW, m.layout.BottomH, m.focused == PanelInbox),
		m.layout.BottomLeftW, m.layout.BottomH, m.focused == PanelInbox)
	signalsPanel := RenderPanel("Signals",
		m.signals.View(m.layout.BottomRightW, m.layout.BottomH, m.focused == PanelSignals),
		m.layout.BottomRightW, m.layout.BottomH, m.focused == PanelSignals)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top, inboxPanel, signalsPanel)

	// Key hints
	keyhints := KeyhintsView(m.mode, m.focused, m.width)
	if m.transientErr != "" {
		keyhints = WarningText.Render(m.transientErr)
	}

	page := lipgloss.JoinVertical(lipgloss.Left,
		sessionsPanel,
		middleRow,
		bottomRow,
		keyhints,
	)

	// Overlay new session dialog if active
	if m.mode == ModeNewSession {
		dialog := m.renderNewSessionDialog()
		page = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceForeground(ColorBg))
	}

	// Overlay search dialog
	if m.mode == ModeSearch {
		dialog := m.renderSearchDialog()
		page = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceForeground(ColorBg))
	}

	return page
}

func (m *Model) renderNewSessionDialog() string {
	dialogW := 60
	if m.width < 64 {
		dialogW = m.width - 4
	}

	var lines []string

	title := AccentText.Bold(true).Render("New Session")
	lines = append(lines, title)
	lines = append(lines, "")

	if m.newSessionStep == 0 {
		lines = append(lines, BoldText.Render("Path:"))
		lines = append(lines, "> "+m.newSessionInput.View())
	} else {
		lines = append(lines, MutedText.Render("Path: ")+m.newSessionPath)
		lines = append(lines, "")
		lines = append(lines, BoldText.Render("Label:"))
		lines = append(lines, "> "+m.newSessionInput.View())
	}

	if m.newSessionErr != "" {
		lines = append(lines, "")
		lines = append(lines, ErrorText.Render("  "+m.newSessionErr))
	}

	lines = append(lines, "")
	if m.newSessionStep == 0 {
		lines = append(lines, AccentText.Render("Enter")+" "+MutedText.Render("next")+"  "+AccentText.Render("Esc")+" "+MutedText.Render("cancel"))
	} else {
		lines = append(lines, AccentText.Render("Enter")+" "+MutedText.Render("jump (ephemeral)"))
		lines = append(lines, SuccessText.Render("Ctrl+S")+" "+MutedText.Render("save to config + jump"))
		lines = append(lines, AccentText.Render("Esc")+" "+MutedText.Render("back"))
	}

	content := strings.Join(lines, "\n")

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogW)

	return style.Render(content)
}

// Source fetch commands
func (m Model) fetchTmux() tea.Cmd {
	return func() tea.Msg {
		sessions, err := sources.GetTmuxSessions(context.Background())
		if err != nil {
			return sourceErrMsg{Source: "tmux", Err: err}
		}
		return tmuxDataMsg{Sessions: sessions}
	}
}

func (m Model) fetchGit() tea.Cmd {
	repos := m.config.Repos
	return func() tea.Msg {
		results := sources.GetGitStatus(context.Background(), repos)
		return gitDataMsg{Repos: results}
	}
}

func (m Model) fetchTasks() tea.Cmd {
	path := m.config.Obsidian.TodayFile
	return func() tea.Msg {
		tasks, err := sources.ReadTasks(path)
		if err != nil {
			return sourceErrMsg{Source: "tasks", Err: err}
		}
		return tasksDataMsg{Tasks: tasks}
	}
}

func (m Model) fetchInbox() tea.Cmd {
	path := m.config.Obsidian.InboxFile
	return func() tea.Msg {
		items, err := sources.ReadTasks(path)
		if err != nil {
			return sourceErrMsg{Source: "inbox", Err: err}
		}
		return inboxDataMsg{Items: items}
	}
}

func (m Model) fetchGitHub() tea.Cmd {
	if !m.config.GitHub.Enabled {
		return nil
	}
	repos := m.config.Repos
	return func() tea.Msg {
		status := sources.GetGitHubStatus(context.Background(), repos)
		return githubDataMsg{Status: status}
	}
}

func (m Model) localTick() tea.Cmd {
	d := time.Duration(m.config.General.RefreshInterval) * time.Second
	return tea.Tick(d, func(time.Time) tea.Msg { return localTickMsg{} })
}

func (m Model) remoteTick() tea.Cmd {
	d := time.Duration(m.config.GitHub.RefreshInterval) * time.Second
	return tea.Tick(d, func(time.Time) tea.Msg { return remoteTickMsg{} })
}

func (m Model) selectedSessionName() string {
	if len(m.sessions.Sessions) == 0 {
		return ""
	}
	return m.sessions.Sessions[m.sessions.Cursor].Name
}

func (m Model) fetchPreview() tea.Cmd {
	name := m.selectedSessionName()
	if name == "" {
		return nil
	}
	maxLines := m.layout.SessionsH - 6 // cards take ~4 rows, leave rest for preview
	if maxLines < 3 {
		maxLines = 3
	}
	return func() tea.Msg {
		content, err := sources.CapturePane(context.Background(), name, maxLines)
		if err != nil {
			return previewDataMsg{Content: MutedText.Render("(no preview available)"), Session: name}
		}
		return previewDataMsg{Content: content, Session: name}
	}
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = ModeNavigation
		m.searchInput.Blur()
		return nil
	case "enter":
		if len(m.searchResults) > 0 {
			idx := m.searchResults[m.searchCursor]
			name := m.sessions.Sessions[idx].Name
			m.mode = ModeNavigation
			m.searchInput.Blur()
			return func() tea.Msg {
				err := tmuxSwitch(name)
				return tmuxSwitchResultMsg{Err: err}
			}
		}
		return nil
	case "up", "ctrl+k":
		if m.searchCursor > 0 {
			m.searchCursor--
		}
		return nil
	case "down", "ctrl+j":
		if m.searchCursor < len(m.searchResults)-1 {
			m.searchCursor++
		}
		return nil
	}
	return nil
}

func (m *Model) updateSearchResults() {
	query := strings.ToLower(m.searchInput.Value())
	m.searchResults = nil
	m.searchCursor = 0

	for i, s := range m.sessions.Sessions {
		if query == "" || strings.Contains(strings.ToLower(s.Name), query) {
			m.searchResults = append(m.searchResults, i)
		}
	}
}

func (m *Model) renderSearchDialog() string {
	dialogW := 50
	if m.width < 54 {
		dialogW = m.width - 4
	}

	var lines []string

	lines = append(lines, AccentText.Bold(true).Render("Jump to Session"))
	lines = append(lines, "")
	lines = append(lines, "  "+m.searchInput.View())
	lines = append(lines, "")

	maxVisible := 10
	for vi, ri := range m.searchResults {
		if vi >= maxVisible {
			lines = append(lines, MutedText.Render(fmt.Sprintf("  … %d more", len(m.searchResults)-maxVisible)))
			break
		}

		s := m.sessions.Sessions[ri]

		// Status indicator
		statusDot := MutedText.Render("○")
		if st, ok := m.sessions.Statuses[s.Name]; ok {
			switch st {
			case sources.ClaudeStatusIdle:
				statusDot = ErrorText.Render("●")
			case sources.ClaudeStatusWorking:
				statusDot = SuccessText.Render("●")
			}
		}

		name := s.Name
		if vi == m.searchCursor {
			name = AccentText.Bold(true).Render(s.Name)
			lines = append(lines, fmt.Sprintf("  ▸ %s %s", statusDot, name))
		} else {
			lines = append(lines, fmt.Sprintf("    %s %s", statusDot, name))
		}
	}

	if len(m.searchResults) == 0 && m.searchInput.Value() != "" {
		lines = append(lines, MutedText.Render("  no matches"))
	}

	lines = append(lines, "")
	lines = append(lines, MutedText.Render("  ↑↓ navigate  Enter jump  Esc cancel"))

	content := strings.Join(lines, "\n")

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogW)

	return style.Render(content)
}

func (m Model) fetchSessionStatuses() tea.Cmd {
	sessions := m.sessions.Sessions
	return func() tea.Msg {
		ctx := context.Background()
		snapshots := make(map[string]string, len(sessions))
		for _, s := range sessions {
			content, err := sources.CapturePaneContent(ctx, s.Name)
			if err != nil {
				continue
			}
			snapshots[s.Name] = content
		}
		return sessionStatusMsg{Snapshots: snapshots}
	}
}

// tmuxSwitch switches to an existing tmux session.
func tmuxSwitch(name string) error {
	return exec.Command("tmux", "switch-client", "-t", name).Run()
}

// tmuxJump switches to or creates a tmux session for a repo.
func tmuxJump(label, path string) error {
	if !validLabel.MatchString(label) {
		return fmt.Errorf("invalid session label %q: must be alphanumeric, hyphens, or underscores", label)
	}
	// Try switching first
	if err := exec.Command("tmux", "switch-client", "-t", label).Run(); err == nil {
		return nil
	}
	// Create session then switch
	if err := exec.Command("tmux", "new-session", "-d", "-s", label, "-c", path).Run(); err != nil {
		return err
	}
	return exec.Command("tmux", "switch-client", "-t", label).Run()
}
