package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Carbon-green palette: a near-black ground with a green cast, accented by
// terminal green. Chrome recedes; only status and focus carry colour.
var (
	ColorBg         = lipgloss.Color("#0b0f0b")
	ColorSurface    = lipgloss.Color("#151a15")
	ColorSurfaceAlt = lipgloss.Color("#1c231c")
	ColorFg         = lipgloss.Color("#cdd6cd")
	ColorMuted      = lipgloss.Color("#6e7d6e")
	ColorAccent     = lipgloss.Color("#3fb950") // green
	ColorSuccess    = lipgloss.Color("#3fb950")
	ColorWarning    = lipgloss.Color("#d29922")
	ColorError      = lipgloss.Color("#f85149")
	ColorPurple     = lipgloss.Color("#a371f7")
	ColorBorder     = lipgloss.Color("#232a23") // subtle: unfocused panels
	ColorSelectedBg = ColorSurfaceAlt
)

// Reusable styles
var (
	MutedText   = lipgloss.NewStyle().Foreground(ColorMuted)
	BoldText    = lipgloss.NewStyle().Bold(true).Foreground(ColorFg)
	AccentText  = lipgloss.NewStyle().Foreground(ColorAccent)
	PurpleText  = lipgloss.NewStyle().Foreground(ColorPurple)
	SuccessText = lipgloss.NewStyle().Foreground(ColorSuccess)
	WarningText = lipgloss.NewStyle().Foreground(ColorWarning)
	ErrorText   = lipgloss.NewStyle().Foreground(ColorError)

	StatusClean    = lipgloss.NewStyle().Foreground(ColorSuccess)
	StatusDirty    = lipgloss.NewStyle().Foreground(ColorError)
	StatusUnpushed = lipgloss.NewStyle().Foreground(ColorWarning)

	SelectedRow = lipgloss.NewStyle().Background(ColorSelectedBg)
)

// SelBar is the left accent bar marking a selected row. Two cells wide with
// its trailing space, matching the plain "  " prefix so columns stay aligned.
const SelBar = "▎"

// RowCursor returns the row prefix for a list row — an accent bar when
// selected, blank padding otherwise. Always two cells wide.
func RowCursor(selected bool) string {
	if selected {
		return AccentText.Render(SelBar) + " "
	}
	return "  "
}

// Variant selects the colour role for chips, dots, and labels.
type Variant int

const (
	VariantNeutral Variant = iota
	VariantAccent
	VariantWarning
	VariantError
	VariantMuted
)

func variantColor(v Variant) lipgloss.Color {
	switch v {
	case VariantAccent:
		return ColorAccent
	case VariantWarning:
		return ColorWarning
	case VariantError:
		return ColorError
	case VariantMuted:
		return ColorMuted
	default:
		return ColorFg
	}
}

// SectionLabel renders an uppercase panel or section heading. Unfocused
// headings sit back in muted grey; the focused one brightens to the accent.
func SectionLabel(s string, focused bool) string {
	label := strings.ToUpper(s)
	if focused {
		return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(label)
	}
	return lipgloss.NewStyle().Foreground(ColorMuted).Bold(true).Render(label)
}

// Chip renders a single-line filled pill, e.g. OPUS-5 or XHIGH. Chips carry a
// background, so avoid nesting them inside a row that also paints a background.
func Chip(text string, v Variant) string {
	return lipgloss.NewStyle().
		Foreground(variantColor(v)).
		Background(ColorSurfaceAlt).
		Padding(0, 1).
		Render(strings.ToUpper(text))
}

// StatusDot renders a filled dot followed by its label, e.g. "● Working".
func StatusDot(label string, v Variant) string {
	style := lipgloss.NewStyle().Foreground(variantColor(v))
	return style.Render("●") + " " + style.Render(label)
}

// Breadcrumb renders a muted uppercase trail, e.g. "LOCAL / COCKPIT".
func Breadcrumb(parts ...string) string {
	upper := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		upper = append(upper, strings.ToUpper(p))
	}
	return MutedText.Render(strings.Join(upper, " / "))
}

// Rule renders a horizontal divider with an optional inline caption.
func Rule(caption string, width int) string {
	if width < 4 {
		width = 4
	}
	if caption == "" {
		return MutedText.Render(strings.Repeat("─", width))
	}
	head := "─── " + caption + " "
	pad := width - lipgloss.Width(head)
	if pad < 0 {
		pad = 0
	}
	return MutedText.Render(head + strings.Repeat("─", pad))
}

// RenderPanel renders a bordered panel with a title inside the border.
// Content is hard-clipped to fit within the panel height.
func RenderPanel(title string, content string, width int, height int, focused bool) string {
	borderColor := ColorBorder
	if focused {
		borderColor = ColorAccent
	}

	// Title is the first line of content — no border surgery
	titledContent := SectionLabel(title, focused) + "\n" + content

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
		Width(width-2).
		Height(height-2).
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
