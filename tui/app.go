package tui

import (
	"context"
	"errors"
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
	"github.com/jhoot/cockpit/buildctl"
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
	PanelViz
	panelCount // sentinel
)

// Mode represents the TUI interaction mode.
type Mode int

const (
	ModeNavigation Mode = iota
	ModeCapture
	ModeNewSession
	ModeSearch
	ModeVizPicker
	ModeBuildLaunch
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

// BuildClient is the injectable boundary to Build's frozen buildctl
// contract. The production implementation is *buildctl.Client; tests
// substitute fakes. Cockpit never speaks to Build through any other route.
type BuildClient interface {
	ListSessions(ctx context.Context) ([]buildctl.Session, error)
	ListProjects(ctx context.Context) ([]buildctl.Project, error)
	Launch(ctx context.Context, opts buildctl.LaunchOptions) (buildctl.Session, error)
	Resume(ctx context.Context, conversationID, permission string) (buildctl.Session, error)
	AttachCommand(ctx context.Context, runID string) (*exec.Cmd, error)
}

// buildctlResolve is a seam for tests: production resolves the real
// executable, tests substitute a stub so nothing touches the real Build home.
var buildctlResolve = buildctl.ResolveCommand

// Model is the root Bubbletea model.
type Model struct {
	config     *config.Config
	configPath string
	width      int
	height     int
	focused    PanelID
	mode       Mode
	layout     Layout

	sessions           SessionsModel
	repos              ReposModel
	tasks              TasksModel
	inbox              InboxModel
	viz                VizModel
	github             *sources.GitHubStatus
	sessionPreview     string
	lastPreviewSession string

	// Build integration: raw source lists remerged into sessions.Sessions.
	buildClient    BuildClient
	legacySessions []sources.TmuxSession
	buildSessions  []buildctl.Session

	transientErr   string
	transientTimer int

	// New session dialog state
	newSessionInput textinput.Model
	newSessionStep  int    // 0=path, 1=label confirm
	newSessionPath  string // expanded path from step 0
	newSessionErr   string // inline validation error

	// Build launch dialog state (L key)
	launchStep       int // 0=project, 1=agent, 2=permission, 3=prompt
	launchProjects   []buildctl.Project
	launchCursor     int
	launchAgent      int // 0=claude, 1=codex
	launchPermission int // 0=standard, 1=dangerous
	launchLoading    bool
	launchErr        string
	launchInput      textinput.Model

	// Session search (/ key): results hold MergedSession.Key() identities,
	// never list positions, so refreshes cannot retarget a pending jump.
	searchInput   textinput.Model
	searchResults []string
	searchCursor  int

	// Visualizer picker (V key)
	vizPickerCursor int
}

// NewModel creates a new root model with the given config.
func NewModel(cfg *config.Config, configPath string) Model {
	ti := textinput.New()
	ti.Placeholder = "~/workspace/my-project"
	ti.CharLimit = 512
	ti.Width = 50

	si := textinput.New()
	si.Placeholder = "search sessions..."
	si.CharLimit = 128
	si.Width = 40

	pi := textinput.New()
	pi.Placeholder = "optional opening prompt..."
	pi.CharLimit = 512
	pi.Width = 50

	m := Model{
		config:          cfg,
		configPath:      configPath,
		focused:         PanelSessions, // default focus
		sessions:        NewSessionsModel(),
		repos:           NewReposModel(),
		tasks:           NewTasksModel(),
		inbox:           InboxModel{Loading: true, FilePath: cfg.Obsidian.InboxFile},
		viz:             NewVizModel(),
		newSessionInput: ti,
		searchInput:     si,
		launchInput:     pi,
	}
	m.buildClient, m.sessions.BuildNote = resolveBuildClient(cfg)
	return m
}

// resolveBuildClient locates the buildctl executable. Failure is nonfatal:
// the model runs legacy-only with a quiet, actionable indicator.
func resolveBuildClient(cfg *config.Config) (BuildClient, string) {
	path, err := buildctlResolve(cfg.Build.Command)
	if err != nil {
		return nil, "Build unavailable — legacy only (no buildctl; set [build].command)"
	}
	return &buildctl.Client{Command: path}, ""
}

// buildFailureNote renders a quiet, actionable indicator for a failed Build
// fetch. Every failure class degrades to legacy-only and is nonfatal.
func buildFailureNote(err error) string {
	switch {
	case errors.Is(err, buildctl.ErrUnavailable):
		return "Build unavailable — legacy only (is Build running?)"
	case errors.Is(err, buildctl.ErrTimeout):
		return "Build timed out — legacy only"
	case errors.Is(err, buildctl.ErrUnsupportedSchema), errors.Is(err, buildctl.ErrMalformed):
		return "Build data rejected (incompatible buildctl) — legacy only"
	default:
		return "Build error — legacy only"
	}
}

// Message types for source data
type (
	tmuxDataMsg   struct{ Sessions []sources.TmuxSession }
	gitDataMsg    struct{ Repos []sources.GitRepoStatus }
	tasksDataMsg  struct{ Tasks []sources.Task }
	inboxDataMsg  struct{ Items []sources.Task }
	githubDataMsg struct{ Status *sources.GitHubStatus }
	sourceErrMsg  struct {
		Source string
		Err    error
	}
	previewDataMsg struct {
		Content string
		Session string
	}
	sessionStatusMsg struct{ Snapshots map[string]string } // MergedSession.Key() → pane content
	buildDataMsg     struct {
		Sessions []buildctl.Session
		Err      error
	}
	buildProjectsMsg struct {
		Projects []buildctl.Project
		Err      error
	}
	buildActionResultMsg struct {
		Verb string
		Err  error
	}
	attachResultMsg     struct{ Err error }
	localTickMsg        struct{}
	remoteTickMsg       struct{}
	vizTickMsg          struct{}
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
		m.fetchBuild(),
		m.localTick(),
		m.remoteTick(),
		m.vizTick(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	modeBefore := m.mode
	launchStepBefore := m.launchStep

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
		m.legacySessions = filtered
		m.remerge()
		m.sessions.Loading = false
		// Fetch preview for currently selected session + status for all sessions
		cmds = append(cmds, m.refreshPreview(), m.fetchSessionStatuses())

	case buildDataMsg:
		if msg.Err != nil {
			// Every Build failure class degrades nonfatally to legacy-only.
			// Stale Build records are dropped so stale actions are impossible.
			m.buildSessions = nil
			m.sessions.BuildNote = buildFailureNote(msg.Err)
		} else {
			m.buildSessions = msg.Sessions
			m.sessions.BuildNote = ""
		}
		m.remerge()
		m.sessions.Loading = false
		cmds = append(cmds, m.refreshPreview())

	case buildProjectsMsg:
		m.launchLoading = false
		if msg.Err != nil {
			m.launchErr = "cannot list projects: " + msg.Err.Error()
			m.launchProjects = nil
		} else {
			m.launchErr = ""
			m.launchProjects = msg.Projects
			if m.launchCursor >= len(m.launchProjects) {
				m.launchCursor = 0
			}
		}

	case buildActionResultMsg:
		if msg.Err != nil {
			m.transientErr = "⚠ " + msg.Verb + ": " + msg.Err.Error()
			m.transientTimer = 3
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		} else {
			m.transientErr = "✓ " + msg.Verb + " started"
			m.transientTimer = 3
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		}
		// Refresh after any action so availability flags stay contract-true.
		cmds = append(cmds, m.fetchBuild())

	case attachResultMsg:
		// Bubble Tea has already restored the terminal around the child.
		// A failed or detached attach must leave the TUI fully intact.
		if msg.Err != nil {
			m.transientErr = "⚠ attach: " + msg.Err.Error()
			m.transientTimer = 3
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		}
		cmds = append(cmds, m.fetchTmux(), m.fetchBuild())

	case sessionStatusMsg:
		for key, content := range msg.Snapshots {
			m.sessions.UpdateStatus(key, content)
		}

	case previewDataMsg:
		if msg.Session == m.selectedSessionKey() {
			m.sessionPreview = msg.Content
		}

	case gitDataMsg:
		m.repos.Repos = msg.Repos
		m.repos.Loading = false
		m.layout = CalculateLayout(m.width, m.height, len(m.repos.Repos))
		m.viz.SetRepos(msg.Repos)

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
			m.fetchBuild(),
			m.localTick(),
		)

	case remoteTickMsg:
		cmds = append(cmds,
			m.fetchGitHub(),
			m.remoteTick(),
		)

	case vizTickMsg:
		m.viz.Tick()
		cmds = append(cmds, m.vizTick())

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

	// Forward messages to the launch prompt input, but only for keys that
	// arrive while the dialog is already on the prompt step — the Enter that
	// advances step 2→3 is consumed by the step machine, not the input.
	if modeBefore == ModeBuildLaunch && launchStepBefore == 3 {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.launchInput, cmd = m.launchInput.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
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
	case ModeVizPicker:
		return m.handleVizPickerKey(msg)
	case ModeBuildLaunch:
		return m.handleBuildLaunchKey(msg)
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
			return m.refreshPreview()
		}
	case "k":
		m.cursorUp()
		if m.focused == PanelSessions {
			return m.refreshPreview()
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
	case "v":
		m.viz.Next()
		return nil
	case "V":
		m.mode = ModeVizPicker
		m.vizPickerCursor = m.viz.Current
		return nil
	case "p":
		if c := m.viz.ActiveClock(); c != nil {
			c.TogglePomo()
			return nil
		}
	case "R":
		if c := m.viz.ActiveClock(); c != nil {
			c.Reset()
			return nil
		}
	case ".":
		if c := m.viz.ActiveClock(); c != nil {
			c.SkipPhase()
			return nil
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
	case "L":
		if m.buildClient == nil {
			m.transientErr = "⚠ Build unavailable — cannot launch (no buildctl)"
			m.transientTimer = 3
			return tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} })
		}
		m.mode = ModeBuildLaunch
		m.launchStep = 0
		m.launchCursor = 0
		m.launchAgent = 0
		m.launchPermission = 0
		m.launchErr = ""
		m.launchLoading = true
		m.launchProjects = nil
		m.launchInput.SetValue("")
		return m.fetchBuildProjects()
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
	// Only legacy tmux names collide with a new legacy session label. A Build
	// display title matching the label is fine — identities are source-scoped.
	for _, s := range m.sessions.Sessions {
		if s.Source == SourceLegacy && s.Legacy != nil && s.Legacy.Name == label {
			return true
		}
	}
	return false
}

