package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Catppuccin Mocha color palette
var (
	ColorBg         = lipgloss.Color("#1e1e2e")
	ColorFg         = lipgloss.Color("#cdd6f4")
	ColorAccent     = lipgloss.Color("#89b4fa") // blue
	ColorSuccess    = lipgloss.Color("#a6e3a1") // green
	ColorWarning    = lipgloss.Color("#f9e2af") // yellow
	ColorError      = lipgloss.Color("#f38ba8") // red
	ColorMuted      = lipgloss.Color("#6c7086") // overlay0
	ColorPurple     = lipgloss.Color("#cba6f7") // mauve
	ColorSelectedBg = lipgloss.Color("#313244") // surface0
	ColorBorder     = lipgloss.Color("#45475a") // surface1
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

// RenderPanel renders a bordered panel with a title inside the border.
// Content is hard-clipped to fit within the panel height.
func RenderPanel(title string, content string, width int, height int, focused bool) string {
	borderColor := ColorBorder
	if focused {
		borderColor = ColorAccent
	}

	titleStyle := MutedText
	if focused {
		titleStyle = AccentText.Bold(true)
	}

	// Title is the first line of content — no border surgery
	titledContent := titleStyle.Render(title) + "\n" + content

	// Hard-clip content lines to fit: height - 2 (border) - 0 (padding top/bottom)
	// The inner area is height-2, and we have no vertical padding.
	maxLines := height - 2
	if maxLines < 1 {
		maxLines = 1
	}
	titledContent = ClipLines(titledContent, maxLines)

	border := lipgloss.RoundedBorder()
	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2).
		Padding(0, 1)

	return style.Render(titledContent)
}

// ClipLines truncates s to at most maxLines lines.
func ClipLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n")
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
