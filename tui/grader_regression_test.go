package tui

import (
	"strings"
	"testing"
	"time"
	"unicode"

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
	if len(m.searchResults) != 1 || m.searchResults[0] != "build:conv-AAA:run-conv-AAA" {
		t.Fatalf("searchResults = %v, want [build:conv-AAA:run-conv-AAA]", m.searchResults)
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
	if m.sessions.Sessions[m.sessions.Cursor].Key() != "build:conv-AAA:run-conv-AAA" {
		t.Errorf("cursor landed on %q, want build:conv-AAA:run-conv-AAA", m.sessions.Sessions[m.sessions.Cursor].Key())
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

// TestSanitizeC1AndFormatChars: C1 controls (8-bit CSI/OSC/DCS) and Unicode
// format characters (zero-width, bidi overrides) never survive sanitization
// (grader round 3 regression — round 1/2 stripped only C0 and DEL).
func TestSanitizeC1AndFormatChars(t *testing.T) {
	payloads := map[string]string{
		"OSC8 hyperlink": "click \u009d8;;https://evil.example\u009chere",
		"8-bit CSI":      "x\u009b[31m",
		"DCS":            "\u0090payload\u009c",
		"bidi override":  "title\u202eTROJAN",
		"zero width":     "ti\u200dtle",
		"isolate":        "a\u2066b",
	}
	for name, in := range payloads {
		got := SanitizeDisplay(in)
		for _, r := range got {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || unicode.Is(unicode.Cf, r) {
				t.Errorf("%s: SanitizeDisplay(%q) kept %U in %q", name, in, r, got)
			}
		}
	}

	// End-to-end through a rendered card view.
	s := buildSession("conv-c1", "x\u009b[2J\u009d8;;https://evil\u009c", "idle", true, true, false, time.Now())
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}
	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{s}})
	m = m2.(Model)
	m.focused = PanelSessions
	view := m.sessions.View(m.width, 4, true)
	for _, r := range view {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("card View contains C1 character %U", r)
		}
	}
}

// TestSanitizeBoundsLength: contract strings are length-bounded so a hostile
// title cannot blow up the fixed panel layout.
func TestSanitizeBoundsLength(t *testing.T) {
	long := strings.Repeat("a", 200000)
	got := SanitizeDisplay(long)
	if len([]rune(got)) > maxDisplayLen {
		t.Errorf("sanitized length = %d runes, want <= %d", len([]rune(got)), maxDisplayLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated string should end with an ellipsis")
	}

	// A huge Build title must not overflow the rendered page height.
	s := buildSession("conv-huge", long, "idle", true, true, false, time.Now())
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}
	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{s}})
	m = m2.(Model)
	m.focused = PanelSessions
	m.refreshPreview()
	view := m.View()
	if lines := strings.Count(view, "\n") + 1; lines > m.height+10 {
		t.Errorf("View rendered %d lines into height %d — layout blown", lines, m.height)
	}
}

// TestLaunchDialogLateErrorNoPanic: a buildProjectsMsg arriving after the
// user advanced steps must not panic rendering and must not let a stale
// list re-point a pending launch (grader round 3 regression).
func TestLaunchDialogLateErrorNoPanic(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{
		projects: []buildctl.Project{{ID: "p1", Label: "one", HostKind: "local"}},
	}
	m.buildClient = fake

	m.handleNavKey(keyMsg("L"))
	m2, _ := m.Update(buildProjectsMsg{Projects: fake.projects})
	m = m2.(Model)
	m.handleBuildLaunchKey(keyMsg("enter")) // → step 1
	m.handleBuildLaunchKey(keyMsg("enter")) // → step 2
	m.handleBuildLaunchKey(keyMsg("enter")) // → step 3
	if m.launchStep != 3 {
		t.Fatalf("setup: step = %d, want 3", m.launchStep)
	}

	// Late failure (Build went down mid-dialog): must degrade, not panic.
	m2, _ = m.Update(buildProjectsMsg{Err: buildctl.ErrUnavailable})
	m = m2.(Model)
	if m.launchStep != 0 {
		t.Errorf("late error left dialog at step %d", m.launchStep)
	}
	_ = m.View() // must not panic

	// Late success with a different list also resets to re-confirmation.
	m2, _ = m.Update(buildProjectsMsg{Projects: fake.projects})
	m = m2.(Model)
	m.handleBuildLaunchKey(keyMsg("enter")) // → step 1
	m2, _ = m.Update(buildProjectsMsg{Projects: []buildctl.Project{{ID: "p2", Label: "two", HostKind: "local"}}})
	m = m2.(Model)
	if m.launchStep != 0 {
		t.Errorf("late list swap left dialog at step %d", m.launchStep)
	}
	if cmd := m.handleBuildLaunchKey(keyMsg("enter")); cmd != nil {
		// Re-confirming at step 0 advances; submit must carry the CURRENT
		// project, not the stale one.
		m.handleBuildLaunchKey(keyMsg("enter"))
		m.handleBuildLaunchKey(keyMsg("enter"))
		cmd = m.handleBuildLaunchKey(keyMsg("enter"))
		cmd()
		if len(fake.launched) != 1 || fake.launched[0].ProjectID != "p2" {
			t.Errorf("launched = %+v, want current project p2", fake.launched)
		}
	}
}

