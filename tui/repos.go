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
		return MutedText.Render("No repos configured. Add repos in ") +
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

	visibleRows := height - 3 // header + borders
	if visibleRows < 1 {
		visibleRows = 1
	}

	m.AdjustScroll(visibleRows)

	var lines []string
	lines = append(lines, header)

	// Scrolling
	start := m.ScrollOffset
	end := start + visibleRows
	if end > len(m.Repos) {
		end = len(m.Repos)
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

	// Scroll indicators
	if start > 0 {
		lines = append([]string{lines[0], MutedText.Render("  ▲ more above")}, lines[1:]...)
	}
	if end < len(m.Repos) {
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
