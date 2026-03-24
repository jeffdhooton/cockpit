package tui

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

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
)

// Layout holds calculated panel dimensions.
type Layout struct {
	SessionsH int // cards + preview
	MiddleH   int // repos | today row height
	BottomH   int // inbox | signals row height
	KeyhintsH int
	LeftW     int  // repos / inbox width
	RightW    int  // today / signals width
}

// CalculateLayout computes panel sizes based on terminal dimensions.
func CalculateLayout(width, height, repoCount int) Layout {
	l := Layout{KeyhintsH: 1}

	// Layout ratios scale with terminal height
	usable := height - l.KeyhintsH

	sessionsPct := 45
	if height < 50 {
		sessionsPct = 25
	} else if height < 60 {
		sessionsPct = 30
	} else if height < 70 {
		sessionsPct = 38
	}

	l.SessionsH = usable * sessionsPct / 100
	l.MiddleH = usable * 30 / 100
	l.BottomH = usable - l.SessionsH - l.MiddleH

	// Floors — every section needs a minimum to be usable
	if l.SessionsH < 10 {
		l.SessionsH = 10
	}
	if l.MiddleH < 6 {
		l.MiddleH = 6
	}
	if l.BottomH < 5 {
		l.BottomH = 5
	}

	// Width: 50/50 split for side-by-side panels
	l.LeftW = width / 2
	l.RightW = width - l.LeftW

	return l
}

// Model is the root Bubbletea model.
type Model struct {
	config  *config.Config
	width   int
	height  int
	focused PanelID
	mode    Mode
	layout  Layout

	sessions       SessionsModel
	repos          ReposModel
	tasks          TasksModel
	inbox          InboxModel
	signals        SignalsModel
	github         *sources.GitHubStatus
	calendar       []sources.CalendarEvent
	sessionPreview string
	lastPreviewSession string

	staleThreshold time.Duration
	transientErr   string
	transientTimer int
}

