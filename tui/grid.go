package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
)

// Target is one tile in the grid: a running tmux session, a saved repo with no
// session, or both joined on session.Name == repo.Label — the same identity
// tmuxJump already assumes when it switches to a session named for a repo.
type Target struct {
	Label string
	// Host is the machine the target lives on; empty means local. Two hosts
	// may each have a "docket", and they must never share a tile.
	Host    string
	Session *sources.TmuxSession
	Repo    *sources.GitRepoStatus
	Status  sources.AgentStatus
	// StatusReported is true when Status came from an agent hook rather than
	// the pane-hash guess. The tile dims a guess so it never reads as a fact.
	StatusReported bool
	// Unreachable is true when the target's host failed its last poll. The
	// data shown is last-known, and the tile says so.
	Unreachable bool
	Processes   []sources.ProcessInfo
}

// Running reports whether the target has a live tmux session behind it.
func (t Target) Running() bool { return t.Session != nil }

// Key identifies the target across hosts: host/label remotely, label locally.
func (t Target) Key() string {
	if t.Host == "" {
		return t.Label
	}
	return t.Host + "/" + t.Label
}

// Name is what the tile shows: the key, so a remote tile names its host.
func (t Target) Name() string { return t.Key() }

// AttachProcesses joins per-repo process state onto the tiles. It is separate
// from BuildTargets because process data arrives on its own poll and should
// never delay the grid.
func AttachProcesses(targets []Target, byLabel map[string][]sources.ProcessInfo) []Target {
	for i := range targets {
		if infos, ok := byLabel[targets[i].Key()]; ok {
			targets[i].Processes = infos
		}
	}
	return targets
}

