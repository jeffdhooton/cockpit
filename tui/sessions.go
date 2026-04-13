package tui

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// SessionsModel manages the sessions panel.
type SessionsModel struct {
	Sessions   []sources.TmuxSession
	Cursor     int
	Loading    bool
	Statuses   map[string]sources.ClaudeStatus // session name → detected status
	prevHashes map[string]string               // session name → previous content hash
}

// UpdateStatus compares current pane content against the previous snapshot.
// If the content changed, the session is working. If unchanged, it's idle.
func (m *SessionsModel) UpdateStatus(name, content string) {
	if m.Statuses == nil {
		m.Statuses = make(map[string]sources.ClaudeStatus)
	}
	if m.prevHashes == nil {
		m.prevHashes = make(map[string]string)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	prev, seen := m.prevHashes[name]
	m.prevHashes[name] = hash

	if !seen {
		// First poll — can't determine yet
		m.Statuses[name] = sources.ClaudeStatusUnknown
		return
	}

	if hash == prev {
		m.Statuses[name] = sources.ClaudeStatusIdle
	} else {
		m.Statuses[name] = sources.ClaudeStatusWorking
	}
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

	// Claude Code status indicator (from content-hash diffing)
	statusText := MutedText.Render("detached")
	if s.Attached {
		statusText = SuccessText.Render("attached")
	}
	if st, ok := m.Statuses[s.Name]; ok {
		switch st {
		case sources.ClaudeStatusIdle:
			statusText = ErrorText.Render("● idle")
		case sources.ClaudeStatusWorking:
			statusText = SuccessText.Render("● working")
		}
	}

	idle := formatIdleTime(s.LastUsed)
	info := fmt.Sprintf("%dw %s", s.Windows, MutedText.Render(idle))

	border := lipgloss.RoundedBorder()
	borderColor := ColorBorder
	if st, ok := m.Statuses[s.Name]; ok {
		switch st {
		case sources.ClaudeStatusIdle:
			borderColor = ColorError
		case sources.ClaudeStatusWorking:
			borderColor = ColorSuccess
		}
	}
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
		if st, ok := m.Statuses[s.Name]; ok {
			switch st {
			case sources.ClaudeStatusIdle:
				status = ErrorText.Render("● idle")
			case sources.ClaudeStatusWorking:
				status = SuccessText.Render("● working")
			}
		}

		line := fmt.Sprintf("%s%s [%s] %dw",
			cursor, nameStyle.Render(s.Name), status, s.Windows)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
