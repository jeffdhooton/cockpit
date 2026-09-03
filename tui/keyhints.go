package tui

import "strings"

// KeyhintsView renders the context-sensitive bottom key bar.
func KeyhintsView(mode Mode, focused PanelID, width int) string {
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
	case ModeVizPicker:
		hints = []hint{
			{"↑↓", "nav"},
			{"Enter", "select"},
			{"Esc", "cancel"},
		}
	default: // ModeNavigation
		hints = []hint{
			{"Tab", "panels"},
			{"j/k", "nav"},
			{"Enter", "jump"},
			{"x", "toggle"},
			{"c", "cap"},
			{"n", "new"},
			{"v", "viz"},
			{"V", "pick"},
		}
		if focused == PanelSessions {
			hints = append(hints, hint{"s", "save"})
		}
		hints = append(hints,
			hint{"r", "refresh"},
			hint{"q", "quit"},
		)
	}

	var parts []string
	totalLen := 0
	for _, h := range hints {
		key := strings.ToUpper(h.key)
		part := AccentText.Render(key) + " " + MutedText.Render(h.desc)
		plainLen := len(key) + 1 + len(h.desc) + 3 // + separator
		if totalLen+plainLen > width && len(parts) > 0 {
			break // truncate from right
		}
		parts = append(parts, part)
		totalLen += plainLen
	}

	sep := MutedText.Render(" · ")
	return "  " + strings.Join(parts, sep)
}

// GridKeyhintsView renders the grid view's key bar. Hints truncate from the
// right, so the phone sees the first few and the desktop sees them all.
func GridKeyhintsView(width int) string {
	hints := []struct{ key, desc string }{
		{"hjkl", "nav"},
		// Second, so the digits survive truncation on the phone widths they
		// were added for.
		{"1-0", "open"},
		{"Enter", "jump"},
		{"n", "new"},
		{"s", "save"},
		{"/", "find"},
		{"d", "dash"},
		{"r", "refresh"},
		{"q", "quit"},
	}

	var parts []string
	totalLen := 0
	for _, h := range hints {
		key := strings.ToUpper(h.key)
		plainLen := len(key) + 1 + len(h.desc) + 3
		if totalLen+plainLen > width && len(parts) > 0 {
			break
		}
		parts = append(parts, AccentText.Render(key)+" "+MutedText.Render(h.desc))
		totalLen += plainLen
	}
	return "  " + strings.Join(parts, MutedText.Render(" · "))
}
