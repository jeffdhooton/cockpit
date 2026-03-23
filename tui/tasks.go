package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/jhoot/cockpit/sources"
)

// TasksModel manages the today tasks panel.
type TasksModel struct {
	Tasks        []sources.Task
	Cursor       int
	Loading      bool
	ScrollOffset int
	TextInput    textinput.Model
	Capturing    bool
}

func NewTasksModel() TasksModel {
	ti := textinput.New()
	ti.Placeholder = "add a task..."
	ti.CharLimit = 256
	return TasksModel{Loading: true, TextInput: ti}
}

func (m *TasksModel) CursorUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *TasksModel) CursorDown() {
	if m.Cursor < len(m.Tasks)-1 {
		m.Cursor++
	}
}

func (m *TasksModel) AdjustScroll(visibleRows int) {
	if m.Cursor < m.ScrollOffset {
		m.ScrollOffset = m.Cursor
	}
	if m.Cursor >= m.ScrollOffset+visibleRows {
		m.ScrollOffset = m.Cursor - visibleRows + 1
	}
}

// FirstUnchecked returns the index of the first unchecked task, or 0.
func (m TasksModel) FirstUnchecked() int {
	for i, t := range m.Tasks {
		if !t.Done {
			return i
		}
	}
	return 0
}

func (m *TasksModel) View(width, height int, focused bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading tasks...")
	}

	// Reserve space for capture input
	inputRows := 0
	if m.Capturing || focused {
		inputRows = 2
	}

	if len(m.Tasks) == 0 && !m.Capturing {
		content := MutedText.Render("No tasks yet. Press ") +
			AccentText.Render("c") +
			MutedText.Render(" to add one.")
		if focused {
			content += "\n\n" + MutedText.Render("> _")
		}
		return content
	}

	visibleRows := height - 2 - inputRows
	if visibleRows < 1 {
		visibleRows = 1
	}

	m.AdjustScroll(visibleRows)

	start := m.ScrollOffset
	end := start + visibleRows
	if end > len(m.Tasks) {
		end = len(m.Tasks)
	}

	var lines []string
	for i := start; i < end; i++ {
		task := m.Tasks[i]
		selected := i == m.Cursor && focused

		cursor := "  "
		if selected {
			cursor = AccentText.Render("◂ ")
		}

		var checkbox, text string
		if task.Done {
			checkbox = SuccessText.Render("[x]")
			text = MutedText.Render(task.Text)
		} else {
			checkbox = "[ ]"
			text = task.Text
		}

		line := cursor + checkbox + " " + text
		if selected {
			line = SelectedRow.Render(line)
		}
		lines = append(lines, line)
	}

	// Scroll indicators
	if start > 0 {
		lines = append([]string{MutedText.Render("  ▲ more above")}, lines...)
	}
	if end < len(m.Tasks) {
		lines = append(lines, MutedText.Render("  ▼ more below"))
	}

	// Capture input line
	if m.Capturing {
		lines = append(lines, "")
		lines = append(lines, "> "+m.TextInput.View())
	} else if focused {
		lines = append(lines, "")
		lines = append(lines, MutedText.Render("> _"))
	}

	return strings.Join(lines, "\n")
}
