package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// Target is one tile in the grid: a running tmux session, a saved repo with no
// session, or both joined on session.Name == repo.Label — the same identity
// tmuxJump already assumes when it switches to a session named for a repo.
type Target struct {
	Label   string
	Session *sources.TmuxSession
	Repo    *sources.GitRepoStatus
	Status  sources.ClaudeStatus
}

// Running reports whether the target has a live tmux session behind it.
func (t Target) Running() bool { return t.Session != nil }

// BuildTargets joins sessions and repos into one ordered tile list. Running
// targets come first, then dormant, alphabetical within each group. Ordering is
// deliberately not last-used: on a 5-second refresh that would move the tile
// under the cursor while the user is aiming at it.
func BuildTargets(
	sessions []sources.TmuxSession,
	repos []sources.GitRepoStatus,
	statuses map[string]sources.ClaudeStatus,
	selfSession string,
) []Target {
	repoByLabel := make(map[string]*sources.GitRepoStatus, len(repos))
	for i := range repos {
		repoByLabel[repos[i].Label] = &repos[i]
	}

	var running, dormant []Target
	live := make(map[string]bool, len(sessions))

	for i := range sessions {
		s := &sessions[i]
		if s.Name == selfSession {
			continue
		}
		live[s.Name] = true
		running = append(running, Target{
			Label:   s.Name,
			Session: s,
			Repo:    repoByLabel[s.Name],
			Status:  statuses[s.Name],
		})
	}

	for i := range repos {
		r := &repos[i]
		if live[r.Label] {
			continue
		}
		dormant = append(dormant, Target{Label: r.Label, Repo: r})
	}

	sort.Slice(running, func(i, j int) bool { return running[i].Label < running[j].Label })
	sort.Slice(dormant, func(i, j int) bool { return dormant[i].Label < dormant[j].Label })

	return append(running, dormant...)
}

const (
	// gridCellWidth is one tile's total footprint: 18 content + 2 border + 2 gap.
	gridCellWidth = 22
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
	name := nameStyle.Render(Truncate(t.Label, inner))

	status := StatusDot("no session", VariantMuted)
	if t.Running() {
		status = StatusDot("detached", VariantMuted)
		if t.Session.Attached {
			status = StatusDot("attached", VariantAccent)
		}
		switch t.Status {
		case sources.ClaudeStatusIdle:
			label := "idle"
			if age := formatIdleTime(t.Session.LastUsed); age != "" {
				label = "idle " + age
			}
			status = StatusDot(Truncate(label, inner-2), VariantMuted)
		case sources.ClaudeStatusWorking:
			status = StatusDot("working", VariantAccent)
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

	borderColor := ColorBorder
	if selected {
		borderColor = ColorAccent
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(inner).
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

// gridTargets builds the current tile list from live sessions and configured repos.
func (m Model) gridTargets() []Target {
	return BuildTargets(m.sessions.Sessions, m.repos.Repos, m.sessions.Statuses, m.config.General.SessionName)
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

	// Panel chrome eats 2 border rows + 1 title row, and 2 border + 2 padding cells.
	grid := RenderGrid(targets, cursor, m.width-4, gridH-3)
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
	if t.Running() {
		name := t.Label
		return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxSwitch(name)} }
	}
	if t.Repo == nil {
		return nil
	}
	label, path := t.Label, t.Repo.Path
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJump(label, path)} }
}

// handleGridKey is the grid view's key surface. It is deliberately narrower than
// the dashboard's: panel-scoped keys have no meaning when no panels are shown.
func (m *Model) handleGridKey(msg tea.KeyMsg) tea.Cmd {
	targets := m.gridTargets()
	cols := GridCols(m.width)
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
