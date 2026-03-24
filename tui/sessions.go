package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// SessionsModel manages the sessions panel.
type SessionsModel struct {
	Sessions []sources.TmuxSession
	Cursor   int
	Loading  bool
}

func NewSessionsModel() SessionsModel {
	return SessionsModel{Loading: true}
}

func (m *SessionsModel) CursorUp() {
	if m.Cursor > 0 {
		m.Cursor--
	}
}

func (m *SessionsModel) CursorDown() {
	if m.Cursor < len(m.Sessions)-1 {
		m.Cursor++
	}
}

func (m SessionsModel) View(width, height int, focused bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading sessions...")
	}
	if len(m.Sessions) == 0 {
		return MutedText.Render("No tmux sessions running. Start one: ") +
			AccentText.Render("tmux new -s <name>")
	}

	// Render session cards horizontally
	var cards []string
	for i, s := range m.Sessions {
		card := m.renderCard(s, i == m.Cursor && focused)
		cards = append(cards, card)
	}

	// Join cards horizontally with gap
	row := lipgloss.JoinHorizontal(lipgloss.Top, cards...)

	// If too wide, just truncate visually — lipgloss handles this
	return row
}

func (m SessionsModel) renderCard(s sources.TmuxSession, selected bool) string {
	nameStyle := BoldText
	if selected {
		nameStyle = nameStyle.Foreground(ColorAccent)
	}

	statusText := MutedText.Render("detached")
	if s.Attached {
		statusText = SuccessText.Render("attached")
	}

	idle := formatIdleTime(s.LastUsed)
	info := fmt.Sprintf("%dw %s", s.Windows, MutedText.Render(idle))

	border := lipgloss.RoundedBorder()
	borderColor := ColorBorder
	if selected {
		borderColor = ColorAccent
	}

	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Padding(0, 1).
		MarginRight(1)

	content := nameStyle.Render(s.Name) + "\n" +
		statusText + " " + info

	return style.Render(content)
}

func formatIdleTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (m SessionsModel) CompactView(width int, focused bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading...")
	}
	if len(m.Sessions) == 0 {
		return MutedText.Render("No sessions")
	}

	var lines []string
	for i, s := range m.Sessions {
		nameStyle := lipgloss.NewStyle().Foreground(ColorFg)
		cursor := "  "
		if i == m.Cursor && focused {
			nameStyle = nameStyle.Foreground(ColorAccent)
			cursor = AccentText.Render("◂ ")
		}

		status := MutedText.Render("detached")
		if s.Attached {
			status = SuccessText.Render("attached")
		}

		line := fmt.Sprintf("%s%s [%s] %dw",
			cursor, nameStyle.Render(s.Name), status, s.Windows)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
