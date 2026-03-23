package tui

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
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
	SessionsH int
	ReposH    int
	TodayH    int
	BottomH   int
	KeyhintsH int
	InboxW    int
	SignalsW  int
	StackBottom bool // true when bottom row stacks vertically
}

// CalculateLayout computes panel sizes based on terminal dimensions.
func CalculateLayout(width, height, repoCount int) Layout {
	l := Layout{KeyhintsH: 1}

	// Height tiers
	switch {
	case height >= 40:
		l.SessionsH = 3
		l.ReposH = min(repoCount+2, 6)
		l.BottomH = 6
	case height >= 30:
		l.SessionsH = 2
		l.ReposH = min(repoCount+2, 4)
		l.BottomH = 4
	case height >= 24:
		l.SessionsH = 2
		l.ReposH = min(repoCount+2, 3)
		l.BottomH = 3
	default:
		l.SessionsH = 1
		l.ReposH = 1
		l.BottomH = 3
	}

	// Today gets remaining space
	l.TodayH = height - l.SessionsH - l.ReposH - l.BottomH - l.KeyhintsH
	if l.TodayH < 2 {
		l.TodayH = 2
	}

	// Width tiers for bottom row
	if width < 80 {
		l.StackBottom = true
		l.InboxW = width
		l.SignalsW = width
	} else {
		l.InboxW = width / 2
		l.SignalsW = width - l.InboxW
	}

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

	sessions SessionsModel
	repos    ReposModel
	tasks    TasksModel
	inbox    InboxModel
	signals  SignalsModel
	github   *sources.GitHubStatus

	staleThreshold time.Duration
	transientErr   string
	transientTimer int
}

// NewModel creates a new root model with the given config.
func NewModel(cfg *config.Config) Model {
	staleThreshold, _ := time.ParseDuration(cfg.Signals.StaleSessionThreshold)

	m := Model{
		config:         cfg,
		focused:        PanelToday, // default focus
		sessions:       NewSessionsModel(),
		repos:          NewReposModel(),
		tasks:          NewTasksModel(),
		inbox:          NewInboxModel(),
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
	githubDataMsg  struct{ Status *sources.GitHubStatus }
	sourceErrMsg   struct{ Source string; Err error }
	localTickMsg   struct{}
	remoteTickMsg  struct{}
	clearErrMsg    struct{}
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
		m.sessions.Sessions = msg.Sessions
		m.sessions.Loading = false
		m.updateSignals()

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
		} else {
			return m, tea.Quit
		}
	}

	// Update text input if in capture mode — only forward key and blink messages
	if m.mode == ModeCapture {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.inbox.TextInput, cmd = m.inbox.TextInput.Update(msg)
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
	case "k":
		m.cursorUp()
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
		m.focused = PanelInbox
		m.inbox.Capturing = true
		m.inbox.TextInput.Focus()
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
			// Update local state immediately
			m.tasks.Tasks[m.tasks.Cursor].Done = !m.tasks.Tasks[m.tasks.Cursor].Done
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
		m.inbox.Capturing = false
		m.inbox.TextInput.Blur()
		m.inbox.TextInput.Reset()
	case "enter":
		text := m.inbox.TextInput.Value()
		if text != "" {
			err := sources.AppendInbox(m.config.Obsidian.InboxFile, text)
			if err != nil {
				return func() tea.Msg {
					return sourceErrMsg{Source: "capture", Err: err}
				}
			}
			m.inbox.TextInput.Reset()
			// Re-fetch inbox to show the new item
			return m.fetchInbox()
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
	m.signals.UpdateSignals(m.sessions.Sessions, m.repos.Repos, m.github, m.staleThreshold)
}

func (m Model) View() string {
	if m.width < 60 {
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			WarningText.Render("Terminal too narrow (need 60+ cols).\nResize or press q to quit."))
	}

	showLastCommit := m.width >= 80

	// Render panels
	sessionsContent := m.sessions.View(m.width, m.layout.SessionsH, m.focused == PanelSessions)
	if m.width < 80 {
		sessionsContent = m.sessions.CompactView(m.width, m.focused == PanelSessions)
	}
	sessionsPanel := RenderPanel("Sessions", sessionsContent, m.width, m.layout.SessionsH, m.focused == PanelSessions)

	// Note: repos and tasks View methods use pointer receivers to adjust scroll.
	// This is safe because View() is called on a local copy of the model in Bubbletea.
	repos := m.repos
	reposPanel := RenderPanel("Repos",
		repos.View(m.width, m.layout.ReposH, m.focused == PanelRepos, showLastCommit),
		m.width, m.layout.ReposH, m.focused == PanelRepos)

	tasks := m.tasks
	tasksPanel := RenderPanel("Today",
		tasks.View(m.width, m.layout.TodayH, m.focused == PanelToday),
		m.width, m.layout.TodayH, m.focused == PanelToday)

	// Bottom row
	var bottomRow string
	if m.layout.StackBottom {
		inboxPanel := RenderPanel("Inbox",
			m.inbox.View(m.layout.InboxW, m.layout.BottomH/2, m.focused == PanelInbox),
			m.layout.InboxW, m.layout.BottomH/2, m.focused == PanelInbox)
		signalsPanel := RenderPanel("Signals",
			m.signals.View(m.layout.SignalsW, m.layout.BottomH/2, m.focused == PanelSignals),
			m.layout.SignalsW, m.layout.BottomH/2, m.focused == PanelSignals)
		bottomRow = lipgloss.JoinVertical(lipgloss.Left, inboxPanel, signalsPanel)
	} else {
		inboxPanel := RenderPanel("Inbox",
			m.inbox.View(m.layout.InboxW, m.layout.BottomH, m.focused == PanelInbox),
			m.layout.InboxW, m.layout.BottomH, m.focused == PanelInbox)
		signalsPanel := RenderPanel("Signals",
			m.signals.View(m.layout.SignalsW, m.layout.BottomH, m.focused == PanelSignals),
			m.layout.SignalsW, m.layout.BottomH, m.focused == PanelSignals)
		bottomRow = lipgloss.JoinHorizontal(lipgloss.Top, inboxPanel, signalsPanel)
	}

	// Key hints
	keyhints := KeyhintsView(m.mode, m.width)
	if m.transientErr != "" {
		keyhints = WarningText.Render(m.transientErr)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		sessionsPanel,
		reposPanel,
		tasksPanel,
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

func (m Model) localTick() tea.Cmd {
	d := time.Duration(m.config.General.RefreshInterval) * time.Second
	return tea.Tick(d, func(time.Time) tea.Msg { return localTickMsg{} })
}

func (m Model) remoteTick() tea.Cmd {
	d := time.Duration(m.config.GitHub.RefreshInterval) * time.Second
	return tea.Tick(d, func(time.Time) tea.Msg { return remoteTickMsg{} })
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
