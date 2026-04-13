package tui

import (
	"fmt"
	"strings"

	"github.com/jhoot/cockpit/sources"
)

// ReposModel manages the repos panel.
type ReposModel struct {
	Repos    []sources.GitRepoStatus
	Cursor   int
	Loading  bool
	ScrollOffset int
}

func NewReposModel() ReposModel {
	return ReposModel{Loading: true}
}

func (m *ReposModel) CursorUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *ReposModel) CursorDown() {
	if m.Cursor < len(m.Repos)-1 {
		m.Cursor++
	}
}

func (m *ReposModel) AdjustScroll(visibleRows int) {
	if m.Cursor < m.ScrollOffset {
		m.ScrollOffset = m.Cursor
	}
	if m.Cursor >= m.ScrollOffset+visibleRows {
		m.ScrollOffset = m.Cursor - visibleRows + 1
	}
}

func (m *ReposModel) View(width, height int, focused bool, showLastCommit bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading repos...")
	}
	if len(m.Repos) == 0 {
		return MutedText.Render("No projects configured. Add repos in ") +
			AccentText.Render("~/.config/cockpit/config.toml")
	}

	innerWidth := width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	// Column widths
	labelW := 16
	branchW := 14
	statusW := 6
	unpushedW := 4
	commitW := innerWidth - labelW - branchW - statusW - unpushedW - 4 // gaps
	if commitW < 0 || !showLastCommit {
		commitW = 0
	}

	// Header
	header := MutedText.Bold(true).Render(
		padRight("PROJECT", labelW) + " " +
			padRight("BRANCH", branchW) + " " +
			padRight("STATUS", statusW) + " " +
			padRight("↑", unpushedW))
	if commitW > 0 {
		header += " " + MutedText.Bold(true).Render(padRight("LAST COMMIT", commitW))
	}

	// Available content lines: height - 2 (borders) - 1 (panel title) - 1 (header)
	bodyRows := height - 4
	if bodyRows < 1 {
		bodyRows = 1
	}

	// Converge on visibleRows accounting for the "more above"/"more below"
	// indicators, each of which eats a row. AdjustScroll must be called with
	// the final row count or the cursor can end up clipped off-screen.
	visibleRows := bodyRows
	hasAbove := false
	hasBelow := false
	for i := 0; i < 3; i++ {
		m.AdjustScroll(visibleRows)
		end := m.ScrollOffset + visibleRows
		if end > len(m.Repos) {
			end = len(m.Repos)
		}
		newAbove := m.ScrollOffset > 0
		newBelow := end < len(m.Repos)
		newVisible := bodyRows
		if newAbove {
			newVisible--
		}
		if newBelow {
			newVisible--
		}
		if newVisible < 1 {
			newVisible = 1
		}
		hasAbove = newAbove
		hasBelow = newBelow
		if newVisible == visibleRows {
			break
		}
		visibleRows = newVisible
	}

	start := m.ScrollOffset
	end := start + visibleRows
	if end > len(m.Repos) {
		end = len(m.Repos)
	}
	hasAbove = start > 0
	hasBelow = end < len(m.Repos)

	var lines []string
	lines = append(lines, header)

	if hasAbove {
		lines = append(lines, MutedText.Render("  ▲ more above"))
	}

	for i := start; i < end; i++ {
		r := m.Repos[i]
		selected := i == m.Cursor && focused

		cursor := "  "
		if selected {
			cursor = AccentText.Render("◂ ")
		}

		label := Truncate(r.Label, labelW)
		branch := PurpleText.Render(Truncate(r.Branch, branchW))

		var status string
		if r.Error != nil {
			status = WarningText.Render("⚠ err")
		} else if r.Dirty {
			status = StatusDirty.Render(fmt.Sprintf("✗%d", r.DirtyCount))
		} else {
			status = StatusClean.Render("✓")
		}

		unpushed := ""
		if r.Unpushed > 0 {
			unpushed = StatusUnpushed.Render(fmt.Sprintf("↑%d", r.Unpushed))
		}

		line := cursor + padRight(label, labelW) + " " +
			padRight(branch, branchW) + " " +
			padRight(status, statusW) + " " +
			padRight(unpushed, unpushedW)

		if commitW > 0 {
			line += " " + MutedText.Render(Truncate(r.LastCommit, commitW))
		}

		if selected {
			line = SelectedRow.Render(line)
		}

		lines = append(lines, line)
	}

	if hasBelow {
		lines = append(lines, MutedText.Render("  ▼ more below"))
	}

	return strings.Join(lines, "\n")
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