func (m *Model) saveSessionAsRepo() tea.Cmd {
	session := m.sessions.Sessions[m.sessions.Cursor]
	if session.Source != SourceLegacy || session.Legacy == nil {
		// Build sessions are owned by Build; there is no tmux cwd to capture.
		m.transientErr = "⚠ Build sessions are managed by Build — nothing to save"
		m.transientTimer = 3
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} })
	}
	label := session.Legacy.Name

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
			return m.activateSession(m.sessions.Cursor)
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

// activateSession performs the primary action on a merged session row.
// Legacy sessions switch on the default tmux server exactly as before.
// Build sessions act only on contract flags: attach when attachable, resume
// when resumable, otherwise a visible hint. Nothing is inferred from tmux
// or process state.
func (m *Model) activateSession(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.sessions.Sessions) {
		return nil
	}
	sel := m.sessions.Sessions[idx]

	if sel.Source == SourceLegacy {
		if sel.Legacy == nil {
			return nil
		}
		name := sel.Legacy.Name
		return func() tea.Msg {
			err := tmuxSwitch(name)
			return tmuxSwitchResultMsg{Err: err}
		}
	}

	if sel.Build == nil || m.buildClient == nil {
		return nil
	}

	switch {
	case sel.Attachable():
		return m.attachBuild(*sel.Build.RunID)
	case sel.Resumable():
		return m.resumeBuild(sel.Build.ConversationID)
	default:
		m.transientErr = fmt.Sprintf("⚠ %q is not attachable or resumable (status: %s)",
			sel.DisplayName(), buildStatusLabel(sel.Build.Status))
		m.transientTimer = 3
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} })
	}
}

