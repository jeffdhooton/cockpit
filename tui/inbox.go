package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/jhoot/cockpit/sources"
)

// InboxModel manages the inbox panel.
type InboxModel struct {
	Items        []sources.Task
	Cursor       int
	Loading      bool
	TextInput    textinput.Model
	Capturing    bool
	ScrollOffset int
}

func NewInboxModel() InboxModel {
	ti := textinput.New()
	ti.Placeholder = "capture a thought..."
	ti.CharLimit = 256
	return InboxModel{
		Loading:   true,
		TextInput: ti,
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

	if len(m.Items) == 0 && !m.Capturing {
		lines = append(lines, MutedText.Render("Inbox empty — press ")+
			AccentText.Render("c")+
			MutedText.Render(" to capture a thought"))
	} else {
		visibleRows := height - 4 // leave room for input
		if visibleRows < 1 {
			visibleRows = 1
		}
		end := len(m.Items)
		start := 0
		if end > visibleRows {
			start = end - visibleRows
		}
		for i := start; i < end; i++ {
			item := m.Items[i]
			prefix := "  "
			if i == m.Cursor && focused && !m.Capturing {
				prefix = AccentText.Render("◂ ")
			}
			lines = append(lines, prefix+"[ ] "+item.Text)
		}
	}

	// Input line
	if m.Capturing {
		lines = append(lines, "")
		lines = append(lines, "> "+m.TextInput.View())
	} else if focused {
		lines = append(lines, "")
		lines = append(lines, MutedText.Render("> _"))
	}

	return strings.Join(lines, "\n")
}
