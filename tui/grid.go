package tui

import (
	"fmt"
	"sort"
	"strings"

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