// attachBuild runs the interactive `buildctl session attach` child with the
// Bubble Tea terminal suspended around it. ExecProcess releases the
// terminal before spawning and restores it after the child exits — on
// success, on detach, and on child failure alike.
func (m *Model) attachBuild(runID string) tea.Cmd {
	cmd, err := m.buildClient.AttachCommand(context.Background(), runID)
	if err != nil {
		return func() tea.Msg { return attachResultMsg{Err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return attachResultMsg{Err: err}
	})
}

// resumeBuild runs `buildctl session resume` for a conversation the contract
// says is resumable, then refreshes.
func (m *Model) resumeBuild(conversationID string) tea.Cmd {
	client := m.buildClient
	return func() tea.Msg {
		_, err := client.Resume(context.Background(), conversationID, "standard")
		return buildActionResultMsg{Verb: "resume", Err: err}
	}
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
		previewHeader := m.renderPreviewHeader(m.width - 4)
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

	// === Bottom row: Notes (2/3) | Visualizer (1/3) ===
	inboxPanel := RenderPanel("Notes",
		m.inbox.View(m.layout.BottomLeftW, m.layout.BottomH, m.focused == PanelInbox),
		m.layout.BottomLeftW, m.layout.BottomH, m.focused == PanelInbox)
	vizPanel := RenderPanel(m.viz.Name(),
		m.viz.View(m.layout.BottomRightW, m.layout.BottomH, m.focused == PanelViz),
		m.layout.BottomRightW, m.layout.BottomH, m.focused == PanelViz)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top, inboxPanel, vizPanel)

	// Key hints
	keyhints := KeyhintsView(m.mode, m.focused, m.width, m.buildClient != nil)
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

	// Overlay viz picker
	if m.mode == ModeVizPicker {
		dialog := m.renderVizPickerDialog()
		page = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceForeground(ColorBg))
	}

	// Overlay Build launch dialog
	if m.mode == ModeBuildLaunch {
		dialog := m.renderBuildLaunchDialog()
		page = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog,
			lipgloss.WithWhitespaceChars(" "),
			lipgloss.WithWhitespaceForeground(ColorBg))
	}

	return page
}

