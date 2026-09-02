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
	Statuses   map[string]sources.AgentStatus // session name → status
	Reported   map[string]bool                // session name → status came from a hook
	prevHashes map[string]string              // session name → previous content hash
}

// AdoptReported copies each session's hook-reported status into Statuses,
// so every reader of that map sees the report without knowing where it came
// from, and records which names are reported so the display can mark the
// rest as guessed.
//
// A session that stopped reporting — a crashed agent going stale in tmux —
// drops back to inferred here rather than freezing on its last report.
func (m *SessionsModel) AdoptReported() {
	if m.Statuses == nil {
		m.Statuses = make(map[string]sources.AgentStatus)
	}
	m.Reported = make(map[string]bool, len(m.Sessions))
	for _, s := range m.Sessions {
		if !s.StatusReported {
			continue
		}
		m.Statuses[s.Key()] = s.Status
		m.Reported[s.Key()] = true
	}
}

// NeedingCapture returns the sessions whose status still has to be guessed
// from pane content. A session that reports needs no capture, and the
// capture is the most expensive poll cockpit runs.
func (m SessionsModel) NeedingCapture() []sources.TmuxSession {
	var out []sources.TmuxSession
	for _, s := range m.Sessions {
		// The capture is a local exec, so a remote session can only ever be
		// reported, never guessed.
		if s.Host != "" || m.Reported[s.Key()] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// UpdateStatus compares current pane content against the previous snapshot.
// If the content changed, the session is working. If unchanged, it's idle.
func (m *SessionsModel) UpdateStatus(name, content string) {
	// A capture already in flight when a report landed must not clobber it.
	if m.Reported[name] {
		return
	}
	if m.Statuses == nil {
		m.Statuses = make(map[string]sources.AgentStatus)
	}
	if m.prevHashes == nil {
		m.prevHashes = make(map[string]string)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	prev, seen := m.prevHashes[name]
	m.prevHashes[name] = hash

	if !seen {
		// First poll — can't determine yet
		m.Statuses[name] = sources.AgentStatusUnknown
		return
	}

	if hash == prev {
		m.Statuses[name] = sources.AgentStatusIdle
	} else {
		m.Statuses[name] = sources.AgentStatusWorking
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

	// Status indicator: reported when available, hashed otherwise.
	statusText := StatusDot("Detached", VariantMuted)
	if s.Attached {
		statusText = StatusDot("Attached", VariantAccent)
	}
	if st, ok := m.Statuses[s.Name]; ok {
		switch st {
		case sources.AgentStatusIdle:
			statusText = StatusDot("Idle", VariantMuted)
		case sources.AgentStatusWorking:
			statusText = StatusDot("Working", VariantAccent)
		}
	}

	idle := formatIdleTime(s.LastUsed)
	info := MutedText.Render(fmt.Sprintf("%dw", s.Windows))
	if idle != "" {
		info += MutedText.Render(" · " + idle)
	}

	// Chrome stays subtle — only the selected card lifts to the accent border.
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
		selected := i == m.Cursor && focused
		nameStyle := lipgloss.NewStyle().Foreground(ColorFg)
		if selected {
			nameStyle = nameStyle.Foreground(ColorAccent)
		}

		status := StatusDot("Detached", VariantMuted)
		if s.Attached {
			status = StatusDot("Attached", VariantAccent)
		}
		if st, ok := m.Statuses[s.Name]; ok {
			switch st {
			case sources.AgentStatusIdle:
				status = StatusDot("Idle", VariantMuted)
			case sources.AgentStatusWorking:
				status = StatusDot("Working", VariantAccent)
			}
		}

		line := RowCursor(selected) + nameStyle.Render(s.Name) + "  " +
			status + MutedText.Render(fmt.Sprintf("  %dw", s.Windows))
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
