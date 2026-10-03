package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/process"
	"github.com/jeffdhooton/cockpit/sources"
)

var validLabel = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Mode represents the TUI interaction mode.
type Mode int

const (
	ModeNavigation Mode = iota
	ModeCapture
	ModeNewSession
	ModeSearch
	ModeAttentionFilter // typing into the attention queue's filter
	ModeConfirm         // a process action awaits confirmation
	ModeSessionsFilter  // typing into the session view's filter
)

// ViewMode selects the top-level layout.
type ViewMode int

const (
	ViewGrid      ViewMode = iota // unified sessions + repos tile wall
	ViewSessions                  // sessions left, selected session's panes right (default)
	ViewAttention                 // the attention queue
	ViewProcesses                 // one project's process panel
)

// Model is the root Bubbletea model.
type Model struct {
	config     *config.Config
	configPath string
	width      int
	height     int
	mode       Mode

	view       ViewMode
	gridCursor string // label of the selected target; survives list churn
	gridIndex  int    // last resolved index, a fallback when the label is gone
	// gridHost is the machine whose grid is open. Empty is the root, where
	// each host shows as a single box. Backspace pops back to it.
	gridHost string
	// gridRootCursor is the root selection held while a host is open, so
	// backspace lands on the box you came from rather than at the top.
	gridRootCursor string

	sessions       SessionsModel
	repos          ReposModel
	github         *sources.GitHubStatus
	githubAt       time.Time
	processes      map[string][]sources.ProcessInfo // repo label → configured process state
	hosts          map[string]hostState             // remote host → last poll and link state
	hermes         map[string]sources.HermesStatus  // hermes label → last status
	hermesAt       time.Time
	spine          *sources.SpineStatus // last spine bearings read; nil before the first
	spineAt        time.Time
	spineScroll    int    // lines the spine preview is scrolled down; 0 off the spine tile
	sessionPreview string // the grid's preview of the selected local session

	// Local host observation beyond the session list: pane records, the
	// read outcome, and per-project process observations. A failed read
	// keeps the last-known data and marks it unavailable.
	localPanes      []sources.PaneReport
	localObservedAt time.Time
	localOutcome    sources.Observation
	localErr        string
	procObs         map[string]sources.ProcessObservation // repo key → observation
	procErrs        map[string]string                     // repo key → read failure

	// svc is the shared process service; every start, stop, restart,
	// adoption and reconcile goes through it, as it does in the daemon.
	svc *process.Service
	// sess, attn and procs are the views; nav is the return stack.
	sess  sessionsModel
	attn  attentionModel
	procs processesModel
	nav   []returnPoint
	// now is a clock seam for tests.
	now func() time.Time

	transientErr   string
	transientTimer int

	// captureInput is the one-line capture prompt (c).
	captureInput textinput.Model

	// New session dialog state
	newSessionInput textinput.Model
	newSessionStep  int    // 0=path, 1=label confirm
	newSessionPath  string // expanded path from step 0
	newSessionErr   string // inline validation error

	// Session search (/ key on the grid)
	searchInput   textinput.Model
	searchResults []int // indices into sessions.Sessions
	searchCursor  int
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

	ci := textinput.New()
	ci.Placeholder = "capture a thought"
	ci.CharLimit = 512
	ci.Width = 60

	m := Model{
		config:          cfg,
		configPath:      configPath,
		sessions:        NewSessionsModel(),
		repos:           NewReposModel(),
		newSessionInput: ti,
		searchInput:     si,
		captureInput:    ci,
		svc:             process.New(cfg, sources.DefaultRunner()),
		sess:            newSessionsModel(),
		attn:            newAttentionModel(),
		procs:           newProcessesModel(),
		now:             time.Now,
		localOutcome:    sources.ObservationUnavailable,
		localErr:        "not read yet",
		view:            ViewSessions,
	}
	if cfg.General.DefaultView == "grid" {
		m.view = ViewGrid
	}
	return m
}

