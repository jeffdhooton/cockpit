package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// InboxModel manages the inbox panel.
type InboxModel struct {
	Items        []sources.Task
	Cursor       int
	Loading      bool
	ScrollOffset int
	FilePath     string
}

func NewInboxModel() InboxModel {
	return InboxModel{
		Loading: true,
	}
}

func (m *InboxModel) CursorUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *InboxModel) CursorDown() {
	if m.Cursor < len(m.Items)-1 {
		m.Cursor++
	}
}

func (m InboxModel) View(width, height int, focused bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading inbox...")
	}

	if len(m.Items) == 0 {
		return MutedText.Render("No notes. Add items to ") +
			AccentText.Render(m.FilePath)
	}

	// Two-column layout: split items into left and right columns
	innerW := width - 4 // borders + minimal padding
	colW := innerW / 2
	if colW < 15 {
		colW = innerW // fall back to single column if too narrow
	}

	visibleRows := height - 4
	if visibleRows < 1 {
		visibleRows = 1
	}

	twoCol := colW < innerW // true if we have room for two columns

	if twoCol {
		// Each column shows visibleRows items; total capacity = 2 * visibleRows
		totalVisible := visibleRows * 2
		_ = totalVisible

		// Scroll: items flow left-col top-to-bottom, then right-col top-to-bottom
		// Determine scroll window so cursor is visible
		start := m.ScrollOffset
		if m.Cursor < start {
			start = m.Cursor
		}
		capacity := visibleRows * 2
		if m.Cursor >= start+capacity {
			start = m.Cursor - capacity + 1
		}
		if start < 0 {
			start = 0
		}
		m.ScrollOffset = start

		end := start + capacity
		if end > len(m.Items) {
			end = len(m.Items)
		}

		visible := m.Items[start:end]

		// Split into left and right columns
		half := visibleRows
		if half > len(visible) {
			half = len(visible)
		}
		leftItems := visible[:half]
		var rightItems []sources.Task
		if half < len(visible) {
			rightItems = visible[half:]
		}

		renderCol := func(items []sources.Task, startIdx int) string {
			var lines []string
			for i, item := range items {
				globalIdx := start + startIdx + i
				prefix := "  "
				if globalIdx == m.Cursor && focused {
					prefix = AccentText.Render("◂ ")
				}
				text := item.Text
				maxText := colW - 6 // prefix(2) + checkbox(4)
				if maxText > 0 && len(text) > maxText {
					text = text[:maxText-1] + "…"
				}
				lines = append(lines, prefix+"[ ] "+text)
			}
			// Pad to visibleRows so columns align
			for len(lines) < visibleRows {
				lines = append(lines, "")
			}
			return strings.Join(lines, "\n")
		}

		leftCol := renderCol(leftItems, 0)
		rightCol := renderCol(rightItems, half)

		leftStyle := lipgloss.NewStyle().Width(colW)
		rightStyle := lipgloss.NewStyle().Width(colW)

		return lipgloss.JoinHorizontal(lipgloss.Top,
			leftStyle.Render(leftCol),
			rightStyle.Render(rightCol))
	}

	// Single column fallback
	start := m.ScrollOffset
	if m.Cursor < start {
		start = m.Cursor
	}
	if m.Cursor >= start+visibleRows {
		start = m.Cursor - visibleRows + 1
	}
	if start < 0 {
		start = 0
	}
	m.ScrollOffset = start

	end := start + visibleRows
	if end > len(m.Items) {
		end = len(m.Items)
	}

	var lines []string
	for i := start; i < end; i++ {
		item := m.Items[i]
		prefix := "  "
		if i == m.Cursor && focused {
			prefix = AccentText.Render("◂ ")
		}
		lines = append(lines, prefix+"[ ] "+item.Text)
	}

	return strings.Join(lines, "\n")
}
