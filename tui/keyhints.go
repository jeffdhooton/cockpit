package tui

import "strings"

// KeyhintsView renders the context-sensitive bottom key bar.
func KeyhintsView(mode Mode, width int) string {
	type hint struct {
		key  string
		desc string
	}

	var hints []hint
	switch mode {
	case ModeCapture:
		hints = []hint{
			{"Enter", "save"},
			{"Esc", "cancel"},
		}
	case ModeNewSession:
		hints = []hint{
			{"Enter", "next/jump"},
			{"Ctrl+S", "save+jump"},
			{"Esc", "back/cancel"},
		}
	default: // ModeNavigation
		hints = []hint{
			{"Tab", "panels"},
			{"j/k", "nav"},
			{"Enter", "jump"},
			{"x", "toggle"},
			{"c", "cap"},
			{"n", "new"},
			{"r", "refresh"},
			{"q", "quit"},
		}
	}

	var parts []string
	totalLen := 0
	for _, h := range hints {
		part := AccentText.Render(h.key) + " " + MutedText.Render(h.desc)
		plainLen := len(h.key) + 1 + len(h.desc) + 2
		if totalLen+plainLen > width && len(parts) > 0 {
			break // truncate from right
		}
		parts = append(parts, part)
		totalLen += plainLen
	}

	return "  " + strings.Join(parts, "  ")
}