// processIndicator renders the live/configured process count for a tile,
// counting only processes the config declares. Ad-hoc windows the user opened
// are not the tile's business.
func processIndicator(infos []sources.ProcessInfo) string {
	total, running := 0, 0
	for _, i := range infos {
		if !i.Configured {
			continue
		}
		total++
		if i.State == sources.ProcessRunning {
			running++
		}
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("⚙ %d/%d", running, total)
}

// processIndicatorDegraded reports whether any configured process died. A
// process that was never started is idle, not broken.
func processIndicatorDegraded(infos []sources.ProcessInfo) bool {
	for _, i := range infos {
		if i.Configured && i.State == sources.ProcessDead {
			return true
		}
	}
	return false
}

// BuildTargets joins sessions and repos into one ordered tile list. Running
// targets come first, then dormant, alphabetical within each group. Ordering is
// deliberately not last-used: on a 5-second refresh that would move the tile
// under the cursor while the user is aiming at it.
func BuildTargets(
	sessions []sources.TmuxSession,
	repos []sources.GitRepoStatus,
	statuses map[string]sources.AgentStatus,
	selfSession string,
) []Target {
	repoByKey := make(map[string]*sources.GitRepoStatus, len(repos))
	for i := range repos {
		repoByKey[repos[i].Key()] = &repos[i]
	}

	var running, dormant []Target
	live := make(map[string]bool, len(sessions))

	for i := range sessions {
		s := &sessions[i]
		// Cockpit's own session is excluded on every host, and so is a local
		// view session that exists only to hold ssh windows onto a remote.
		if s.Name == selfSession || s.ViewOf != "" {
			continue
		}
		live[s.Key()] = true
		running = append(running, Target{
			Label:          s.Name,
			Host:           s.Host,
			Session:        s,
			Repo:           repoByKey[s.Key()],
			Status:         statuses[s.Key()],
			StatusReported: s.StatusReported,
		})
	}

	for i := range repos {
		r := &repos[i]
		if live[r.Key()] {
			continue
		}
		dormant = append(dormant, Target{Label: r.Label, Host: r.Host, Repo: r})
	}

	sort.Slice(running, func(i, j int) bool { return running[i].Key() < running[j].Key() })
	sort.Slice(dormant, func(i, j int) bool { return dormant[i].Key() < dormant[j].Key() })

	return append(running, dormant...)
}

const (
	// gridCellWidth is one tile's total footprint: 14 content + 2 border + 2 padding.
	// GridCols is measured against the grid's content width, not the terminal's,
	// so this budget excludes the enclosing panel's own border and padding.
	gridCellWidth = 18
	// gridMaxCols caps the grid. An 8-wide grid on a 200-column terminal is too
	// sparse to scan, and the width is better spent on the preview.
	gridMaxCols = 4
	// gridTileH is a tile's total height: 3 content lines + 2 border rows.
	gridTileH = 5

	// MobileMaxWidth is the threshold below which the preview is dropped and the
	// grid takes the full screen.
	MobileMaxWidth = 70
	// MinTerminalWidth is the floor below which even one tile is illegible.
	MinTerminalWidth = 24
)

// GridCols returns the column count for a given terminal width.
func GridCols(width int) int {
	cols := width / gridCellWidth
	if cols < 1 {
		return 1
	}
	if cols > gridMaxCols {
		return gridMaxCols
	}
	return cols
}

// MoveGridCursor returns the index after a directional move. Horizontal moves
// step by one and so walk the list linearly across row boundaries, which is the
// fast path on a phone. Vertical moves step by a full row and clamp to the last
// target, so a short final row is still reachable from the row above.
func MoveGridCursor(idx, count, cols, dx, dy int) int {
	if count <= 0 {
		return 0
	}
	idx += dx + dy*cols
	if idx < 0 {
		return 0
	}
	if idx >= count {
		return count - 1
	}
	return idx
}

// resolveGridCursor turns the stored cursor label into an index. When the label
// is gone — session died, repo dropped from config — it clamps the previous
// index into range so the selection lands on a neighbour instead of jumping to
// the top.
func resolveGridCursor(targets []Target, label string, prev int) int {
	for i := range targets {
		if targets[i].Label == label {
			return i
		}
	}
	if len(targets) == 0 {
		return 0
	}
	if prev < 0 {
		return 0
	}
	if prev >= len(targets) {
		return len(targets) - 1
	}
	return prev
}

// renderTile draws one target: label, status, and git state. Every piece is
// truncated to the inner width before styling, so the tile can never wrap and
// blow its 3-line content budget.
func renderTile(t Target, width int, selected bool) string {
	inner := width - 4 // 2 border cells + 2 padding cells
	if inner < 6 {
		inner = 6
	}

	nameStyle := BoldText
	switch {
	case selected:
		nameStyle = BoldText.Foreground(ColorAccent)
	case !t.Running():
		nameStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	}
	name := nameStyle.Render(Truncate(t.Name(), inner))

	// Shape carries session existence: a hollow ring means there is nothing to
	// attach to, while every live session keeps the filled status dot.
	status := StatusRing("no session", VariantMuted)
	if t.Unreachable {
		// Last-known data under a warning, never a blank tile.
		status = WarningText.Render("⚠ " + Truncate("unreachable", inner-2))
	} else if t.Running() {
		status = StatusDot("detached", VariantMuted)
		if t.Session.Attached {
			status = StatusDot("attached", VariantAccent)
		}
		// A guessed status is dimmed so it never reads as a fact. Colour is
		// the cheapest channel: it costs no width, and on a terminal without
		// styling it degrades to "looks the same" rather than "means the
		// wrong thing".
		dot := StatusDot
		if !t.StatusReported {
			dot = StatusDotDim
		}
		switch t.Status {
		case sources.AgentStatusIdle:
			label := "idle"
			if age := formatIdleTime(t.Session.LastUsed); age != "" {
				label = "idle " + age
			}
			status = dot(Truncate(label, inner-2), VariantNeutral)
		case sources.AgentStatusWorking:
			status = dot("working", VariantAccent)
		case sources.AgentStatusNeedsInput:
			// Only ever reported: the guess cannot see this state, so there
			// is no dim variant to draw.
			status = StatusDot(Truncate("needs you", inner-2), VariantWarning)
		}
	}

	git := ""
	if t.Repo != nil {
		if t.Repo.Error != nil {
			git = WarningText.Render("git err")
		} else {
			// Reserve room for the trailing markers before truncating the branch.
			branchW := inner - 6
			if branchW < 3 {
				branchW = 3
			}
			git = PurpleText.Render(Truncate(t.Repo.Branch, branchW))
			if t.Repo.Dirty {
				git += " " + StatusDirty.Render(fmt.Sprintf("✗%d", t.Repo.DirtyCount))
			} else {
				git += " " + StatusClean.Render("✓")
			}
			if t.Repo.Unpushed > 0 && lipgloss.Width(git)+3 <= inner {
				git += " " + StatusUnpushed.Render(fmt.Sprintf("↑%d", t.Repo.Unpushed))
			}
		}
	}

	// The tile has a fixed 3-line budget, so the indicator shares the git line
	// and is dropped rather than wrapped when it will not fit.
	if ind := processIndicator(t.Processes); ind != "" {
		style := MutedText
		if processIndicatorDegraded(t.Processes) {
			style = WarningText
		}
		switch {
		case git == "":
			git = style.Render(ind)
		case lipgloss.Width(git)+1+lipgloss.Width(ind) <= inner:
			git += " " + style.Render(ind)
		}
	}

	borderColor := ColorBorder
	if selected {
		borderColor = ColorAccent
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		// lipgloss Width counts padding but not border; inner is the text budget.
		Width(inner + 2).
		Height(3).
		MaxHeight(gridTileH).
		Render(name + "\n" + status + "\n" + git)
}

// RenderGrid lays targets out in a responsive grid, scrolling by row to keep the
// cursor visible and appending a muted count when tiles are clipped.
func RenderGrid(targets []Target, cursor, width, height int) string {
	if len(targets) == 0 {
		return MutedText.Render("No sessions or repos. Add repos in ") +
			AccentText.Render("~/.config/cockpit/config.toml")
	}

	cols := GridCols(width)
	cellW := width / cols
	rows := (len(targets) + cols - 1) / cols

	visibleRows := height / gridTileH
	if visibleRows < 1 {
		visibleRows = 1
	}

	offset := 0
	if cursorRow := cursor / cols; cursorRow >= visibleRows {
		offset = cursorRow - visibleRows + 1
	}

	var out []string
	for r := offset; r < rows && r < offset+visibleRows; r++ {
		var cells []string
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= len(targets) {
				cells = append(cells, lipgloss.NewStyle().Width(cellW).Height(gridTileH).Render(""))
				continue
			}
			cells = append(cells, renderTile(targets[i], cellW, i == cursor))
		}
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}

	if shown := (offset + visibleRows) * cols; shown < len(targets) {
		out = append(out, MutedText.Render(fmt.Sprintf("  ▼ %d more", len(targets)-shown)))
	}

	return strings.Join(out, "\n")
}

// View renders the active view, then any modal overlay on top of it.
func (m Model) View() string {
	if m.width < MinTerminalWidth {
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			WarningText.Render("Terminal too narrow.\nResize or press q to quit."))
	}

	page := m.dashboardView()
	if m.view == ViewGrid {
		page = m.gridView()
	}

	switch m.mode {
	case ModeNewSession:
		page = m.overlay(m.renderNewSessionDialog())
	case ModeSearch:
		page = m.overlay(m.renderSearchDialog())
	case ModeVizPicker:
		page = m.overlay(m.renderVizPickerDialog())
	}
	return page
}

