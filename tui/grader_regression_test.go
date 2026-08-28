package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jhoot/cockpit/buildctl"
	"github.com/jhoot/cockpit/sources"
)

// Regression tests for grader round 1 findings.

// TestSearchEnterSurvivesRefresh: search results pin identity keys, not list
// positions. A refresh that re-sorts the merged list while the dialog is
// open must not retarget Enter at a different session.
func TestSearchEnterSurvivesRefresh(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	now := time.Now()
	// Legacy is newest, so it sorts first.
	m2, _ := m.Update(tmuxDataMsg{Sessions: []sources.TmuxSession{
		legacySession("zzz-legacy", now.Add(time.Hour)),
	}})
	m = m2.(Model)
	m2, _ = m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-AAA", "alpha", "idle", true, true, false, now),
	}})
	m = m2.(Model)

	// Open search, query matches only the Build session.
	m.handleNavKey(keyMsg("/"))
	m.searchInput.SetValue("alpha")
	m.updateSearchResults()
	if len(m.searchResults) != 1 || m.searchResults[0] != "build:conv-AAA" {
		t.Fatalf("searchResults = %v, want [build:conv-AAA]", m.searchResults)
	}

	// A refresh arrives while the dialog is open and re-sorts the list:
	// the legacy session is now even newer, pushing alpha to position 1.
	m2, _ = m.Update(tmuxDataMsg{Sessions: []sources.TmuxSession{
		legacySession("zzz-legacy", now.Add(2*time.Hour)),
	}})
	m = m2.(Model)
	if m.sessions.Sessions[0].Key() != "legacy:zzz-legacy" {
		t.Fatalf("setup: expected reorder, got %v", m.sessions.Sessions)
	}

	// Enter must still activate alpha — not whatever sits at its old index.
	cmd := m.handleSearchKey(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("Enter returned nil cmd")
	}
	if len(fake.attached) != 1 || fake.attached[0] != "run-conv-AAA" {
		t.Fatalf("attached = %v — Enter activated the wrong session after reorder", fake.attached)
	}
	if m.sessions.Sessions[m.sessions.Cursor].Key() != "build:conv-AAA" {
		t.Errorf("cursor landed on %q, want build:conv-AAA", m.sessions.Sessions[m.sessions.Cursor].Key())
	}
}

// TestSearchEnterOnVanishedSession: if the highlighted session disappears
// before Enter, nothing activates and the user gets a hint.
func TestSearchEnterOnVanishedSession(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-AAA", "alpha", "idle", true, true, false, time.Now()),
	}})
	m = m2.(Model)

	m.handleNavKey(keyMsg("/"))
	m.searchInput.SetValue("alpha")
	m.updateSearchResults()

	// Build goes away entirely (failed refresh drops stale records).
	m2, _ = m.Update(buildDataMsg{Err: buildctl.ErrUnavailable})
	m = m2.(Model)

	cmd := m.handleSearchKey(keyMsg("enter"))
	if len(fake.attached) != 0 || len(fake.resumed) != 0 {
		t.Fatalf("action fired on a vanished session: attached=%v resumed=%v", fake.attached, fake.resumed)
	}
	if cmd == nil {
		t.Fatal("expected a feedback cmd for the hint")
	}
	if !strings.Contains(m.transientErr, "no longer available") {
		t.Errorf("transient = %q, want a gone-session hint", m.transientErr)
	}
}

// TestEmptyRunIDNotAttachable: even if a hostile/buggy payload slips an
// empty-string run_id past the client, the gating layer refuses attach.
func TestEmptyRunIDNotAttachable(t *testing.T) {
	s := buildSession("conv-e", "t", "idle", true, true, false, time.Now())
	empty := ""
	s.RunID = &empty
	ms := MergedSession{Source: SourceBuild, Build: &s}
	if ms.Attachable() {
		t.Error("empty run_id must not be attachable")
	}
}

// TestContractStringsSanitized: ANSI/control sequences in contract strings
// never reach the terminal verbatim.
func TestContractStringsSanitized(t *testing.T) {
	evil := "evil\x1b[2J\x1b[H\r\n"
	s := buildSession("conv-ansi", evil, "idle", true, true, false, time.Now())
	s.ProjectLabel = "proj\x1b[31m"
	ms := MergedSession{Source: SourceBuild, Build: &s}

	if got := ms.DisplayName(); strings.ContainsAny(got, "\x1b\r\n") {
		t.Errorf("DisplayName = %q, control characters leaked", got)
	}
	if preview := buildPreviewText(ms); strings.ContainsAny(preview, "\x1b\r") {
		t.Errorf("preview = %q, control characters leaked", preview)
	}

	// End-to-end through the model's View.
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}
	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{s}})
	m = m2.(Model)
	m.focused = PanelSessions
	m.refreshPreview()
	view := m.View()
	if strings.Contains(view, "\x1b[2J") {
		t.Error("clear-screen sequence from contract title reached View output")
	}
}

// TestSanitizeProjectLabelAndAgent: control sequences in project_label or
// agent never reach the card or compact views (grader round 2 regression —
// round 1 fixed the title but missed these render boundaries).
func TestSanitizeProjectLabelAndAgent(t *testing.T) {
	s := buildSession("conv-ansi2", "clean title", "idle", true, true, false, time.Now())
	s.ProjectLabel = "proj\x1b[2J\x1b[H"
	s.Agent = "codex\x1b[31m"

	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}
	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{s}})
	m = m2.(Model)
	m.focused = PanelSessions

	if view := m.sessions.View(m.width, 4, true); strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b[31m") {
		t.Error("card View leaked control sequences from project_label/agent")
	}
	if view := m.sessions.CompactView(70, true); strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b[31m") {
		t.Error("CompactView leaked control sequences from project_label/agent")
	}
}

// TestContractErrorMessageSanitized: error.message from a contract failure
// envelope is sanitized before it reaches transientErr / launchErr.
func TestContractErrorMessageSanitized(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}

	evilErr := &buildctl.Error{
		Kind:    buildctl.KindNotFound,
		Code:    "stale_run",
		Message: "stale\x1b[2J\x1b[H run",
	}

	m2, _ := m.Update(buildActionResultMsg{Verb: "resume", Err: evilErr})
	m = m2.(Model)
	if strings.Contains(m.transientErr, "\x1b") {
		t.Errorf("transientErr = %q, control sequences leaked", m.transientErr)
	}
	if !strings.Contains(m.transientErr, "stale") {
		t.Errorf("transientErr = %q, sanitized message lost its content", m.transientErr)
	}

	m2, _ = m.Update(buildProjectsMsg{Err: evilErr})
	m = m2.(Model)
	if strings.Contains(m.launchErr, "\x1b") {
		t.Errorf("launchErr = %q, control sequences leaked", m.launchErr)
	}
}

// TestStalePreviewClearedAfterDrop: when a failed Build refresh drops the
// records, the preview of the vanished session is cleared too — stale
// contract state is never presented as current.
func TestStalePreviewClearedAfterDrop(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-gone", "vanishing", "idle", true, true, false, time.Now()),
	}})
	m = m2.(Model)
	m.focused = PanelSessions
	m.refreshPreview()
	if m.sessionPreview == "" {
		t.Fatal("setup: expected a Build preview")
	}

	m2, _ = m.Update(buildDataMsg{Err: buildctl.ErrUnavailable})
	m = m2.(Model)
	if m.sessionPreview != "" {
		t.Errorf("stale preview survived record drop:\n%s", m.sessionPreview)
	}
}
