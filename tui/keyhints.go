package tui

import "strings"

type hint struct{ key, desc string }

// renderHints joins hints, truncating from the right so a phone sees the
// first few and a desktop sees them all.
func renderHints(hints []hint, width int) string {
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

// SessionsKeyhintsView renders the session view's key bar.
func SessionsKeyhintsView(width int, panes bool, badge string) string {
	nav := hint{"j/k", "sessions"}
	if panes {
		nav = hint{"j/k", "panes"}
	}
	return renderHints([]hint{
		nav, {"Tab", "panes/sessions"}, {"Enter", "attach"}, {"a", badge}, {"o", "open project"},
		{"p", "procs"}, {"l", "preview"}, {"1-0", "attach"}, {"n", "new"}, {"/", "filter"}, {"c", "cap"}, {"g", "grid"}, {"r", "refresh"}, {"q", "quit"},
	}, width)
}

// GridKeyhintsView renders the grid view's key bar. Hints truncate from the
// right, so the phone sees the first few and the desktop sees them all. Nested
// is true inside a host's grid, where backspace has somewhere to go. The
// attention badge is persistent: it is the second hint so it survives
// truncation on a phone. With the spine tile selected the preview scrolls,
// and its keys come straight after the badge.
func GridKeyhintsView(width int, nested bool, badge string, spine bool) string {
	hints := []hint{
		{"hjkl", "nav"},
		{"a", badge},
	}
	if spine {
		hints = append(hints, hint{"J/K", "scroll fleet"})
	}
	hints = append(hints, []hint{
		// Third, so the digits survive truncation on the phone widths they
		// were added for.
		{"1-0", "open"},
		{"Enter", "jump"},
		{"p", "procs"},
	}...)
	// Inside a host: the way out matters more than anything below it,
	// and at the root the key does nothing worth advertising.
	if nested {
		hints = append(hints, hint{"⌫", "back"})
	}
	hints = append(hints, []hint{
		{"n", "new"},
		{"s", "save"},
		{"/", "find"},
		{"d", "sessions"},
		{"c", "cap"},
		{"r", "refresh"},
		{"q", "quit"},
	}...)
	return renderHints(hints, width)
}

// AttentionKeyhintsView renders the queue's key bar.
func AttentionKeyhintsView(width int, detail, filtering bool) string {
	switch {
	case filtering:
		return renderHints([]hint{{"type", "filter"}, {"Enter", "apply"}, {"Esc", "clear"}}, width)
	case detail:
		return renderHints([]hint{{"Esc", "back"}, {"Enter", "attach"}, {"o", "open url"}}, width)
	}
	return renderHints([]hint{
		{"Enter", "open"},
		{"j/k", "nav"},
		{"Tab", "housekeeping"},
		{"/", "filter"},
		{"r", "refresh"},
		{"Esc", "back"},
	}, width)
}

// ProcessKeyhintsView renders the process panel's key bar, showing only the
// actions the selected row actually offers.
func ProcessKeyhintsView(width int, output bool, actions []string) string {
	if output {
		return renderHints([]hint{{"j/k", "scroll"}, {"G", "follow"}, {"L", "2000 lines"}, {"Esc", "back"}}, width)
	}
	hints := []hint{}
	for _, a := range actions {
		key, desc, ok := strings.Cut(a, " ")
		if !ok {
			continue
		}
		hints = append(hints, hint{key, desc})
	}
	hints = append(hints, hint{"c", "command"}, hint{"r", "refresh"}, hint{"Esc", "back"})
	return renderHints(hints, width)
}
