package tui

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// SessionsModel manages the sessions panel: a merged, identity-preserving
// view of Build sessions (via the buildctl contract) and legacy tmux
// sessions on the default server.
type SessionsModel struct {
	Sessions   []MergedSession
	Cursor     int
	Loading    bool
	Statuses   map[string]sources.ClaudeStatus // MergedSession.Key() → detected status (legacy only)
	BuildNote  string                          // quiet Build availability indicator; empty when healthy
	prevHashes map[string]string               // MergedSession.Key() → previous content hash
}

// UpdateStatus compares current pane content against the previous snapshot.
// If the content changed, the session is working. If unchanged, it's idle.
// Only legacy sessions are polled this way — Build status comes from the
// contract, never from pane scraping.
func (m *SessionsModel) UpdateStatus(key, content string) {
	if m.Statuses == nil {
		m.Statuses = make(map[string]sources.ClaudeStatus)
	}
	if m.prevHashes == nil {
		m.prevHashes = make(map[string]string)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	prev, seen := m.prevHashes[key]
	m.prevHashes[key] = hash

	if !seen {
		// First poll — can't determine yet
		m.Statuses[key] = sources.ClaudeStatusUnknown
		return
	}

	if hash == prev {
		m.Statuses[key] = sources.ClaudeStatusIdle
	} else {
		m.Statuses[key] = sources.ClaudeStatusWorking
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

// statusLine renders the status portion of a session row.
func (m SessionsModel) statusLine(s MergedSession) string {
	if s.Source == SourceBuild && s.Build != nil {
		label := buildStatusLabel(s.Build.Status)
		variant := VariantMuted
		if s.Build.Live {
			variant = VariantAccent
		}
		return StatusDot(label, variant)
	}
	statusText := StatusDot("Detached", VariantMuted)
	if s.Legacy != nil && s.Legacy.Attached {
		statusText = StatusDot("Attached", VariantAccent)
	}
	if st, ok := m.Statuses[s.Key()]; ok {
		switch st {
		case sources.ClaudeStatusIdle:
			statusText = StatusDot("Idle", VariantMuted)
		case sources.ClaudeStatusWorking:
			statusText = StatusDot("Working", VariantAccent)
		}
	}
	return statusText
}

// infoLine renders secondary metadata for a session row.
func (m SessionsModel) infoLine(s MergedSession) string {
	if s.Source == SourceBuild && s.Build != nil {
		parts := []string{s.Build.ProjectLabel, s.Build.Agent}
		if idle := formatIdleTime(s.Build.UpdatedAt); idle != "" {
			parts = append(parts, idle)
		}
		return MutedText.Render(strings.Join(parts, " · "))
	}
	info := ""
	if s.Legacy != nil {
		info = MutedText.Render(fmt.Sprintf("%dw", s.Legacy.Windows))
		if idle := formatIdleTime(s.Legacy.LastUsed); idle != "" {
			info += MutedText.Render(" · " + idle)
		}
	}
	return info
}

// buildStatusLabel maps contract status values to display labels.
func buildStatusLabel(status string) string {
	switch status {
	case "needs_input":
		return "Needs input"
	case "starting":
		return "Starting"
	case "working":
		return "Working"
	case "idle":
		return "Idle"
	case "exited":
		return "Exited"
	case "disconnected":
		return "Disconnected"
	default:
		return status
	}
}

func (m SessionsModel) View(width, height int, focused bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading sessions...")
	}
	if len(m.Sessions) == 0 {
		empty := MutedText.Render("No tmux sessions running. Start one: ") +
			AccentText.Render("tmux new -s <name>")
		if m.BuildNote != "" {
			empty += "\n" + MutedText.Render(m.BuildNote)
		}
		return empty
	}

	// Render session cards horizontally
	var cards []string
	for i, s := range m.Sessions {
		card := m.renderCard(s, i == m.Cursor && focused)
		cards = append(cards, card)
	}

	// Join cards horizontally with gap
	row := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
	if m.BuildNote != "" {
		row += "\n" + MutedText.Render(m.BuildNote)
	}

	// If too wide, just truncate visually — lipgloss handles this
	return row
}

func (m SessionsModel) renderCard(s MergedSession, selected bool) string {
	nameStyle := BoldText
	if selected {
		nameStyle = nameStyle.Foreground(ColorAccent)
	}

	name := s.DisplayName()
	if s.Source == SourceBuild {
		name = "⚡ " + name
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

	content := nameStyle.Render(name) + "\n" +
		m.statusLine(s) + " " + m.infoLine(s)

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
		if m.BuildNote != "" {
			return MutedText.Render("No sessions · " + m.BuildNote)
		}
		return MutedText.Render("No sessions")
	}

	var lines []string
	for i, s := range m.Sessions {
		selected := i == m.Cursor && focused
		nameStyle := lipgloss.NewStyle().Foreground(ColorFg)
		if selected {
			nameStyle = nameStyle.Foreground(ColorAccent)
		}

		name := s.DisplayName()
		if s.Source == SourceBuild {
			name = "⚡ " + name
		}

		info := ""
		if s.Source == SourceLegacy && s.Legacy != nil {
			info = MutedText.Render(fmt.Sprintf("  %dw", s.Legacy.Windows))
		} else if s.Source == SourceBuild && s.Build != nil {
			info = MutedText.Render("  " + s.Build.ProjectLabel)
		}

		line := RowCursor(selected) + nameStyle.Render(name) + "  " +
			m.statusLine(s) + info
		lines = append(lines, line)
	}
	if m.BuildNote != "" {
		lines = append(lines, MutedText.Render(m.BuildNote))
	}
	return strings.Join(lines, "\n")
}