// Message types for source data
type (
	tmuxDataMsg struct {
		Sessions []sources.TmuxSession
		Panes    []sources.PaneReport
		At       time.Time
		Err      error
	}
	hermesDataMsg  struct{ Status sources.HermesStatus }
	hermesTickMsg  struct{ Label string }
	gitDataMsg     struct{ Repos []sources.GitRepoStatus }
	githubDataMsg  struct{ Status *sources.GitHubStatus }
	processDataMsg struct {
		ByLabel map[string][]sources.ProcessInfo
		Obs     map[string]sources.ProcessObservation
		Errs    map[string]string
	}
	sourceErrMsg struct {
		Source string
		Err    error
	}
	previewDataMsg struct {
		Content string
		Session string
	}
	localTickMsg        struct{}
	remoteTickMsg       struct{}
	clearErrMsg         struct{}
	configSaveResultMsg struct{ Err error }
)

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchTmux(),
		m.fetchGit(),
		m.fetchGitHub(),
		m.fetchProcesses(),
		m.localTick(),
		m.remoteTick(),
		m.fetchHosts(),
		m.fetchHermes(),
		m.fetchSpine(),
	)
}

// fetchHermes polls every configured Hermes dashboard.
func (m Model) fetchHermes() tea.Cmd {
	var cmds []tea.Cmd
	for _, h := range m.config.Hermes {
		cmds = append(cmds, m.fetchOneHermes(h))
	}
	return tea.Batch(cmds...)
}

func (m Model) fetchOneHermes(h config.HermesConfig) tea.Cmd {
	return func() tea.Msg {
		return hermesDataMsg{Status: sources.GetHermesStatus(context.Background(), http.DefaultClient, h)}
	}
}

func (m Model) hermesTick(h config.HermesConfig) tea.Cmd {
	d := time.Duration(h.RefreshInterval) * time.Second
	return tea.Tick(d, func(time.Time) tea.Msg { return hermesTickMsg{Label: h.Label} })
}

// fetchHosts starts a poll of every configured remote host.
func (m Model) fetchHosts() tea.Cmd {
	var cmds []tea.Cmd
	for _, h := range m.config.Hosts {
		cmds = append(cmds, m.fetchHost(h))
	}
	return tea.Batch(cmds...)
}

