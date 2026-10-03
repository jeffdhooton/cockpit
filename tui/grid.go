package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
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
	// Hotkey is the digit that jumps straight to this target, 1-10, or 0 when
	// it has none. Only targets with a live session get one: a digit is a
	// shortcut into something already open, not a way to start something.
	Hotkey int
	// Hermes is set on the tile for a Hermes gateway. It has no repo. With a
	// Host, Enter opens a shell on that machine and the shell's tmux session
	// is folded into this tile; without one the tile is read-only.
	Hermes *sources.HermesStatus
	// HostBox marks the tile that stands for a whole machine. At the root
	// every target on that host collapses into it, and Enter opens the host's
	// own grid rather than switching to anything.
	HostBox bool
	// Polled is true once a host box's machine has answered at least one
	// poll. Until then the box says nothing about reachability rather than
	// claiming a machine it has never spoken to is up.
	Polled bool
	// Display overrides the name on the tile. Inside a host's own grid every
	// tile would otherwise repeat the host the panel title already names, and
	// on a phone the prefix costs the cells the label needs.
	Display string
	// Spine is set on the one tile for the spine fleet. It summarises
	// `spine bearings --json`; Enter switches to the spine session, and a
	// local session of that name is folded into it rather than drawn twice.
	Spine *sources.SpineStatus
}

// Running reports whether the target has a live tmux session behind it.
func (t Target) Running() bool { return t.Session != nil }

// Key identifies the target across hosts: host/label remotely, label locally.
// A Hermes tile is keyed by its label alone: it is named for the gateway, not
// for the machine it happens to run on. A host box is keyed by the machine's
// own name, which "mini/mini" would only repeat.
func (t Target) Key() string {
	if t.Host == "" || t.Hermes != nil || t.HostBox {
		return t.Label
	}
	return t.Host + "/" + t.Label
}

// Name is what the tile shows: the key, so a remote tile names its host, or
// Display where the grid has overridden it.
func (t Target) Name() string {
	if t.Display != "" {
		return t.Display
	}
	return t.Key()
}

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

// hotkeyMax is how many targets can carry a digit: the ten keys 1-9 and 0.
const hotkeyMax = 10

// AssignHotkeys numbers the first ten running targets in grid order, so the
// digit on a tile is the digit that jumps to it. Dormant repos are skipped —
// a digit takes you to a session that already exists.
func AssignHotkeys(targets []Target) []Target {
	next := 1
	for i := range targets {
		targets[i].Hotkey = 0
		if next > hotkeyMax || !targets[i].Running() {
			continue
		}
		targets[i].Hotkey = next
		next++
	}
	return targets
}