// NewModel creates a new root model with the given config.
func NewModel(cfg *config.Config) Model {
	staleThreshold, _ := time.ParseDuration(cfg.Signals.StaleSessionThreshold)

	m := Model{
		config:         cfg,
		focused:        PanelSessions, // default focus
		sessions:       NewSessionsModel(),
		repos:          NewReposModel(),
		tasks:          NewTasksModel(),
		inbox:          InboxModel{Loading: true, FilePath: cfg.Obsidian.InboxFile},
		signals:        NewSignalsModel(),
		staleThreshold: staleThreshold,
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
	calendarDataMsg  struct{ Events []sources.CalendarEvent }
	sourceErrMsg     struct{ Source string; Err error }
	previewDataMsg    struct{ Content string; Session string }
	localTickMsg      struct{}
	remoteTickMsg     struct{}
	clearErrMsg       struct{}
)

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchTmux(),
		m.fetchGit(),
		m.fetchTasks(),
		m.fetchInbox(),
		m.fetchGitHub(),
		m.fetchCalendar(),
		m.localTick(),
		m.remoteTick(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

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
		// Fetch preview for currently selected session
		cmds = append(cmds, m.fetchPreview())

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
		m.tasks.Tasks = msg.Tasks
		m.tasks.Loading = false
		// Set cursor to first unchecked if this is the initial load
		if m.tasks.Cursor == 0 {
			m.tasks.Cursor = m.tasks.FirstUnchecked()
		}

	case inboxDataMsg:
		m.inbox.Items = msg.Items
		m.inbox.Loading = false

	case githubDataMsg:
		m.github = msg.Status
		m.updateSignals()

	case calendarDataMsg:
		m.calendar = msg.Events
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

	case localTickMsg:
		cmds = append(cmds,
			m.fetchTmux(),
			m.fetchGit(),
			m.fetchTasks(),
			m.fetchInbox(),
			m.fetchCalendar(),
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

	// Update text input if in capture mode — only forward key and blink messages
	if m.mode == ModeCapture {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.tasks.TextInput, cmd = m.tasks.TextInput.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.mode == ModeCapture {
		return m.handleCaptureKey(msg)
	}
	return m.handleNavKey(msg)
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
			m.tasks.Tasks[m.tasks.Cursor].Done = !m.tasks.Tasks[m.tasks.Cursor].Done
		} else if m.focused == PanelInbox && len(m.inbox.Items) > 0 {
			item := m.inbox.Items[m.inbox.Cursor]
			err := sources.ToggleTask(m.config.Obsidian.InboxFile, item.Line)
			if err != nil {
				return func() tea.Msg {
					return sourceErrMsg{Source: "toggle", Err: err}
				}
			}
			m.inbox.Items[m.inbox.Cursor].Done = !m.inbox.Items[m.inbox.Cursor].Done
		}
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
	m.signals.UpdateSignals(m.sessions.Sessions, m.repos.Repos, m.github, m.calendar, m.staleThreshold)
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

	// Preview gets a fixed max height — clamp the content
	// Preview lines scale with terminal height
	// Preview lines scale with terminal
	previewMaxLines := 3
	switch {
	case m.height >= 70:
		previewMaxLines = 25
	case m.height >= 55:
		previewMaxLines = 12
	case m.height >= 45:
		previewMaxLines = 6
	}
	if m.sessionPreview != "" {
		previewHeader := MutedText.Render("─── " + m.selectedSessionName() + " ")
		lines := strings.Split(m.sessionPreview, "\n")
		if len(lines) > previewMaxLines {
			lines = lines[len(lines)-previewMaxLines:]
		}
		// Pad to exactly previewMaxLines so the panel height is stable
		for len(lines) < previewMaxLines {
			lines = append(lines, "")
		}
		sessionsContent += "\n" + previewHeader + "\n" + strings.Join(lines, "\n")
	} else {
		// Reserve the same space even with no preview so layout doesn't jump
		emptyLines := make([]string, previewMaxLines+1) // +1 for header line
		for i := range emptyLines {
			emptyLines[i] = ""
		}
		sessionsContent += "\n" + strings.Join(emptyLines, "\n")
	}

	sessionsPanel := RenderPanel("Sessions", sessionsContent, m.width, m.layout.SessionsH, m.focused == PanelSessions)

	// === Middle row: Repos | Today (side by side) ===
	repos := m.repos
	reposPanel := RenderPanel("Repos",
		repos.View(m.layout.LeftW, m.layout.MiddleH, m.focused == PanelRepos, showLastCommit),
		m.layout.LeftW, m.layout.MiddleH, m.focused == PanelRepos)

	tasks := m.tasks
	tasksPanel := RenderPanel("Today",
		tasks.View(m.layout.RightW, m.layout.MiddleH, m.focused == PanelToday),
		m.layout.RightW, m.layout.MiddleH, m.focused == PanelToday)

	middleRow := lipgloss.JoinHorizontal(lipgloss.Top, reposPanel, tasksPanel)

	// === Bottom row: Inbox | Signals (side by side) ===
	inboxPanel := RenderPanel("Notes",
		m.inbox.View(m.layout.LeftW, m.layout.BottomH, m.focused == PanelInbox),
		m.layout.LeftW, m.layout.BottomH, m.focused == PanelInbox)
	signalsPanel := RenderPanel("Signals",
		m.signals.View(m.layout.RightW, m.layout.BottomH, m.focused == PanelSignals),
		m.layout.RightW, m.layout.BottomH, m.focused == PanelSignals)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top, inboxPanel, signalsPanel)

	// Key hints
	keyhints := KeyhintsView(m.mode, m.width)
	if m.transientErr != "" {
		keyhints = WarningText.Render(m.transientErr)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		sessionsPanel,
		middleRow,
		bottomRow,
		keyhints,
	)
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

func (m Model) fetchCalendar() tea.Cmd {
	return func() tea.Msg {
		events, err := sources.GetUpcomingEvents(context.Background(), 60)
		if err != nil {
			// Calendar not available — silently ignore
			return calendarDataMsg{}
		}
		return calendarDataMsg{Events: events}
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