// transient shows a one-line notice under the view for a few seconds.
func (m *Model) transient(text string) tea.Cmd {
	m.transientErr = text
	m.transientTimer = 3
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	modeBefore := m.mode

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Reserve the final column. Every line cockpit draws ends in a border
		// character, and writing the terminal's last cell leaves it in a
		// pending-wrap state that desynchronises Bubbletea's line diffing —
		// tiles lose their bottom border and gain stray blank lines. One
		// column is a cheap price for a frame that stays aligned.
		m.width = msg.Width - 1
		if m.width < 0 {
			m.width = 0
		}
		m.height = msg.Height

	case tea.KeyMsg:
		cmd := m.handleKey(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case tmuxDataMsg:
		if msg.Err != nil {
			// Keep the last-known sessions; say the read failed. An
			// unreadable server is not an empty one.
			m.localOutcome = sources.ObservationUnavailable
			m.localErr = msg.Err.Error()
			m.sessions.Loading = false
			cmds = append(cmds, m.transient("⚠ tmux: "+msg.Err.Error()), m.recomputeAttention())
			break
		}
		// Filter out the cockpit session itself
		var filtered []sources.TmuxSession
		for _, s := range msg.Sessions {
			if s.Name != m.config.General.SessionName {
				filtered = append(filtered, s)
			}
		}
		m.sessions.Sessions = filtered
		m.sessions.Loading = false
		m.localPanes = msg.Panes
		m.localObservedAt = msg.At
		m.localOutcome = sources.ObservationFresh
		m.localErr = ""
		// A hook-reported status arrives on the same list-sessions call.
		m.sessions.AdoptReported()
		cmds = append(cmds, m.recomputeAttention(), m.fetchPreview())

	case previewDataMsg:
		if msg.Session == m.gridLocalSession() {
			m.sessionPreview = msg.Content
		}

	case panePreviewMsg:
		m.sess.applyPreview(msg)

	case githubDataMsg:
		m.github = msg.Status
		m.githubAt = m.now()
		cmds = append(cmds, m.recomputeAttention())

	case processDataMsg:
		m.processes = msg.ByLabel
		m.procObs = msg.Obs
		m.procErrs = msg.Errs
		cmds = append(cmds, m.recomputeAttention())

	case hostDataMsg:
		if m.hosts == nil {
			m.hosts = map[string]hostState{}
		}
		m.hosts[msg.Host] = mergeHost(m.hosts[msg.Host], msg.hostPoll)
		if h, ok := m.config.Host(msg.Host); ok {
			cmds = append(cmds, m.hostTick(h))
		}
		cmds = append(cmds, m.recomputeAttention())

	case hermesDataMsg:
		if m.hermes == nil {
			m.hermes = map[string]sources.HermesStatus{}
		}
		m.hermes[msg.Status.Label] = msg.Status
		m.hermesAt = m.now()
		for _, h := range m.config.Hermes {
			if h.Label == msg.Status.Label {
				cmds = append(cmds, m.hermesTick(h))
			}
		}
		cmds = append(cmds, m.recomputeAttention())

	case spineDataMsg:
		st := msg.Status
		// A shorter snapshot starts again from the top rather than leaving
		// the window parked past what is left.
		if spineLineCount(&st, m.now(), m.width-4) < spineLineCount(m.spine, m.now(), m.width-4) {
			m.spineScroll = 0
		}
		m.spine = &st
		m.spineAt = msg.At
		cmds = append(cmds, m.recomputeAttention())

	case gitDataMsg:
		m.repos.Repos = msg.Repos
		m.repos.Loading = false
		cmds = append(cmds, m.recomputeAttention())

	case attachResultMsg:
		if msg.Gone {
			m.attn.message = "This target is no longer available"
			m.procs.message = "This target is no longer available"
			m.sess.message = "This target is no longer available"
			cmds = append(cmds, m.refreshAll())
		} else if msg.Err != nil {
			cmds = append(cmds, m.transient("⚠ attach: "+msg.Err.Error()))
		}

	case procObsMsg:
		if m.procs.apply(msg) && m.view == ViewProcesses {
			cmds = append(cmds, m.fetchProcOutput(m.procs.output.lines))
		}

	case procOutputMsg:
		m.procs.applyOutput(msg)

	case procActionMsg:
		if msg.key == m.procs.key {
			delete(m.procs.pending, msg.process)
			m.procs.message = msg.res.Message
			if msg.res.Observation != nil {
				m.procs.obs = msg.res.Observation
				m.procs.observedAt = msg.res.Observation.ObservedAt
				m.procs.err = ""
				m.procs.resolveSelection()
			}
			cmds = append(cmds, m.fetchProcObs(), m.fetchProcesses())
		}

	case hermesTickMsg:
		for _, h := range m.config.Hermes {
			if h.Label == msg.Label {
				cmds = append(cmds, m.fetchOneHermes(h))
			}
		}

	case hostTickMsg:
		if h, ok := m.config.Host(msg.Host); ok {
			cmds = append(cmds, m.fetchHost(h))
		}

	case sourceErrMsg:
		cmds = append(cmds, m.transient("⚠ "+msg.Source+": "+msg.Err.Error()))

	case clearErrMsg:
		m.transientTimer--
		if m.transientTimer <= 0 {
			m.transientErr = ""
		} else {
			cmds = append(cmds, tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} }))
		}

	case sessionSavedMsg:
		// Add to in-memory config and refresh
		m.config.Repos = append(m.config.Repos, msg.Repo)
		cmds = append(cmds, m.transient("✓ saved "+msg.Repo.Label+" to config"), m.fetchGit())

	case configSaveResultMsg:
		if msg.Err != nil {
			cmds = append(cmds, m.transient("⚠ config save: "+msg.Err.Error()))
		}

	case localTickMsg:
		cmds = append(cmds,
			m.fetchTmux(),
			m.fetchGit(),
			m.fetchProcesses(),
			m.fetchSpine(),
			m.localTick(),
		)
		if m.view == ViewProcesses && m.mode != ModeConfirm {
			cmds = append(cmds, m.fetchProcObs())
		}
		if m.view == ViewSessions {
			cmds = append(cmds, m.fetchPanePreview())
		}

	case remoteTickMsg:
		cmds = append(cmds,
			m.fetchGitHub(),
			m.remoteTick(),
		)

	case tmuxSwitchResultMsg:
		if msg.Err != nil {
			cmds = append(cmds, m.transient("⚠ tmux: "+msg.Err.Error()))
		}
		// On success: do nothing. The tmux client switched away but cockpit
		// keeps running in the background. User returns via prefix+S or `cockpit`.
	}

	// Forward keys to the capture prompt — skip the key that entered the mode
	if modeBefore == ModeCapture && m.mode == ModeCapture {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.captureInput, cmd = m.captureInput.Update(msg)
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

	// Forward keys to the attention filter while typing into it.
	if modeBefore == ModeAttentionFilter && m.mode == ModeAttentionFilter {
		if _, ok := msg.(tea.KeyMsg); ok {
			prev := m.attn.filter.Value()
			var cmd tea.Cmd
			m.attn.filter, cmd = m.attn.filter.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if m.attn.filter.Value() != prev {
				m.attn.query = m.attn.filter.Value()
				m.attn.resolved = nil
				m.attn.index = 0
				m.attn.selected = ""
				m.attn.resolveSelection()
			}
		}
	}

	// Forward keys to the session filter while typing into it.
	if modeBefore == ModeSessionsFilter && m.mode == ModeSessionsFilter {
		if _, ok := msg.(tea.KeyMsg); ok {
			prev := m.sess.filter.Value()
			var cmd tea.Cmd
			m.sess.filter, cmd = m.sess.filter.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if m.sess.filter.Value() != prev {
				m.sess.query = m.sess.filter.Value()
				m.sess.resolve()
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
	case ModeAttentionFilter:
		return m.handleAttentionFilterKey(msg)
	case ModeSessionsFilter:
		return m.handleSessionsFilterKey(msg)
	case ModeConfirm:
		return m.handleConfirmKey(msg)
	default:
		return m.handleNavKey(msg)
	}
}

func (m *Model) handleNavKey(msg tea.KeyMsg) tea.Cmd {
	switch m.view {
	case ViewGrid:
		return m.handleGridKey(msg)
	case ViewAttention:
		return m.handleAttentionKey(msg)
	case ViewProcesses:
		return m.handleProcessesKey(msg)
	default:
		return m.handleSessionsKey(msg)
	}
}

// startCapture opens the one-line capture prompt from any view.
func (m *Model) startCapture() tea.Cmd {
	m.mode = ModeCapture
	m.captureInput.Reset()
	return m.captureInput.Focus()
}

func (m *Model) handleCaptureKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = ModeNavigation
		m.captureInput.Blur()
		m.captureInput.Reset()
	case "enter":
		text := strings.TrimSpace(m.captureInput.Value())
		m.mode = ModeNavigation
		m.captureInput.Blur()
		m.captureInput.Reset()
		if text == "" {
			return nil
		}
		file := m.config.Obsidian.TodayFile
		if file == "" {
			return m.transient("⚠ capture: no today_file configured under [obsidian]")
		}
		if err := sources.AppendInbox(file, text); err != nil {
			return m.transient("⚠ capture: " + err.Error())
		}
		return m.transient("✓ captured")
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

	svc := m.svc
	cmds = append(cmds, func() tea.Msg {
		err := tmuxJumpRepo(svc, repo)
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

// saveSessionAsRepo adds the named session to config as a repo. The label is
// passed in rather than read off a cursor: the grid's selection and the
// sessions list are ordered differently, and the tmux calls below run on this
// machine, so the caller is the one that knows the session is local.
func (m *Model) saveSessionAsRepo(label string) tea.Cmd {
	if label == "" {
		return nil
	}

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
	now := m.now
	return func() tea.Msg {
		at := now()
		obs, err := sources.ObserveHost(context.Background(), sources.DefaultRunner(), "", at)
		if err != nil {
			return tmuxDataMsg{Err: err, At: at}
		}
		return tmuxDataMsg{Sessions: obs.Sessions, Panes: obs.Panes, At: at}
	}
}

func (m Model) fetchGit() tea.Cmd {
	repos := m.localRepos()
	return func() tea.Msg {
		results := sources.GetGitStatus(context.Background(), sources.LocalCommandRunner{}, repos)
		return gitDataMsg{Repos: results}
	}
}

func (m Model) fetchGitHub() tea.Cmd {
	if !m.config.GitHub.Enabled {
		return nil
	}
	repos := m.localRepos()
	return func() tea.Msg {
		status := sources.GetGitHubStatus(context.Background(), repos)
		return githubDataMsg{Status: status}
	}
}

// fetchProcesses polls window state for every repo that declares processes.
// Repos without processes are skipped so the poll costs nothing for users who
// never configured any.
func (m Model) fetchProcesses() tea.Cmd {
	var repos []config.RepoConfig
	for _, r := range m.localRepos() {
		if len(r.Processes) > 0 {
			repos = append(repos, r)
		}
	}
	if len(repos) == 0 {
		return nil
	}
	now := m.now
	return func() tea.Msg {
		ctx := context.Background()
		r := sources.DefaultRunner()
		msg := processDataMsg{
			ByLabel: make(map[string][]sources.ProcessInfo, len(repos)),
			Obs:     make(map[string]sources.ProcessObservation, len(repos)),
			Errs:    map[string]string{},
		}
		for _, repo := range repos {
			obs, err := sources.ObserveProcesses(ctx, r, repo, now())
			if err != nil {
				// A failed read is recorded, not dropped: the queue must
				// show reduced coverage rather than a clean project.
				msg.Errs[repo.Key()] = err.Error()
				continue
			}
			msg.ByLabel[repo.Label] = obs.Processes
			msg.Obs[repo.Key()] = obs
		}
		return msg
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

// fetchPreview reads the grid's selected local session's visible screen. It
// is skipped below the mobile width, where nothing renders it, and outside
// the grid, which has no preview panel.
func (m Model) fetchPreview() tea.Cmd {
	if m.width < MobileMaxWidth || m.view != ViewGrid {
		return nil
	}
	name := m.gridLocalSession()
	if name == "" {
		return nil
	}
	return func() tea.Msg {
		out, err := sources.DefaultRunner().Run(context.Background(), sources.CapturePaneVisibleArgs(name)...)
		if err != nil {
			return previewDataMsg{Content: MutedText.Render("(no preview available)"), Session: name}
		}
		return previewDataMsg{Content: trimPadding(sources.StripControl(out)), Session: name}
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
			case sources.AgentStatusIdle:
				statusDot = ErrorText.Render("●")
			case sources.AgentStatusWorking:
				statusDot = SuccessText.Render("●")
			case sources.AgentStatusNeedsInput:
				statusDot = WarningText.Render("●")
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

// tmuxSwitch switches to an existing tmux session.
func tmuxSwitch(name string) error {
	return exec.Command("tmux", "switch-client", "-t", name).Run()
}

// tmuxJumpRepo switches to a repo's tmux session, creating it if needed and
// bringing its configured processes up as sibling windows, through the
// shared service so the project lock and the stop overrides apply.
//
// Window 0 stays a plain shell and is what you land on, so a project with a
// noisy dev server does not drop you into a log.
func tmuxJumpRepo(svc *process.Service, repo config.RepoConfig) error {
	if !validLabel.MatchString(repo.Label) {
		return fmt.Errorf("invalid session label %q: must be alphanumeric, hyphens, or underscores", repo.Label)
	}

	ctx := context.Background()
	r := sources.DefaultRunner()

	// A process that fails to launch is worth knowing about, but it must never
	// stand between the user and the session they asked for.
	created, _, err := svc.Prepare(ctx, r, repo)
	if err != nil {
		return err
	}

	if created {
		_, _ = r.Run(ctx, sources.SelectFirstWindowArgs(repo.Label)...)
	}
	return exec.Command("tmux", "switch-client", "-t", repo.Label).Run()
}

// repoForLabel resolves a jump target to its configured repo, falling back to
// a bare repo so sessions cockpit does not manage stay jumpable.
func (m Model) repoForLabel(label, path string) config.RepoConfig {
	if m.config != nil {
		if repo, ok := m.config.Repo(label); ok {
			return repo
		}
	}
	return config.RepoConfig{Label: label, Path: path}
}