// hotkeyLabel renders a hotkey as the key you press: 1-9, then 0 for the tenth.
// An unassigned target renders as nothing.
func hotkeyLabel(n int) string {
	switch {
	case n < 1 || n > hotkeyMax:
		return ""
	case n == hotkeyMax:
		return "0"
	default:
		return strconv.Itoa(n)
	}
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
	hermes ...sources.HermesStatus,
) []Target {
	repoByKey := make(map[string]*sources.GitRepoStatus, len(repos))
	for i := range repos {
		repoByKey[repos[i].Key()] = &repos[i]
	}

	// A Hermes tile with a host owns the remote session Enter creates for its
	// shell, so that session is folded into the tile instead of becoming a
	// second one. Keyed by host/label, so a local session of the same name
	// stays a separate target.
	hermesByKey := make(map[string]*Target, len(hermes))
	hermesTargets := make([]Target, len(hermes))
	for i := range hermes {
		hermesTargets[i] = Target{Label: hermes[i].Label, Host: hermes[i].Host, Hermes: &hermes[i]}
		if hermes[i].Host != "" {
			hermesByKey[hermes[i].Host+"/"+hermes[i].Label] = &hermesTargets[i]
		}
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
		if h, ok := hermesByKey[s.Key()]; ok {
			h.Session = s
			h.Status = statuses[s.Key()]
			h.StatusReported = s.StatusReported
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

	// Hermes gateways sit after the live sessions and before the dormant
	// repos: they are running things, not projects waiting to be opened.
	out := append(running, hermesTargets...)
	return append(out, dormant...)
}

const (
	// gridCellWidth is one tile's total footprint: 14 content + 2 border + 2 padding.
	// GridCols is measured against the grid's content width, not the terminal's,
	// so this budget excludes the enclosing panel's own border and padding.
	gridCellWidth = 18
	// gridMaxCellWidth caps how wide one tile gets. Past this a tile is mostly
	// empty padding — a tile holds a label, a status and a branch, and none of
	// them grow. Extra terminal width therefore buys more columns rather than
	// wider ones: capping columns instead produced 83-column boxes holding
	// twenty-odd characters each on an ultrawide screen.
	gridMaxCellWidth = 28
	// gridTileH is a tile's total height: 3 content lines + 2 border rows.
	gridTileH = 5
	// gridCompactTileH is the mobile tile's height: 1 content line + 2 border
	// rows. Two thirds of the footprint buys half again as many tiles on the
	// one screen size where they are scarcest.
	gridCompactTileH = 3

	// MobileMaxWidth is the threshold below which the preview is dropped and the
	// grid takes the full screen.
	MobileMaxWidth = 70
	// MinTerminalWidth is the floor below which even one tile is illegible.
	MinTerminalWidth = 24
)

// GridCols returns the column count for a given terminal width: enough columns
// that no tile exceeds gridMaxCellWidth, but never so many that one falls below
// the legible floor. Rows still span the full width, so the right edge stays
// flush with the panel border.
func GridCols(width int) int {
	legible := width / gridCellWidth
	if legible < 1 {
		return 1
	}
	// Round up, so the widest tile lands at or under the cap.
	cols := (width + gridMaxCellWidth - 1) / gridMaxCellWidth
	if cols > legible {
		cols = legible
	}
	if cols < 1 {
		return 1
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

// cursorID is what the grid cursor remembers a tile by: its label, except
// the spine tile, whose label a configured repo named spine would share.
func (t Target) cursorID() string {
	if t.Spine != nil {
		return spineCursor
	}
	return t.Label
}

// resolveGridCursor turns the stored cursor label into an index. When the label
// is gone — session died, repo dropped from config — it clamps the previous
// index into range so the selection lands on a neighbour instead of jumping to
// the top.
func resolveGridCursor(targets []Target, label string, prev int) int {
	for i := range targets {
		if targets[i].cursorID() == label {
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

// tileMarker is the tile's state in a single cell: shape carries whether a
// session exists, colour carries what the agent is doing. It is what the
// compact tile shows in place of renderTile's labelled status line, which
// draws the same glyph with its name beside it.
func tileMarker(t Target) string {
	glyph, v := "●", VariantMuted
	switch {
	case t.Spine != nil:
		return spineMarker(t.Spine)
	case t.HostBox:
		switch {
		case !t.Polled:
			glyph, v = "○", VariantMuted
		case t.Unreachable:
			glyph, v = "⚠", VariantWarning
		default:
			v = VariantAccent
		}
	case t.Hermes != nil:
		switch {
		case !t.Hermes.Reachable:
			glyph, v = "⚠", VariantWarning
		case t.Hermes.Gateway == "running":
			v = VariantAccent
		default:
			v = VariantWarning
		}
	case t.Unreachable:
		glyph, v = "⚠", VariantWarning
	case !t.Running():
		glyph, v = "○", VariantMuted
	case t.Status == sources.AgentStatusNeedsInput:
		v = VariantWarning
	case t.Status == sources.AgentStatusWorking:
		v = VariantAccent
	case t.Status == sources.AgentStatusIdle:
		v = VariantNeutral
	case t.Session.Attached:
		v = VariantAccent
	}
	return lipgloss.NewStyle().Foreground(variantColor(v)).Render(glyph)
}

// renderTile draws one target: label, status, and git state. Every piece is
// truncated to the inner width before styling, so the tile can never wrap and
// blow its 3-line content budget. A compact tile keeps only the marker and the
// name, on one line.
func renderTile(t Target, width int, selected, compact bool) string {
	inner := width - 4 // 2 border cells + 2 padding cells
	if inner < 6 {
		inner = 6
	}

	// The hotkey leads the tile so the eye pairs the digit with the name it
	// jumps to. Its two cells are spent whether or not the tile has a digit:
	// a column of digits only reads as a column if the tiles without one hold
	// the space open.
	key := "  "
	if lbl := hotkeyLabel(t.Hotkey); lbl != "" {
		key = MutedText.Render(lbl) + " "
	}
	nameW := inner - 2

	nameStyle := BoldText
	switch {
	case selected:
		nameStyle = BoldText.Foreground(ColorAccent)
	case !t.Running() && t.Spine == nil:
		nameStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	}

	borderColor := ColorBorder
	if selected {
		borderColor = ColorAccent
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		// lipgloss Width counts padding but not border; inner is the text budget.
		Width(inner + 2)

	// On a phone the name is the whole point, so it gets the line and the
	// marker gets the cell beside it. Branch, dirty counts and the process
	// indicator do not survive the trip.
	if compact {
		if t.Spine != nil {
			// The needs-you count is the one number worth a phone's line.
			return box.Height(1).MaxHeight(gridCompactTileH).
				Render(key + tileMarker(t) + " " + nameStyle.Render(Truncate(t.Name(), nameW-4)) + " " + spineCompactCount(t.Spine))
		}
		return box.Height(1).MaxHeight(gridCompactTileH).
			Render(key + tileMarker(t) + " " + nameStyle.Render(Truncate(t.Name(), nameW-2)))
	}

	name := key + nameStyle.Render(Truncate(t.Name(), nameW))
	if t.Spine != nil {
		status, detail := spineTileLines(t.Spine, inner)
		return box.Height(3).MaxHeight(gridTileH).Render(name + "\n" + status + "\n" + detail)
	}

	// Shape carries session existence: a hollow ring means there is nothing to
	// attach to, while every live session keeps the filled status dot.
	status := StatusRing("no session", VariantMuted)
	if t.HostBox {
		status = hostBoxStatusLine(t, inner)
	} else if t.Hermes != nil {
		status = hermesStatusLine(t.Hermes, inner)
	} else if t.Unreachable {
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
	if t.Hermes != nil && len(t.Hermes.Platforms) > 0 {
		// The git line carries the connected platforms instead.
		git = MutedText.Render(Truncate(strings.Join(t.Hermes.Platforms, " "), inner))
	} else if t.Repo != nil {
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

	return box.Height(3).MaxHeight(gridTileH).Render(name + "\n" + status + "\n" + git)
}

// tileHeight is a tile's row footprint for the current layout.
func tileHeight(compact bool) int {
	if compact {
		return gridCompactTileH
	}
	return gridTileH
}

// RenderGrid lays targets out in a responsive grid, scrolling by row to keep the
// cursor visible and appending a muted count when tiles are clipped. Compact
// draws the shorter mobile tile, which fits half again as many rows.
func RenderGrid(targets []Target, cursor, width, height int, compact bool) string {
	if len(targets) == 0 {
		return MutedText.Render("No sessions or repos. Add repos in ") +
			AccentText.Render("~/.config/cockpit/config.toml")
	}

	tileH := tileHeight(compact)
	cols := GridCols(width)
	cellW := width / cols
	rows := (len(targets) + cols - 1) / cols

	visibleRows := height / tileH
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
				cells = append(cells, lipgloss.NewStyle().Width(cellW).Height(tileH).Render(""))
				continue
			}
			cells = append(cells, renderTile(targets[i], cellW, i == cursor, compact))
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

	var page string
	switch m.view {
	case ViewGrid:
		page = m.gridView()
	case ViewAttention:
		page = m.attentionView()
	case ViewProcesses:
		page = m.processesView()
	default:
		page = m.sessionsView()
	}

	switch m.mode {
	case ModeNewSession:
		page = m.overlay(m.renderNewSessionDialog())
	case ModeSearch:
		page = m.overlay(m.renderSearchDialog())
	case ModeConfirm:
		page = m.overlay(m.procs.confirmView(m.width))
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

// hostBoxes is one collapsed tile per configured machine, in config order.
// The tile carries the host's link state and nothing about its contents:
// what is running inside is one keypress away and speaks for itself there.
func (m Model) hostBoxes() []Target {
	out := make([]Target, 0, len(m.config.Hosts))
	for _, h := range m.config.Hosts {
		st, polled := m.hosts[h.Name]
		out = append(out, Target{
			Label:       h.Name,
			Host:        h.Name,
			HostBox:     true,
			Polled:      polled,
			Unreachable: st.unreachable,
		})
	}
	return out
}

// insertHostBoxes places the machine tiles after the live sessions and any
// gateway, and before the dormant repos — the same slot a gateway takes. A
// box holds running work; a dormant repo is a project waiting to be opened.
func insertHostBoxes(targets, boxes []Target) []Target {
	if len(boxes) == 0 {
		return targets
	}
	at := len(targets)
	for i := range targets {
		if !targets[i].Running() && targets[i].Hermes == nil && targets[i].Spine == nil {
			at = i
			break
		}
	}
	out := make([]Target, 0, len(targets)+len(boxes))
	out = append(out, targets[:at]...)
	out = append(out, boxes...)
	return append(out, targets[at:]...)
}

// insertSpine places the spine tile right after the live sessions, beside
// them and ahead of any gateway, host box or dormant repo.
func insertSpine(targets []Target, spine Target) []Target {
	at := len(targets)
	for i := range targets {
		if !targets[i].Running() {
			at = i
			break
		}
	}
	out := make([]Target, 0, len(targets)+1)
	out = append(out, targets[:at]...)
	out = append(out, spine)
	return append(out, targets[at:]...)
}

// gridTargets builds the tile list for the level the grid is on. At the root
// that is this machine's sessions and repos, plus one collapsed box per
// configured host; inside a host it is that host's targets alone. Everything
// below is written against the scoped inputs, so one code path draws both
// levels and a host's grid reads exactly like the root's.
func (m Model) gridTargets() []Target {
	var sessions []sources.TmuxSession
	var repos []sources.GitRepoStatus
	if m.gridHost == "" {
		// The spine session belongs to the spine tile, not a tile of its own.
		for _, s := range m.sessions.Sessions {
			if s.Name != spineSession {
				sessions = append(sessions, s)
			}
		}
		repos = append(repos, m.repos.Repos...)
	} else if _, declared := m.config.Host(m.gridHost); declared {
		// An undeclared host draws an empty grid rather than its last-known
		// tiles: it is a machine cockpit no longer knows how to reach, and
		// backspace is still there to get out of it.
		sessions = append(sessions, m.hosts[m.gridHost].poll.Sessions...)
		repos = append(repos, m.reposOn(m.gridHost)...)
	}

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

	// A gateway shows up on the level its machine is on: bound to a host it
	// lives inside that host's box, and hostless it has no box to live in and
	// stays at the root.
	var hermes []sources.HermesStatus
	for _, h := range m.config.Hermes {
		if h.Host != m.gridHost {
			continue
		}
		st, polled := m.hermes[h.Label]
		if !polled {
			st = sources.HermesStatus{Label: h.Label}
		}
		st.Host = h.Host
		hermes = append(hermes, st)
	}

	targets := BuildTargets(sessions, repos, statuses, m.config.General.SessionName, hermes...)
	if m.gridHost == "" {
		targets = insertSpine(targets, m.spineTarget())
		targets = insertHostBoxes(targets, m.hostBoxes())
	}
	for i := range targets {
		if targets[i].Host == "" {
			continue
		}
		targets[i].Unreachable = m.hosts[targets[i].Host].unreachable
		if m.gridHost != "" {
			// Every tile here is on the host the title names, so the prefix
			// on each one says nothing the eye has not already read.
			targets[i].Display = targets[i].Label
		}
	}

	processes := make(map[string][]sources.ProcessInfo, len(m.processes))
	for k, v := range m.processes {
		processes[k] = v
	}
	for k, v := range m.remoteProcesses() {
		processes[k] = v
	}
	// Hotkeys are assigned here, once, so the digit drawn on a tile and the
	// digit handleGridKey resolves are read off the same list.
	return AssignHotkeys(AttachProcesses(targets, processes))
}

// gridView renders the unified grid, plus the session preview on desktop widths.
func (m Model) gridView() string {
	targets := m.gridTargets()
	cursor := resolveGridCursor(targets, m.gridCursor, m.gridIndex)

	hints := GridKeyhintsView(m.width, m.gridHost != "", m.attn.badge())
	switch {
	case m.mode == ModeCapture:
		hints = "  " + AccentText.Render("capture ›") + " " + m.captureInput.View()
	case m.transientErr != "":
		hints = WarningText.Render(m.transientErr)
	}

	// A phone gets the compact tile: no preview to compete with, and the rows
	// it saves are the rows it has fewest of.
	compact := m.width < MobileMaxWidth
	tileH := tileHeight(compact)

	body := m.height - 1 // keyhints row
	if body < tileH {
		body = tileH
	}

	gridH := body
	showPreview := m.width >= MobileMaxWidth && len(targets) > 0
	if showPreview {
		gridH = body * 3 / 5
		if gridH < tileH+3 {
			gridH = tileH + 3
		}
	}

	// The title carries the level, so the grid never leaves you guessing
	// which machine's tiles you are looking at.
	title := "Cockpit"
	if m.gridHost != "" {
		title = m.gridHost
	}

	// Panel chrome eats 2 border rows + 1 title row on top of the content width.
	grid := RenderGrid(targets, cursor, m.gridContentWidth(), gridH-3, compact)
	page := RenderPanel(title, grid, m.width, gridH, true)

	if showPreview {
		page = lipgloss.JoinVertical(lipgloss.Left, page, m.renderPreviewPanel(body-gridH))
	}

	return lipgloss.JoinVertical(lipgloss.Left, page, hints)
}

// gridLocalSession is the tmux session on this machine under the grid cursor,
// or "" when the selection is remote, dormant, or a host box. Both the preview
// and `s` shell out to local tmux, so a selection this machine does not own has
// nothing for either of them: they must show and save nothing rather than fall
// back on whatever local session sat at the same index.
func (m Model) gridLocalSession() string {
	targets := m.gridTargets()
	idx := resolveGridCursor(targets, m.gridCursor, m.gridIndex)
	if idx < 0 || idx >= len(targets) {
		return ""
	}
	t := targets[idx]
	if t.Host != "" || t.HostBox || !t.Running() {
		return ""
	}
	return t.Label
}

// gridSelected is the target under the grid cursor.
func (m Model) gridSelected() (Target, bool) {
	targets := m.gridTargets()
	idx := resolveGridCursor(targets, m.gridCursor, m.gridIndex)
	if idx < 0 || idx >= len(targets) {
		return Target{}, false
	}
	return targets[idx], true
}

// renderPreviewPanel renders the capture-pane output for the selected session.
func (m Model) renderPreviewPanel(height int) string {
	if t, ok := m.gridSelected(); ok && t.Spine != nil {
		return RenderPanel("Spine fleet", spinePreview(t.Spine, m.now(), m.width-4, height-3), m.width, height, false)
	}
	name := m.gridLocalSession()
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
	m.gridCursor = targets[idx].cursorID()
	if targets[idx].Spine != nil {
		return
	}
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

	// A host box opens that machine's own grid. It is navigation, not a
	// jump: there is no session behind a box to switch to.
	if t.HostBox {
		m.openHost(t.Label)
		return nil
	}
	if t.Spine != nil {
		return m.openSpine()
	}

	// A Hermes tile opens a shell on its host. Without a host it is
	// read-only: there is nothing to attach to.
	if t.Hermes != nil {
		return m.enterHermes(t)
	}
	if t.Host != "" {
		return m.enterRemote(t)
	}

	// A configured repo goes through the full jump even when its session is
	// already up, so processes that died or were never started come back.
	svc := m.svc
	if _, configured := m.config.Repo(t.Label); configured {
		repo := m.repoForLabel(t.Label, "")
		return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpRepo(svc, repo)} }
	}
	if t.Running() {
		name := t.Label
		return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxSwitch(name)} }
	}
	if t.Repo == nil {
		return nil
	}
	repo := m.repoForLabel(t.Label, t.Repo.Path)
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpRepo(svc, repo)} }
}

// openHost descends into one machine's grid, remembering the box it came from
// so leaveHost can put the cursor back on it. The box is labelled for the host,
// so the host name is the label to return to. The cursor lands on tile one.
func (m *Model) openHost(host string) {
	m.gridRootCursor = host
	m.gridHost = host
	m.gridCursor = ""
	m.gridIndex = 0
	m.setGridCursor(m.gridTargets(), 0)
}

// leaveHost pops back to the root, onto the box that was open. When that box
// is gone — the host dropped from config while it was open — resolveGridCursor
// clamps the stale index, which is why the index is reset alongside the label.
func (m *Model) leaveHost() {
	m.gridHost = ""
	m.gridCursor = m.gridRootCursor
	m.gridIndex = 0
	m.gridRootCursor = ""
}

// enterHotkey jumps to the target carrying a digit, taking the selection with
// it so the tile you land on is the one left selected. An unbound digit does
// nothing: guessing at a neighbour would switch you into the wrong session.
func (m *Model) enterHotkey(targets []Target, key string) tea.Cmd {
	for i := range targets {
		if hotkeyLabel(targets[i].Hotkey) != key {
			continue
		}
		m.setGridCursor(targets, i)
		return m.enterTarget(targets, i)
	}
	return nil
}

// hermesStatusLine renders the gateway state: running in the accent colour,
// any other reachable state as a warning, and unreachable as a dead link.
func hermesStatusLine(h *sources.HermesStatus, inner int) string {
	switch {
	case !h.Reachable:
		return WarningText.Render("⚠ " + Truncate("unreachable", inner-2))
	case h.Gateway == "running":
		return StatusDot("gateway", VariantAccent)
	default:
		return StatusDot(Truncate(h.Gateway, inner-2), VariantWarning)
	}
}

// hostBoxStatusLine renders a machine's state on its collapsed tile: whether
// the link is up, and nothing about what is running inside it. The contents
// are one keypress away and speak for themselves; the box is about the box.
// Before the first poll it says neither, because it does not yet know.
func hostBoxStatusLine(t Target, inner int) string {
	switch {
	case !t.Polled:
		return StatusRing(Truncate("connecting", inner-2), VariantMuted)
	case t.Unreachable:
		return WarningText.Render("⚠ " + Truncate("unreachable", inner-2))
	default:
		return StatusDot(Truncate("reachable", inner-2), VariantAccent)
	}
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
	return m.jumpRemoteCmd(host, repo)
}

// enterHermes opens a shell on the gateway's host: a remote tmux session named
// for the tile, reached through the same view window a remote project uses.
// Hermes itself runs under launchd, not tmux, so there is no session of its
// own to attach to; this is the box, not the process.
func (m *Model) enterHermes(t Target) tea.Cmd {
	if t.Host == "" {
		return nil
	}
	host, ok := m.config.Host(t.Host)
	if !ok {
		return nil
	}
	return m.jumpRemoteCmd(host, hermesShellRepo(config.HermesConfig{Label: t.Label, Host: t.Host}))
}

// hermesShellRepo describes the shell session as a repo with no processes,
// so the remote jump can create and attach it. It starts in the remote home
// directory, which the remote shell expands.
func hermesShellRepo(h config.HermesConfig) config.RepoConfig {
	return config.RepoConfig{Label: h.Label, Host: h.Host, Path: "~"}
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
	case "backspace":
		// At the root there is nowhere above to go, so this is a no-op
		// rather than an exit: backspace should never quit anything.
		if m.gridHost != "" {
			m.leaveHost()
			return m.fetchPreview()
		}
		return nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
		return m.enterHotkey(targets, msg.String())
	case "d", "g":
		m.openSessions()
		return m.fetchPanePreview()
	case "c":
		return m.startCapture()
	case "a":
		return m.openAttention()
	case "p":
		if idx < 0 || idx >= len(targets) {
			return nil
		}
		t := targets[idx]
		if t.HostBox {
			m.transientErr = "Open a project to manage its processes."
			m.transientTimer = 3
			return tea.Tick(time.Second, func(time.Time) tea.Msg { return clearErrMsg{} })
		}
		if (t.Hermes != nil && !t.Running()) || t.Spine != nil {
			return nil
		}
		return m.openProcessesFor(t.Host, t.Label, "")
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
		// Only a session on this machine: the save reads its path out of the
		// local tmux server and writes a local [[repos]] entry.
		return m.saveSessionAsRepo(m.gridLocalSession())
	case "/":
		m.mode = ModeSearch
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.updateSearchResults()
		return nil
	case "r":
		return tea.Batch(m.fetchTmux(), m.fetchGit(), m.fetchGitHub(), m.fetchSpine())
	case "q":
		return tea.Quit
	}
	return nil
}