// overlay centres a modal dialog over a blank ground.
func (m Model) overlay(dialog string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ColorBg))
}

// gridContentWidth is the width available to tiles: the terminal less the
// enclosing panel's 2 border and 2 padding cells. Rendering and cursor movement
// must both measure columns against this, or a keypress moves by a different
// number of columns than the eye sees.
func (m Model) gridContentWidth() int {
	w := m.width - 4
	if w < gridCellWidth {
		return gridCellWidth
	}
	return w
}

// gridTargets builds the current tile list from live sessions and configured repos.
func (m Model) gridTargets() []Target {
	sessions := append(append([]sources.TmuxSession{}, m.sessions.Sessions...), m.remoteSessions()...)
	repos := append(append([]sources.GitRepoStatus{}, m.repos.Repos...), m.remoteRepos()...)

	// Local statuses come from the sessions model, which also holds the
	// pane-hash guesses. A remote session carries its own reported status
	// and is never guessed.
	statuses := make(map[string]sources.AgentStatus, len(m.sessions.Statuses)+len(sessions))
	for k, v := range m.sessions.Statuses {
		statuses[k] = v
	}
	for _, s := range sessions {
		if s.Host != "" && s.StatusReported {
			statuses[s.Key()] = s.Status
		}
	}

	targets := BuildTargets(sessions, repos, statuses, m.config.General.SessionName)
	for i := range targets {
		if targets[i].Host == "" {
			continue
		}
		targets[i].Unreachable = m.hosts[targets[i].Host].unreachable
	}

	processes := make(map[string][]sources.ProcessInfo, len(m.processes))
	for k, v := range m.processes {
		processes[k] = v
	}
	for k, v := range m.remoteProcesses() {
		processes[k] = v
	}
	return AttachProcesses(targets, processes)
}

// gridView renders the unified grid, plus the session preview on desktop widths.
func (m Model) gridView() string {
	targets := m.gridTargets()
	cursor := resolveGridCursor(targets, m.gridCursor, m.gridIndex)

	hints := GridKeyhintsView(m.width)
	if m.transientErr != "" {
		hints = WarningText.Render(m.transientErr)
	}

	body := m.height - 1 // keyhints row
	if body < gridTileH {
		body = gridTileH
	}

	gridH := body
	showPreview := m.width >= MobileMaxWidth && len(targets) > 0
	if showPreview {
		gridH = body * 3 / 5
		if gridH < gridTileH+3 {
			gridH = gridTileH + 3
		}
	}

	// Panel chrome eats 2 border rows + 1 title row on top of the content width.
	grid := RenderGrid(targets, cursor, m.gridContentWidth(), gridH-3)
	page := RenderPanel("Cockpit", grid, m.width, gridH, true)

	if showPreview {
		page = lipgloss.JoinVertical(lipgloss.Left, page, m.renderPreviewPanel(body-gridH))
	}

	return lipgloss.JoinVertical(lipgloss.Left, page, hints)
}

