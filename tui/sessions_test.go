package tui

import (
	"testing"

	"github.com/jhoot/cockpit/sources"
)

func TestAdoptReportedPrefersAHookOverTheGuess(t *testing.T) {
	m := NewSessionsModel()
	m.Sessions = []sources.TmuxSession{
		{Name: "app", Status: sources.AgentStatusNeedsInput, StatusReported: true},
		{Name: "docs"},
	}
	// A guess from the previous tick says app is idle. The hook knows better.
	m.UpdateStatus("app", "x")
	m.UpdateStatus("app", "x")

	m.AdoptReported()

	if got := m.Statuses["app"]; got != sources.AgentStatusNeedsInput {
		t.Errorf("app = %v, want the reported needs_input over the guessed idle", got)
	}
	if !m.Reported["app"] || m.Reported["docs"] {
		t.Errorf("reported = %v, want app only", m.Reported)
	}
}

func TestAdoptReportedForgetsASessionThatStoppedReporting(t *testing.T) {
	// A crashed agent goes stale in tmux and comes back as not reported. The
	// tile must drop back to inferred rather than freezing on the last report.
	m := NewSessionsModel()
	m.Sessions = []sources.TmuxSession{{Name: "app", Status: sources.AgentStatusWorking, StatusReported: true}}
	m.AdoptReported()

	m.Sessions = []sources.TmuxSession{{Name: "app"}}
	m.AdoptReported()

	if m.Reported["app"] {
		t.Error("a session that stopped reporting is no longer reported")
	}
}

func TestUpdateStatusDoesNotOverwriteAReport(t *testing.T) {
	// A capture already in flight when the hook landed must not clobber it.
	m := NewSessionsModel()
	m.Sessions = []sources.TmuxSession{{Name: "app", Status: sources.AgentStatusNeedsInput, StatusReported: true}}
	m.AdoptReported()

	m.UpdateStatus("app", "a")
	m.UpdateStatus("app", "b")

	if got := m.Statuses["app"]; got != sources.AgentStatusNeedsInput {
		t.Errorf("a pane snapshot overwrote a reported status: %v", got)
	}
}

func TestNeedingCaptureSkipsReportingSessions(t *testing.T) {
	// The per-session capture-pane is the most expensive poll cockpit runs.
	// A session that reports needs no guess.
	m := NewSessionsModel()
	m.Sessions = []sources.TmuxSession{
		{Name: "app", Status: sources.AgentStatusWorking, StatusReported: true},
		{Name: "docs"},
		{Name: "shell"},
	}
	m.AdoptReported()

	got := m.NeedingCapture()
	if len(got) != 2 || got[0].Name != "docs" || got[1].Name != "shell" {
		t.Errorf("want only the unreported sessions, got %+v", got)
	}
}