// renderPreviewHeader renders the toolbar above the session preview: a muted
// breadcrumb on the left, a status dot on the right, with a rule between them.
func (m Model) renderPreviewHeader(width int) string {
	sel, ok := m.selectedSession()
	if !ok {
		return ""
	}

	crumbSource := "local"
	if sel.Source == SourceBuild {
		crumbSource = "build"
	}
	crumb := Breadcrumb(crumbSource, sel.DisplayName())

	status := ""
	if sel.Source == SourceBuild && sel.Build != nil {
		variant := VariantMuted
		if sel.Build.Live {
			variant = VariantAccent
		}
		status = StatusDot(buildStatusLabel(sel.Build.Status), variant)
	} else if st, ok := m.sessions.Statuses[sel.Key()]; ok {
		switch st {
		case sources.ClaudeStatusIdle:
			status = StatusDot("Idle", VariantMuted)
		case sources.ClaudeStatusWorking:
			status = StatusDot("Working", VariantAccent)
		}
	}

	left := crumb
	if status != "" {
		left += "  " + status
	}

	pad := width - lipgloss.Width(left) - 1
	if pad < 1 {
		return left
	}
	return left + " " + MutedText.Render(strings.Repeat("─", pad))
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

// vizTick drives visualizer animation at ~16fps.
func (m Model) vizTick() tea.Cmd {
	return tea.Tick(time.Second/16, func(time.Time) tea.Msg { return vizTickMsg{} })
}

// remerge rebuilds the unified session list from both sources and keeps the
// cursor in range.
func (m *Model) remerge() {
	m.sessions.Sessions = MergeSessions(m.legacySessions, m.buildSessions)
	if m.sessions.Cursor >= len(m.sessions.Sessions) {
		m.sessions.Cursor = len(m.sessions.Sessions) - 1
		if m.sessions.Cursor < 0 {
			m.sessions.Cursor = 0
		}
	}
}

// fetchBuild polls the Build session list through the buildctl contract.
// When no client is resolved it is a no-op; the unavailable indicator was
// already set at startup.
func (m Model) fetchBuild() tea.Cmd {
	client := m.buildClient
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		sessions, err := client.ListSessions(context.Background())
		return buildDataMsg{Sessions: sessions, Err: err}
	}
}

// fetchBuildProjects lists launchable projects: non-archived local projects
// only, per the contract's Goal 1 scoping.
func (m Model) fetchBuildProjects() tea.Cmd {
	client := m.buildClient
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		projects, err := client.ListProjects(context.Background())
		if err != nil {
			return buildProjectsMsg{Err: err}
		}
		var local []buildctl.Project
		for _, p := range projects {
			if !p.Archived && p.HostKind == "local" {
				local = append(local, p)
			}
		}
		return buildProjectsMsg{Projects: local}
	}
}

func (m Model) selectedSession() (MergedSession, bool) {
	if len(m.sessions.Sessions) == 0 || m.sessions.Cursor >= len(m.sessions.Sessions) {
		return MergedSession{}, false
	}
	return m.sessions.Sessions[m.sessions.Cursor], true
}