// renderPreviewPanel renders the capture-pane output for the selected session.
func (m Model) renderPreviewPanel(height int) string {
	name := m.selectedSessionName()
	if name == "" || m.sessionPreview == "" {
		return RenderPanel("Preview", MutedText.Render("(no preview)"), m.width, height, false)
	}

	innerW := m.width - 4
	maxLines := height - 3
	if maxLines < 1 {
		maxLines = 1
	}

	lines := strings.Split(m.sessionPreview, "\n")
	for i, line := range lines {
		if len(line) > innerW {
			lines[i] = line[:innerW-1] + "…"
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return RenderPanel(name, strings.Join(lines, "\n"), m.width, height, false)
}

// setGridCursor moves the selection and keeps SessionsModel.Cursor aligned, so
// the preview, `s` save, and the search overlay keep working off one selection.
func (m *Model) setGridCursor(targets []Target, idx int) {
	if idx < 0 || idx >= len(targets) {
		return
	}
	m.gridIndex = idx
	m.gridCursor = targets[idx].Label
	for i, s := range m.sessions.Sessions {
		if s.Name == targets[idx].Label {
			m.sessions.Cursor = i
			return
		}
	}
}

// enterTarget switches to a running session, or creates and switches to one for
// a dormant repo.
func (m *Model) enterTarget(targets []Target, idx int) tea.Cmd {
	if idx < 0 || idx >= len(targets) {
		return nil
	}
	t := targets[idx]

	if t.Host != "" {
		return m.enterRemote(t)
	}

	// A configured repo goes through the full jump even when its session is
	// already up, so processes that died or were never started come back.
	if _, configured := m.config.Repo(t.Label); configured {
		repo := m.repoForLabel(t.Label, "")
		return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpRepo(repo)} }
	}
	if t.Running() {
		name := t.Label
		return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxSwitch(name)} }
	}
	if t.Repo == nil {
		return nil
	}
	repo := m.repoForLabel(t.Label, t.Repo.Path)
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpRepo(repo)} }
}

// enterRemote jumps to a project on another host through a local view
// session. An unconfigured remote session — one someone started by hand on
// that machine — still gets a view window; it just has no processes to bring
// up.
func (m *Model) enterRemote(t Target) tea.Cmd {
	host, ok := m.config.Host(t.Host)
	if !ok {
		return nil
	}
	repo, configured := m.config.RepoOn(t.Host, t.Label)
	if !configured {
		if !t.Running() {
			return nil
		}
		repo = config.RepoConfig{Host: t.Host, Label: t.Label}
	}
	return func() tea.Msg {
		local := sources.DefaultRunner()
		remote := sources.SSHRunner{Host: host.Name, Tmux: host.Tmux}
		err := sources.JumpRemote(context.Background(), local, remote, host, repo)
		return tmuxSwitchResultMsg{Err: err}
	}
}

// handleGridKey is the grid view's key surface. It is deliberately narrower than
// the dashboard's: panel-scoped keys have no meaning when no panels are shown.
func (m *Model) handleGridKey(msg tea.KeyMsg) tea.Cmd {
	targets := m.gridTargets()
	cols := GridCols(m.gridContentWidth())
	idx := resolveGridCursor(targets, m.gridCursor, m.gridIndex)

	move := func(dx, dy int) tea.Cmd {
		m.setGridCursor(targets, MoveGridCursor(idx, len(targets), cols, dx, dy))
		return m.fetchPreview()
	}

	switch msg.String() {
	case "h", "left":
		return move(-1, 0)
	case "l", "right":
		return move(1, 0)
	case "k", "up":
		return move(0, -1)
	case "j", "down":
		return move(0, 1)
	case "enter":
		return m.enterTarget(targets, idx)
	case "d":
		m.view = ViewDashboard
		return nil
	case "n":
		m.mode = ModeNewSession
		m.newSessionStep = 0
		m.newSessionPath = ""
		m.newSessionErr = ""
		m.newSessionInput.SetValue("")
		m.newSessionInput.Placeholder = "~/workspace/my-project"
		m.newSessionInput.Focus()
		return nil
	case "s":
		if idx < len(targets) && targets[idx].Running() {
			return m.saveSessionAsRepo()
		}
		return nil
	case "/":
		m.mode = ModeSearch
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.updateSearchResults()
		return nil
	case "r":
		return tea.Batch(m.fetchTmux(), m.fetchGit(), m.fetchGitHub())
	case "q":
		return tea.Quit
	}
	return nil
}
