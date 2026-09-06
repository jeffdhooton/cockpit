package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/sources"
)

func wsFixture(now time.Time) sources.Workspace {
	blocked := sources.PaneReport{Session: "api", SessionID: "$2", WindowID: "@1", PaneID: "%9", Generation: "g1", Command: "claude", WindowName: "reviewer", Recorded: true, Reported: true, Status: sources.AgentStatusNeedsInput, Invocation: "a", Active: true}
	local := sources.HostReport{Outcome: sources.ObservationFresh, ObservedAt: now,
		Sessions: []sources.TmuxSession{{Name: "site", ID: "$1", Generation: "g1"}, {Name: "api", ID: "$2", Generation: "g1"}},
		Panes: []sources.PaneReport{
			{Session: "site", SessionID: "$1", WindowID: "@1", PaneID: "%1", Generation: "g1", Command: "zsh", WindowName: "zsh", Active: true},
			{Session: "site", SessionID: "$1", WindowID: "@2", PaneID: "%2", Generation: "g1", Command: "codex", WindowName: "codex"},
			blocked,
		},
	}
	remote := sources.HostReport{Host: "mini", Outcome: sources.ObservationUnavailable, Err: "not polled yet"}
	return sources.BuildWorkspace(sources.AttentionInput{Hosts: []sources.HostReport{local, remote}, Now: now, SelfSession: "cockpit"})
}

func TestSessionsSelectionSurvivesReorder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	if s.current() == nil || s.current().Name != "api" {
		t.Fatalf("first = %+v", s.current())
	}
	s.move(1)
	if s.current().Name != "site" {
		t.Fatalf("after move = %+v", s.current())
	}
	// site's codex starts needing input: site now sorts first; selection
	// stays on site.
	ws := wsFixture(now)
	for i := range ws.Hosts[0].Sessions {
		if ws.Hosts[0].Sessions[i].Name == "site" {
			ws.Hosts[0].Sessions[i].Status = "needs_input"
		}
	}
	s.refresh(ws)
	if s.current().Name != "site" || s.selected != "site" {
		t.Errorf("selection moved: %+v", s.current())
	}
}

func TestSessionsTabMovesFocusToPanes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	s.move(1) // site
	s.paneFocus = true
	if p := s.currentPane(); p == nil || p.PaneID != "%1" {
		t.Fatalf("first pane = %+v", p)
	}
	s.move(1)
	if p := s.currentPane(); p == nil || p.PaneID != "%2" || p.Kind != sources.PaneAgent {
		t.Errorf("second pane = %+v", p)
	}
	s.paneFocus = false
	s.move(-1) // back to api; pane selection resets to api's active pane
	if p := s.currentPane(); p == nil || p.PaneID != "%9" {
		t.Errorf("pane after session change = %+v", p)
	}
}

func TestSessionsLatePreviewIsDropped(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	s.move(1)
	s.paneFocus = true
	s.preview.seq = 3
	s.move(1) // now on %2
	if s.applyPreview(panePreviewMsg{paneID: "%1", seq: 3, text: "old pane"}) {
		t.Error("a reply for another pane must be dropped")
	}
	s.preview.seq = 5
	if s.applyPreview(panePreviewMsg{paneID: "%2", seq: 4, text: "stale"}) {
		t.Error("an older sequence must be dropped")
	}
	if !s.applyPreview(panePreviewMsg{paneID: "%2", seq: 5, text: "\x1b[31mfresh\x1b[0m"}) || s.preview.text != "fresh" {
		t.Errorf("preview = %+v", s.preview)
	}
}

func TestSessionsViewBreakpoints(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	wide := s.view(120, 36, now, false)
	if !strings.Contains(wide, "LOCAL / API") {
		t.Errorf("wide layout should carry a detail header:\n%s", wide)
	}
	for _, l := range strings.Split(wide, "\n") {
		if lipglossWidth(l) > 120 {
			t.Errorf("line wider than 120: %q", l)
		}
	}
	if !strings.Contains(wide, "MINI") || !strings.Contains(wide, "unavailable") {
		t.Errorf("an unpolled host must be visible as unavailable:\n%s", wide)
	}
	narrow := s.view(40, 12, now, false)
	lines := strings.Split(narrow, "\n")
	if len(lines) > 12 {
		t.Errorf("narrow render is %d lines", len(lines))
	}
	for _, l := range lines {
		if lipglossWidth(l) > 40 {
			t.Errorf("line wider than 40: %q", l)
		}
	}
	if !strings.Contains(narrow, "api") || !strings.Contains(narrow, "reviewer") {
		t.Errorf("narrow layout must show the session and its pane:\n%s", narrow)
	}
}

func TestSessionsViewStripsControlSequences(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ws := wsFixture(now)
	ws.Hosts[0].Sessions[0].Panes[0].WindowName = "evil\x1b[2Jname"
	ws.Hosts[0].Sessions[0].Panes[0].Path = "/tmp/\x07bell"
	s := newSessionsModel()
	s.refresh(ws)
	s.applyPreview(panePreviewMsg{paneID: s.pane, seq: 0, text: "\x1b]0;x\x07preview"})
	out := s.view(120, 36, now, false)
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x07") || strings.Contains(out, "\x1b]0;") {
		t.Error("control sequences leaked")
	}
	if !strings.Contains(out, "preview") {
		t.Errorf("preview text should still render:\n%s", out)
	}
}
