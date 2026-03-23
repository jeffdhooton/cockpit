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

// RenderPanel renders a bordered panel with a title in the top border.
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

	// Render panel WITHOUT top border — we'll build it manually
	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		BorderTop(false).
		Width(width - 2).
		Height(height - 2).
		Padding(0, 1)

	body := style.Render(content)

	// Build the top border line manually with the title embedded
	renderedTitle := titleStyle.Render(" " + title + " ")
	titleWidth := lipgloss.Width(renderedTitle)

	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	topLeft := borderStyle.Render(string(border.TopLeft))
	topRight := borderStyle.Render(string(border.TopRight))
	hBar := borderStyle.Render(string(border.Top))

	// Fill: corner + title + remaining horizontal bars + corner
	remaining := width - 2 - titleWidth // -2 for corners
	if remaining < 0 {
		remaining = 0
	}
	topLine := topLeft + renderedTitle + strings.Repeat(hBar, remaining) + topRight

	return topLine + "\n" + body
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