func (m Model) selectedSessionKey() string {
	if sel, ok := m.selectedSession(); ok {
		return sel.Key()
	}
	return ""
}

// refreshPreview updates the preview pane for the current selection. Legacy
// sessions show captured pane content; Build sessions show contract data
// only — Cockpit never scrapes Build's private tmux server.
func (m *Model) refreshPreview() tea.Cmd {
	sel, ok := m.selectedSession()
	if !ok {
		return nil
	}
	if sel.Source == SourceBuild {
		m.sessionPreview = buildPreviewText(sel)
		return nil
	}
	name := sel.Legacy.Name
	key := sel.Key()
	maxLines := m.layout.SessionsH - 6 // cards take ~4 rows, leave rest for preview
	if maxLines < 3 {
		maxLines = 3
	}
	return func() tea.Msg {
		content, err := sources.CapturePane(context.Background(), name, maxLines)
		if err != nil {
			return previewDataMsg{Content: MutedText.Render("(no preview available)"), Session: key}
		}
		return previewDataMsg{Content: content, Session: key}
	}
}

// buildPreviewText renders the contract-known facts about a Build session.
// All interpolated values are contract data and are sanitized for display.
func buildPreviewText(sel MergedSession) string {
	b := sel.Build
	lines := []string{
		fmt.Sprintf("project: %s   agent: %s", SanitizeDisplay(b.ProjectLabel), SanitizeDisplay(b.Agent)),
		fmt.Sprintf("status: %s · live=%t · attachable=%t · resumable=%t",
			buildStatusLabel(b.Status), b.Live, b.Attachable, b.Resumable),
	}
	if b.RunID != nil {
		lines = append(lines, "run: "+SanitizeDisplay(*b.RunID))
	} else {
		lines = append(lines, "run: (none)")
	}
	lines = append(lines, "conversation: "+SanitizeDisplay(b.ConversationID))
	if idle := formatIdleTime(b.UpdatedAt); idle != "" {
		lines = append(lines, "updated: "+idle+" ago")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = ModeNavigation
		m.searchInput.Blur()
		return nil
	case "enter":
		if len(m.searchResults) > 0 {
			key := m.searchResults[m.searchCursor]
			m.mode = ModeNavigation
			m.searchInput.Blur()
			// Results store identity keys, not positions: the merged list
			// re-sorts on every refresh while the dialog is open, so an index
			// could silently point at a different session by Enter time.
			idx := m.sessionIndexByKey(key)
			if idx < 0 {
				m.transientErr = "⚠ session is no longer available"
				m.transientTimer = 3
				return tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} })
			}
			m.sessions.Cursor = idx
			return m.activateSession(idx)
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

func (m *Model) handleVizPickerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "V", "q":
		m.mode = ModeNavigation
		return nil
	case "enter":
		m.viz.Select(m.vizPickerCursor)
		m.mode = ModeNavigation
		return nil
	case "up", "k", "ctrl+k":
		if m.vizPickerCursor > 0 {
			m.vizPickerCursor--
		}
		return nil
	case "down", "j", "ctrl+j":
		if m.vizPickerCursor < len(m.viz.Visualizers)-1 {
			m.vizPickerCursor++
		}
		return nil
	}
	return nil
}

func (m *Model) renderVizPickerDialog() string {
	dialogW := 40
	if m.width < 44 {
		dialogW = m.width - 4
	}

	var lines []string
	lines = append(lines, AccentText.Bold(true).Render("Visualizer"))
	lines = append(lines, "")

	for i, v := range m.viz.Visualizers {
		name := v.Name()
		marker := "   "
		if i == m.viz.Current {
			marker = MutedText.Render(" • ")
		}
		line := marker + name
		if i == m.vizPickerCursor {
			line = AccentText.Bold(true).Render("▸ ") + AccentText.Bold(true).Render(name)
			if i == m.viz.Current {
				line = AccentText.Bold(true).Render("▸") + MutedText.Render("•") + AccentText.Bold(true).Render(name)
			}
		}
		lines = append(lines, "  "+line)
	}

	lines = append(lines, "")
	lines = append(lines, MutedText.Render("  ↑↓ navigate  Enter select  Esc cancel"))

	content := strings.Join(lines, "\n")

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogW)

	return style.Render(content)
}

