package tui

import "strings"

// KeyhintsView renders the context-sensitive bottom key bar.
func KeyhintsView(mode Mode, focused PanelID, width int, buildAvailable bool) string {
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
	case ModeBuildLaunch:
		hints = []hint{
			{"Enter", "next/launch"},
			{"←→", "choose"},
			{"Esc", "back/cancel"},
		}
	default: // ModeNavigation
		hints = []hint{
			{"Tab", "panels"},
			{"j/k", "nav"},
			{"Enter", "jump/attach"},
			{"x", "toggle"},
			{"c", "cap"},
			{"n", "new"},
			{"v", "viz"},
			{"V", "pick"},
		}
		if buildAvailable {
			hints = append(hints, hint{"L", "launch"})
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
