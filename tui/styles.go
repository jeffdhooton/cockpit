package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Tokyo Night color palette
var (
	ColorBg         = lipgloss.Color("#1a1b26")
	ColorFg         = lipgloss.Color("#a9b1d6")
	ColorAccent     = lipgloss.Color("#7aa2f7")
	ColorSuccess    = lipgloss.Color("#9ece6a")
	ColorWarning    = lipgloss.Color("#e0af68")
	ColorError      = lipgloss.Color("#f7768e")
	ColorMuted      = lipgloss.Color("#565f89")
	ColorPurple     = lipgloss.Color("#bb9af7")
	ColorSelectedBg = lipgloss.Color("#292e42")
	ColorBorder     = lipgloss.Color("#3b4261")
)

// Reusable styles
var (
	MutedText = lipgloss.NewStyle().Foreground(ColorMuted)
	BoldText  = lipgloss.NewStyle().Bold(true).Foreground(ColorFg)
	AccentText = lipgloss.NewStyle().Foreground(ColorAccent)
	PurpleText = lipgloss.NewStyle().Foreground(ColorPurple)
	SuccessText = lipgloss.NewStyle().Foreground(ColorSuccess)
	WarningText = lipgloss.NewStyle().Foreground(ColorWarning)
	ErrorText   = lipgloss.NewStyle().Foreground(ColorError)

	StatusClean    = lipgloss.NewStyle().Foreground(ColorSuccess)
	StatusDirty    = lipgloss.NewStyle().Foreground(ColorError)
	StatusUnpushed = lipgloss.NewStyle().Foreground(ColorWarning)

	SelectedRow = lipgloss.NewStyle().Background(ColorSelectedBg)
)

// RenderPanel renders a bordered panel with a title.
func RenderPanel(title string, content string, width int, height int, focused bool) string {
	borderColor := ColorBorder
	if focused {
		borderColor = ColorAccent
	}

	titleStyle := MutedText
	if focused {
		titleStyle = AccentText.Bold(true)
	}

	border := lipgloss.RoundedBorder()
	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Width(width - 2). // account for border
		Height(height - 2). // account for border
		Padding(0, 1)

	// Render title into the top border
	renderedTitle := titleStyle.Render(" " + title + " ")

	panel := style.Render(content)

	// Replace part of the top border with the title using visible width
	lines := strings.Split(panel, "\n")
	titleVisibleWidth := lipgloss.Width(renderedTitle)
	if len(lines) > 0 {
		topLine := lines[0]
		runes := []rune(topLine)
		if len(runes) > 2+titleVisibleWidth {
			lines[0] = string(runes[:2]) + renderedTitle + string(runes[2+titleVisibleWidth:])
		}
	}

	return strings.Join(lines, "\n")
}

// Truncate truncates a string at the last word boundary before maxLen, appending "…".
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return "…"
	}
	truncated := s[:maxLen-1]
	// Find last space for word boundary
	if idx := strings.LastIndex(truncated, " "); idx > 0 {
		truncated = truncated[:idx]
	}
	return truncated + "…"
}