func (m *Model) updateSearchResults() {
	query := strings.ToLower(m.searchInput.Value())
	m.searchResults = nil
	m.searchCursor = 0

	for _, s := range m.sessions.Sessions {
		if query == "" || strings.Contains(strings.ToLower(s.DisplayName()), query) {
			m.searchResults = append(m.searchResults, s.Key())
		}
	}
}

// sessionIndexByKey resolves a stable identity key to the current list
// position, or -1 when the session is gone.
func (m Model) sessionIndexByKey(key string) int {
	for i, s := range m.sessions.Sessions {
		if s.Key() == key {
			return i
		}
	}
	return -1
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
	for vi, key := range m.searchResults {
		if vi >= maxVisible {
			lines = append(lines, MutedText.Render(fmt.Sprintf("  … %d more", len(m.searchResults)-maxVisible)))
			break
		}

		ri := m.sessionIndexByKey(key)
		if ri < 0 {
			// Session vanished after the query ran — show it as gone rather
			// than pointing at whatever now occupies its old position.
			lines = append(lines, MutedText.Render("    ○ (session gone)"))
			continue
		}
		s := m.sessions.Sessions[ri]

		// Status indicator
		statusDot := MutedText.Render("○")
		if s.Source == SourceBuild && s.Build != nil {
			if s.Build.Live {
				statusDot = SuccessText.Render("●")
			} else {
				statusDot = ErrorText.Render("●")
			}
		} else if st, ok := m.sessions.Statuses[s.Key()]; ok {
			switch st {
			case sources.ClaudeStatusIdle:
				statusDot = ErrorText.Render("●")
			case sources.ClaudeStatusWorking:
				statusDot = SuccessText.Render("●")
			}
		}

		name := s.DisplayName()
		if s.Source == SourceBuild {
			name = "⚡ " + name
		}
		if vi == m.searchCursor {
			name = AccentText.Bold(true).Render(name)
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

// handleBuildLaunchKey drives the multi-step Build launch dialog:
// project → agent → permission → optional prompt.
func (m *Model) handleBuildLaunchKey(msg tea.KeyMsg) tea.Cmd {
	if msg.String() == "esc" {
		if m.launchStep > 0 {
			m.launchStep--
			m.launchErr = ""
			m.launchInput.Blur()
			return nil
		}
		m.mode = ModeNavigation
		m.launchInput.Blur()
		return nil
	}

	switch m.launchStep {
	case 0: // project
		switch msg.String() {
		case "up", "k":
			if m.launchCursor > 0 {
				m.launchCursor--
			}
		case "down", "j":
			if m.launchCursor < len(m.launchProjects)-1 {
				m.launchCursor++
			}
		case "enter":
			if m.launchLoading {
				return nil
			}
			if len(m.launchProjects) == 0 {
				m.launchErr = "no local Build projects to launch into"
				return nil
			}
			m.launchErr = ""
			m.launchStep = 1
		}
	case 1: // agent
		switch msg.String() {
		case "left", "h", "right", "l", "tab":
			m.launchAgent = 1 - m.launchAgent
		case "enter":
			m.launchStep = 2
		}
	case 2: // permission — dangerous only when explicitly chosen
		switch msg.String() {
		case "left", "h", "right", "l", "tab":
			m.launchPermission = 1 - m.launchPermission
		case "enter":
			m.launchStep = 3
			m.launchInput.Focus()
		}
	case 3: // optional prompt; empty means no prompt
		if msg.String() == "enter" {
			return m.submitBuildLaunch()
		}
	}
	return nil
}

// submitBuildLaunch validates the dialog state and launches through the
// contract, then returns to navigation and refreshes.
func (m *Model) submitBuildLaunch() tea.Cmd {
	if m.buildClient == nil || len(m.launchProjects) == 0 || m.launchCursor >= len(m.launchProjects) {
		m.mode = ModeNavigation
		m.launchInput.Blur()
		return nil
	}
	project := m.launchProjects[m.launchCursor]
	agent := []string{"claude", "codex"}[m.launchAgent]
	perm := []string{"standard", "dangerous"}[m.launchPermission]
	prompt := strings.TrimSpace(m.launchInput.Value())
	client := m.buildClient

	m.mode = ModeNavigation
	m.launchInput.Blur()

	return func() tea.Msg {
		_, err := client.Launch(context.Background(), buildctl.LaunchOptions{
			ProjectID:  project.ID,
			Agent:      agent,
			Permission: perm,
			Prompt:     prompt,
		})
		return buildActionResultMsg{Verb: "launch", Err: err}
	}
}

func (m *Model) renderBuildLaunchDialog() string {
	dialogW := 64
	if m.width < 68 {
		dialogW = m.width - 4
	}

	var lines []string
	lines = append(lines, AccentText.Bold(true).Render("Launch Build Session"))
	lines = append(lines, "")

	// Step 0: project
	if m.launchStep == 0 {
		lines = append(lines, BoldText.Render("Project:"))
		if m.launchLoading {
			lines = append(lines, MutedText.Render("  ⠋ loading projects..."))
		} else if len(m.launchProjects) == 0 && m.launchErr == "" {
			lines = append(lines, MutedText.Render("  (no local projects)"))
		}
		for i, p := range m.launchProjects {
			marker := "  "
			style := lipgloss.NewStyle().Foreground(ColorFg)
			if i == m.launchCursor {
				marker = "▸ "
				style = style.Foreground(ColorAccent).Bold(true)
			}
			lines = append(lines, marker+style.Render(SanitizeDisplay(p.Label))+"  "+MutedText.Render(config.CollapseTilde(SanitizeDisplay(p.RootPath))))
		}
	} else {
		p := m.launchProjects[m.launchCursor]
		lines = append(lines, MutedText.Render("Project: ")+SanitizeDisplay(p.Label))
	}

	// Step 1: agent
	if m.launchStep == 1 {
		lines = append(lines, "")
		lines = append(lines, BoldText.Render("Agent:"))
		lines = append(lines, "  "+choicePill("claude", m.launchAgent == 0)+"  "+choicePill("codex", m.launchAgent == 1))
	} else if m.launchStep > 1 {
		lines = append(lines, MutedText.Render("Agent: ")+[]string{"claude", "codex"}[m.launchAgent])
	}

	// Step 2: permission
	if m.launchStep == 2 {
		lines = append(lines, "")
		lines = append(lines, BoldText.Render("Permission:"))
		lines = append(lines, "  "+choicePill("standard", m.launchPermission == 0)+"  "+choicePill("dangerous", m.launchPermission == 1))
	} else if m.launchStep > 2 {
		lines = append(lines, MutedText.Render("Permission: ")+[]string{"standard", "dangerous"}[m.launchPermission])
	}

	// Step 3: prompt
	if m.launchStep == 3 {
		lines = append(lines, "")
		lines = append(lines, BoldText.Render("Prompt (optional):"))
		lines = append(lines, "> "+m.launchInput.View())
	}

	if m.launchErr != "" {
		lines = append(lines, "")
		lines = append(lines, ErrorText.Render("  "+m.launchErr))
	}

	lines = append(lines, "")
	switch m.launchStep {
	case 0:
		lines = append(lines, AccentText.Render("Enter")+" "+MutedText.Render("next")+"  "+AccentText.Render("Esc")+" "+MutedText.Render("cancel"))
	case 1, 2:
		lines = append(lines, AccentText.Render("←→")+" "+MutedText.Render("choose")+"  "+AccentText.Render("Enter")+" "+MutedText.Render("next")+"  "+AccentText.Render("Esc")+" "+MutedText.Render("back"))
	default:
		lines = append(lines, AccentText.Render("Enter")+" "+MutedText.Render("launch")+"  "+AccentText.Render("Esc")+" "+MutedText.Render("back"))
	}

	content := strings.Join(lines, "\n")

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogW)

	return style.Render(content)
}

// choicePill renders one selectable option in a choice row.
func choicePill(label string, selected bool) string {
	if selected {
		return lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorAccent).
			Bold(true).
			Padding(0, 1).
			Render(label)
	}
	return MutedText.Render(" " + label + " ")
}

// fetchSessionStatuses polls pane content for legacy sessions only. Build
// session status comes from the contract — never from pane scraping.
func (m Model) fetchSessionStatuses() tea.Cmd {
	var legacy []sources.TmuxSession
	for _, s := range m.sessions.Sessions {
		if s.Source == SourceLegacy && s.Legacy != nil {
			legacy = append(legacy, *s.Legacy)
		}
	}
	return func() tea.Msg {
		ctx := context.Background()
		snapshots := make(map[string]string, len(legacy))
		for _, s := range legacy {
			content, err := sources.CapturePaneContent(ctx, s.Name)
			if err != nil {
				continue
			}
			snapshots["legacy:"+s.Name] = content
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
