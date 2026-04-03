package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jhoot/cockpit/sources"
)


// SignalsModel manages the signals/attention panel.
type SignalsModel struct {
	Signals []Signal
	Loading bool
}

// Signal represents a single attention item.
type Signal struct {
	Icon    string
	Message string
	Level   string // "warning", "error", "info"
}

func NewSignalsModel() SignalsModel {
	return SignalsModel{Loading: true}
}

// UpdateSignals aggregates signals from all sources.
func (m *SignalsModel) UpdateSignals(
	sessions []sources.TmuxSession,
	repos []sources.GitRepoStatus,
	github *sources.GitHubStatus,
	staleThreshold time.Duration,
) {
	m.Loading = false
	var signals []Signal

	// --- Errors (highest priority) ---

	// Failing CI — per repo detail
	if github != nil {
		for _, rc := range github.RepoChecks {
			if rc.CIStatus == "failing" {
				signals = append(signals, Signal{
					Icon:    "✗",
					Message: fmt.Sprintf("%s — CI failing", rc.RepoLabel),
					Level:   "error",
				})
			}
		}
	}

	// --- Warnings ---

	// Dirty repos (uncommitted changes)
	for _, r := range repos {
		if r.DirtyCount > 0 {
			signals = append(signals, Signal{
				Icon:    "●",
				Message: fmt.Sprintf("%s — %d uncommitted file(s) on %s", r.Label, r.DirtyCount, r.Branch),
				Level:   "warning",
			})
		}
	}

	// Unpushed commits
	for _, r := range repos {
		if r.Unpushed > 0 {
			signals = append(signals, Signal{
				Icon:    "↑",
				Message: fmt.Sprintf("%s — %d unpushed commit(s) on %s", r.Label, r.Unpushed, r.Branch),
				Level:   "warning",
			})
		}
	}

	// Behind remote
	for _, r := range repos {
		if r.Behind > 0 {
			signals = append(signals, Signal{
				Icon:    "↓",
				Message: fmt.Sprintf("%s — %d commit(s) behind remote on %s", r.Label, r.Behind, r.Branch),
				Level:   "warning",
			})
		}
	}

	// PRs awaiting review
	if github != nil && github.PRsAwaitingReview > 0 {
		signals = append(signals, Signal{
			Icon:    "◉",
			Message: fmt.Sprintf("%d PR(s) awaiting your review", github.PRsAwaitingReview),
			Level:   "warning",
		})
	}

	// --- Info ---

	// Stale sessions
	for _, s := range sessions {
		if !s.Attached && !s.LastUsed.IsZero() && time.Since(s.LastUsed) > staleThreshold {
			signals = append(signals, Signal{
				Icon:    "⏱",
				Message: fmt.Sprintf("%s — idle for %s", s.Name, formatIdleTime(s.LastUsed)),
				Level:   "info",
			})
		}
	}

	m.Signals = signals
}

func (m SignalsModel) View(width, height int, focused bool) string {
	if m.Loading {
		return MutedText.Render("⠋ Loading signals...")
	}
	if len(m.Signals) == 0 {
		return SuccessText.Render("✓ All clear — nothing needs attention")
	}

	var lines []string
	for _, sig := range m.Signals {
		var icon string
		switch sig.Level {
		case "error":
			icon = ErrorText.Render(sig.Icon)
		case "warning":
			icon = WarningText.Render(sig.Icon)
		default:
			icon = MutedText.Render(sig.Icon)
		}
		lines = append(lines, "  "+icon+" "+sig.Message)
	}
	return strings.Join(lines, "\n")
}
