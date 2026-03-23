package tui

import (
	"strings"

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

	var lines []string

	if len(m.Items) == 0 {
		lines = append(lines, MutedText.Render("No notes. Add items to ")+
			AccentText.Render(m.FilePath))
	} else {
		visibleRows := height - 4 // leave room for input
		if visibleRows < 1 {
			visibleRows = 1
		}

		// Scroll around cursor
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

		for i := start; i < end; i++ {
			item := m.Items[i]
			checkbox := "[ ] "
			if item.Done {
				checkbox = SuccessText.Render("[x] ")
			}
			prefix := "  "
			if i == m.Cursor && focused {
				prefix = AccentText.Render("◂ ")
			}
			lines = append(lines, prefix+checkbox+item.Text)
		}
	}

	return strings.Join(lines, "\n")
}
