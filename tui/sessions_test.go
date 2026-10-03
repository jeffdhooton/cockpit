package tui

import (
	"testing"

	"github.com/jeffdhooton/cockpit/sources"
)

func TestAdoptReportedRecordsHookStatuses(t *testing.T) {
	m := NewSessionsModel()
	m.Sessions = []sources.TmuxSession{
		{Name: "app", Status: sources.AgentStatusNeedsInput, StatusReported: true},
		{Name: "docs"},
	}
	m.AdoptReported()

	if got := m.Statuses["app"]; got != sources.AgentStatusNeedsInput {
		t.Errorf("app = %v, want the reported needs_input", got)
	}
	if !m.Reported["app"] || m.Reported["docs"] {
		t.Errorf("reported = %v, want app only", m.Reported)
	}
	if _, ok := m.Statuses["docs"]; ok {
		t.Error("an unreported session has no status; nothing is guessed")
	}
}

func TestAdoptReportedForgetsASessionThatStoppedReporting(t *testing.T) {
	// A crashed agent goes stale in tmux and comes back as not reported. The
	// tile must drop back to unknown rather than freezing on the last report.
	m := NewSessionsModel()
	m.Sessions = []sources.TmuxSession{{Name: "app", Status: sources.AgentStatusWorking, StatusReported: true}}
	m.AdoptReported()

	m.Sessions = []sources.TmuxSession{{Name: "app"}}
	m.AdoptReported()

	if m.Reported["app"] {
		t.Error("a session that stopped reporting is no longer reported")
	}
	if _, ok := m.Statuses["app"]; ok {
		t.Error("its stale status must not linger")
	}
}