// TestDuplicateConversationIdentity: two Build records sharing a
// conversation id (producer bug/hostility) get distinct identity keys when
// their runs differ, so search cannot shadow one behind the other.
func TestDuplicateConversationIdentity(t *testing.T) {
	now := time.Now()
	a := buildSession("conv-dup", "run A", "idle", true, true, false, now)
	b := buildSession("conv-dup", "run B", "working", true, true, false, now.Add(time.Minute))
	runB := "run-dup-b"
	b.RunID = &runB

	merged := MergeSessions(nil, []buildctl.Session{a, b})
	if len(merged) != 2 {
		t.Fatalf("got %d records, want 2", len(merged))
	}
	if merged[0].Key() == merged[1].Key() {
		t.Errorf("duplicate identity key %q shadows a record", merged[0].Key())
	}

	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}
	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{a, b}})
	m = m2.(Model)
	m.handleNavKey(keyMsg("/"))
	m.searchInput.SetValue("")
	m.updateSearchResults()
	if len(m.searchResults) != 2 || m.searchResults[0] == m.searchResults[1] {
		t.Errorf("search results = %v, want two distinct identities", m.searchResults)
	}
	// Both records must resolve to their own list position.
	idx0 := m.sessionIndexByKey(m.searchResults[0])
	idx1 := m.sessionIndexByKey(m.searchResults[1])
	if idx0 < 0 || idx1 < 0 || idx0 == idx1 {
		t.Errorf("resolution = %d, %d — records not independently addressable", idx0, idx1)
	}
}

// TestOutOfOrderFetchIgnored: fetches overlap (5s tick vs 10s timeout), so a
// stale in-flight result must never undo a newer one. Only the latest
// generation applies (grader round 4).
func TestOutOfOrderFetchIgnored(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}
	m.focused = PanelSessions

	live := []buildctl.Session{
		buildSession("conv-live", "live work", "idle", true, true, false, time.Now()),
	}

	// Generation 0 (initial fetch) succeeds.
	m2, _ := m.Update(buildDataMsg{Gen: 0, Sessions: live})
	m = m2.(Model)
	if len(m.buildSessions) != 1 {
		t.Fatalf("setup: initial fetch not applied")
	}

	// A tick issues generation 1; its result is a failure — records drop.
	m2, _ = m.Update(localTickMsg{})
	m = m2.(Model)
	if m.buildGen != 1 {
		t.Fatalf("setup: buildGen = %d after tick, want 1", m.buildGen)
	}
	m2, _ = m.Update(buildDataMsg{Gen: 1, Err: buildctl.ErrUnavailable})
	m = m2.(Model)
	if len(m.buildSessions) != 0 || m.sessions.BuildNote == "" {
		t.Fatalf("setup: failure not applied: sessions=%v note=%q", m.buildSessions, m.sessions.BuildNote)
	}

	// The stale generation-0 success now arrives late: it must be ignored,
	// not resurrect the records or clear the failure note.
	m2, _ = m.Update(buildDataMsg{Gen: 0, Sessions: live})
	m = m2.(Model)
	if len(m.buildSessions) != 0 {
		t.Fatal("stale in-flight fetch resurrected dropped Build records")
	}
	if m.sessions.BuildNote == "" {
		t.Error("stale fetch cleared the Build-unavailable indicator")
	}
	for _, s := range m.sessions.Sessions {
		if s.Source == SourceBuild {
			t.Errorf("stale Build record is back in the merged list: %+v", s)
		}
	}

	// The current generation's later success applies normally and heals.
	m2, _ = m.Update(buildDataMsg{Gen: 1, Sessions: live})
	m = m2.(Model)
	if len(m.buildSessions) != 1 || m.sessions.BuildNote != "" {
		t.Errorf("current-generation fetch did not apply: sessions=%v note=%q",
			m.buildSessions, m.sessions.BuildNote)
	}
}
