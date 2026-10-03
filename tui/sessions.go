package tui

import (
	"fmt"
	"time"

	"github.com/jeffdhooton/cockpit/sources"
)

// SessionsModel holds the local session list and the hook-reported statuses
// the grid reads. Status is reported or unknown; there is no guess.
type SessionsModel struct {
	Sessions []sources.TmuxSession
	Cursor   int
	Loading  bool
	Statuses map[string]sources.AgentStatus // session key → reported status
	Reported map[string]bool                // session key → status came from a hook
}

// AdoptReported copies each session's hook-reported status into Statuses,
// so every reader of that map sees the report without knowing where it came
// from, and records which names are reported.
//
// A session that stopped reporting — a crashed agent going stale in tmux —
// drops out here rather than freezing on its last report.
func (m *SessionsModel) AdoptReported() {
	m.Statuses = make(map[string]sources.AgentStatus, len(m.Sessions))
	m.Reported = make(map[string]bool, len(m.Sessions))
	for _, s := range m.Sessions {
		if !s.StatusReported {
			continue
		}
		m.Statuses[s.Key()] = s.Status
		m.Reported[s.Key()] = true
	}
}

func NewSessionsModel() SessionsModel {
	return SessionsModel{Loading: true}
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
