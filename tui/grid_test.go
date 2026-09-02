package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

func sess(name string) sources.TmuxSession {
	return sources.TmuxSession{Name: name, Windows: 1}
}

func repo(label string) sources.GitRepoStatus {
	return sources.GitRepoStatus{Label: label, Path: "/tmp/" + label, Branch: "main"}
}

func labels(targets []Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Label)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBuildTargetsJoinsSessionAndRepo(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("my-app")},
		[]sources.GitRepoStatus{repo("my-app")},
		nil, "cockpit",
	)
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1 (session and repo must join, not duplicate)", len(targets))
	}
	if targets[0].Session == nil || targets[0].Repo == nil {
		t.Fatalf("joined target missing a side: session=%v repo=%v", targets[0].Session, targets[0].Repo)
	}
	if !targets[0].Running() {
		t.Error("target with a session should report Running")
	}
}

func TestBuildTargetsSessionWithoutRepo(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("scratch")}, nil, nil, "cockpit")
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Repo != nil {
		t.Error("session with no config entry should have nil Repo")
	}
}

func TestBuildTargetsDormantRepo(t *testing.T) {
	targets := BuildTargets(nil, []sources.GitRepoStatus{repo("dotfiles")}, nil, "cockpit")
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Running() {
		t.Error("repo with no session should not report Running")
	}
}

func TestBuildTargetsExcludesSelfSession(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("cockpit"), sess("my-app")},
		nil, nil, "cockpit",
	)
	eq(t, labels(targets), []string{"my-app"})
}

func TestBuildTargetsOrdersRunningBeforeDormantAlphabetically(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("zeta"), sess("alpha")},
		[]sources.GitRepoStatus{repo("beta"), repo("alpha")},
		nil, "cockpit",
	)
	eq(t, labels(targets), []string{"alpha", "zeta", "beta"})
}

func TestBuildTargetsCarriesStatus(t *testing.T) {
	statuses := map[string]sources.AgentStatus{"my-app": sources.AgentStatusWorking}
	targets := BuildTargets([]sources.TmuxSession{sess("my-app")}, nil, statuses, "cockpit")
	if targets[0].Status != sources.AgentStatusWorking {
		t.Errorf("Status = %v, want Working", targets[0].Status)
	}
}

func TestBuildTargetsEmpty(t *testing.T) {
	if got := BuildTargets(nil, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("got %d targets from empty inputs, want 0", len(got))
	}
}

// GridCols measures against the grid's content width, not the terminal width.
func TestGridCols(t *testing.T) {
	cases := []struct{ contentWidth, want int }{
		{20, 1},  // 24-col terminal, the narrow floor
		{40, 2},  // 44-col terminal, Termius vertical
		{66, 3},  // 70-col terminal
		{116, 4}, // 120-col terminal, capped
		{196, 4}, // 200-col terminal, capped
	}
	for _, c := range cases {
		if got := GridCols(c.contentWidth); got != c.want {
			t.Errorf("GridCols(%d) = %d, want %d", c.contentWidth, got, c.want)
		}
	}
}

// A keypress must move by the same number of columns the eye sees. These two
// read the column count from different call sites, and drifted apart once.
func TestRenderAndKeysAgreeOnColumnCount(t *testing.T) {
	for _, width := range []int{44, 70, 120} {
		m := gridTestModel(width, 40)
		m.sessions.Sessions = []sources.TmuxSession{
			sess("aa"), sess("bb"), sess("cc"), sess("dd"), sess("ee"), sess("ff"),
		}
		m.repos.Repos = nil
		targets := m.gridTargets()

		// Columns the renderer actually drew, counted as tiles on the first row.
		firstRow := strings.Split(RenderGrid(targets, 0, m.gridContentWidth(), 40), "\n")[0]
		drawn := strings.Count(firstRow, "╭")

		// Columns a single `j` moves by.
		m.setGridCursor(targets, 0)
		m.handleGridKey(keyMsg("j"))
		moved := resolveGridCursor(targets, m.gridCursor, m.gridIndex)

		if drawn != moved {
			t.Errorf("width=%d: renderer drew %d columns but `j` moved %d",
				width, drawn, moved)
		}
	}
}

func TestGridIsTwoWideAtPhoneWidth(t *testing.T) {
	m := gridTestModel(44, 24)
	m.sessions.Sessions = []sources.TmuxSession{sess("aa"), sess("bb"), sess("cc"), sess("dd")}
	m.repos.Repos = nil
	firstRow := strings.Split(RenderGrid(m.gridTargets(), 0, m.gridContentWidth(), 20), "\n")[0]
	if got := strings.Count(firstRow, "╭"); got != 2 {
		t.Errorf("44-col terminal drew %d tiles per row, want 2:\n%s", got, firstRow)
	}
}

func TestGridColsNeverZero(t *testing.T) {
	for _, w := range []int{0, 1, 10, 21} {
		if got := GridCols(w); got < 1 {
			t.Errorf("GridCols(%d) = %d, want >= 1", w, got)
		}
	}
}

func TestMoveGridCursorClampsAtEdges(t *testing.T) {
	// 5 targets, 2 columns: rows are [0 1] [2 3] [4]
	cases := []struct {
		name        string
		idx, dx, dy int
		want        int
	}{
		{"left at start", 0, -1, 0, 0},
		{"right at end", 4, 1, 0, 4},
		{"up from top row", 1, 0, -1, 0},
		{"down from full row", 2, 0, 1, 4},
		{"down onto short final row clamps to last", 3, 0, 1, 4},
		{"down from last row stays", 4, 0, 1, 4},
		{"up from last row", 4, 0, -1, 2},
		{"right within row", 0, 1, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MoveGridCursor(c.idx, 5, 2, c.dx, c.dy); got != c.want {
				t.Errorf("MoveGridCursor(%d, 5, 2, %d, %d) = %d, want %d",
					c.idx, c.dx, c.dy, got, c.want)
			}
		})
	}
}

func TestMoveGridCursorEmpty(t *testing.T) {
	if got := MoveGridCursor(0, 0, 2, 0, 1); got != 0 {
		t.Errorf("MoveGridCursor on empty grid = %d, want 0", got)
	}
}

func TestResolveGridCursorFindsLabel(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("alpha"), sess("beta"), sess("gamma")},
		nil, nil, "cockpit",
	)
	if got := resolveGridCursor(targets, "gamma", 0); got != 2 {
		t.Errorf("resolveGridCursor = %d, want 2", got)
	}
}

func TestResolveGridCursorSurvivesInsertionAbove(t *testing.T) {
	// Selection is on "gamma" at index 1; "alpha" then appears above it.
	before := BuildTargets([]sources.TmuxSession{sess("beta"), sess("gamma")}, nil, nil, "cockpit")
	idx := resolveGridCursor(before, "gamma", 0)
	after := BuildTargets(
		[]sources.TmuxSession{sess("alpha"), sess("beta"), sess("gamma")},
		nil, nil, "cockpit",
	)
	if got := resolveGridCursor(after, "gamma", idx); got != 2 {
		t.Errorf("cursor moved off gamma: got index %d, want 2", got)
	}
}

func TestResolveGridCursorVanishedLabelClamps(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("alpha")}, nil, nil, "cockpit")
	if got := resolveGridCursor(targets, "gone", 7); got != 0 {
		t.Errorf("resolveGridCursor with stale label = %d, want 0 (clamped)", got)
	}
	if got := resolveGridCursor(nil, "gone", 3); got != 0 {
		t.Errorf("resolveGridCursor on empty targets = %d, want 0", got)
	}
}

func TestRenderGridFitsWidth(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("my-app"), sess("cockpit-ui"), sess("a-very-long-session-name-here")},
		[]sources.GitRepoStatus{repo("dotfiles"), repo("side-project")},
		nil, "cockpit",
	)
	for _, width := range []int{40, 55, 120} {
		out := RenderGrid(targets, 0, width, 20)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width=%d: line %d is %d cells wide: %q", width, i, w, line)
			}
		}
	}
}

func TestRenderGridHandlesOddAndEmptyCounts(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		var sessions []sources.TmuxSession
		for i := 0; i < n; i++ {
			sessions = append(sessions, sess(string(rune('a'+i))))
		}
		targets := BuildTargets(sessions, nil, nil, "cockpit")
		out := RenderGrid(targets, 0, 44, 20) // must not panic
		if out == "" {
			t.Errorf("n=%d: RenderGrid returned empty string", n)
		}
	}
}

func TestRenderGridShowsMoreIndicatorWhenClipped(t *testing.T) {
	var sessions []sources.TmuxSession
	for i := 0; i < 12; i++ {
		sessions = append(sessions, sess("s"+string(rune('a'+i))))
	}
	targets := BuildTargets(sessions, nil, nil, "cockpit")
	// height 10 fits 2 rows of 2 columns = 4 tiles, leaving 8 hidden.
	out := RenderGrid(targets, 0, 44, 10)
	if !strings.Contains(out, "more") {
		t.Errorf("clipped grid should show a 'more' indicator, got:\n%s", out)
	}
}

func TestRenderGridScrollsToKeepCursorVisible(t *testing.T) {
	var sessions []sources.TmuxSession
	for i := 0; i < 12; i++ {
		sessions = append(sessions, sess("s"+string(rune('a'+i))))
	}
	targets := BuildTargets(sessions, nil, nil, "cockpit")
	last := len(targets) - 1
	out := RenderGrid(targets, last, 44, 10)
	if !strings.Contains(out, targets[last].Label) {
		t.Errorf("selected target %q not visible in clipped grid:\n%s", targets[last].Label, out)
	}
}

func TestRenderTileShowsGitStateForDormantRepo(t *testing.T) {
	r := repo("dotfiles")
	r.Branch = "main"
	r.Dirty = true
	r.DirtyCount = 3
	targets := BuildTargets(nil, []sources.GitRepoStatus{r}, nil, "cockpit")
	out := renderTile(targets[0], 22, false)
	if !strings.Contains(out, "main") {
		t.Errorf("dormant tile should show its branch, got:\n%s", out)
	}
}

func TestRenderTileDistinguishesIdleFromNoSession(t *testing.T) {
	idleSession := sess("idle-app")
	idle := Target{
		Label:   idleSession.Name,
		Session: &idleSession,
		Status:  sources.AgentStatusIdle,
	}
	dormantRepo := repo("dormant-app")
	dormant := Target{Label: dormantRepo.Label, Repo: &dormantRepo}

	idleOut := renderTile(idle, 22, false)
	dormantOut := renderTile(dormant, 22, false)

	if !strings.Contains(idleOut, "●") || strings.Contains(idleOut, "○") {
		t.Errorf("idle session should use only the filled live-session marker:\n%s", idleOut)
	}
	if !strings.Contains(dormantOut, "○") || strings.Contains(dormantOut, "●") {
		t.Errorf("repo with no session should use only the hollow absence marker:\n%s", dormantOut)
	}
}

// lipgloss Width() counts padding but not border, so the tile's declared width
// must account for both or tiles come up short and leave a ragged gap.
func TestRenderTileFillsItsCell(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("my-app")}, nil, nil, "cockpit")
	for _, width := range []int{18, 20, 29} {
		out := renderTile(targets[0], width, false)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w != width {
				t.Errorf("renderTile(width=%d) line %d is %d cells: %q", width, i, w, line)
			}
		}
	}
}

func TestRenderTileHeight(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("my-app")}, nil, nil, "cockpit")
	out := renderTile(targets[0], 22, true)
	if got := len(strings.Split(out, "\n")); got != gridTileH {
		t.Errorf("tile height = %d lines, want %d", got, gridTileH)
	}
}

func gridTestModel(width, height int) Model {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = width
	m.height = height
	m.layout = CalculateLayout(width, height, 0)
	m.sessions.Loading = false
	m.sessions.Sessions = []sources.TmuxSession{sess("my-app"), sess("scry")}
	m.repos.Loading = false
	m.repos.Repos = []sources.GitRepoStatus{repo("dotfiles")}
	return m
}

func TestDefaultViewIsGrid(t *testing.T) {
	cfg := testConfig()
	cfg.General.DefaultView = "grid"
	if m := NewModel(cfg, "/tmp/config.toml"); m.view != ViewGrid {
		t.Errorf("view = %v, want ViewGrid", m.view)
	}
}

func TestDashboardViewHonoursConfig(t *testing.T) {
	cfg := testConfig()
	cfg.General.DefaultView = "dashboard"
	if m := NewModel(cfg, "/tmp/config.toml"); m.view != ViewDashboard {
		t.Errorf("view = %v, want ViewDashboard", m.view)
	}
}

func TestGridTargetsExcludeCockpitSession(t *testing.T) {
	m := gridTestModel(120, 40)
	m.sessions.Sessions = append(m.sessions.Sessions, sess("cockpit"))
	for _, tg := range m.gridTargets() {
		if tg.Label == "cockpit" {
			t.Error("cockpit's own session must not appear as a target")
		}
	}
}

func TestGridViewRendersAtPhoneWidth(t *testing.T) {
	m := gridTestModel(44, 24)
	out := m.View()
	if !strings.Contains(out, "my-app") {
		t.Errorf("phone-width view should list sessions, got:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 44 {
			t.Errorf("line %d is %d cells wide at width 44: %q", i, w, line)
		}
	}
}

func TestNarrowFloorIsTwentyFour(t *testing.T) {
	m := gridTestModel(20, 24)
	if out := m.View(); !strings.Contains(out, "too narrow") {
		t.Errorf("width 20 should show the too-narrow message, got:\n%s", out)
	}
	m = gridTestModel(44, 24)
	if out := m.View(); strings.Contains(out, "too narrow") {
		t.Error("width 44 should render the grid, not the too-narrow message")
	}
}

func TestPreviewSkippedAtPhoneWidth(t *testing.T) {
	m := gridTestModel(44, 24)
	if cmd := m.fetchPreview(); cmd != nil {
		t.Error("fetchPreview should be a no-op below MobileMaxWidth")
	}
	m = gridTestModel(120, 40)
	if cmd := m.fetchPreview(); cmd == nil {
		t.Error("fetchPreview should still run on desktop width")
	}
}

func TestGridKeysMoveCursor(t *testing.T) {
	m := gridTestModel(44, 24) // 2 cols; targets: my-app, scry (running), dotfiles (dormant)
	targets := m.gridTargets()
	if len(targets) != 3 {
		t.Fatalf("setup: got %d targets, want 3", len(targets))
	}

	m.handleGridKey(keyMsg("l"))
	if m.gridCursor != targets[1].Label {
		t.Errorf("after l: cursor = %q, want %q", m.gridCursor, targets[1].Label)
	}

	m.handleGridKey(keyMsg("j"))
	if m.gridCursor != targets[2].Label {
		t.Errorf("after j onto short final row: cursor = %q, want %q", m.gridCursor, targets[2].Label)
	}

	m.handleGridKey(keyMsg("k"))
	if m.gridCursor != targets[0].Label {
		t.Errorf("after k: cursor = %q, want %q", m.gridCursor, targets[0].Label)
	}

	m.handleGridKey(keyMsg("h"))
	if m.gridCursor != targets[0].Label {
		t.Errorf("h at start should clamp, cursor = %q", m.gridCursor)
	}
}

func TestGridCursorKeepsSessionsCursorAligned(t *testing.T) {
	m := gridTestModel(120, 40)
	m.handleGridKey(keyMsg("l")) // move to the second running target
	targets := m.gridTargets()
	idx := resolveGridCursor(targets, m.gridCursor, m.gridIndex)
	if got := m.selectedSessionName(); got != targets[idx].Label {
		t.Errorf("sessions cursor out of sync: selectedSessionName = %q, grid cursor = %q",
			got, targets[idx].Label)
	}
}

func TestGridEnterReturnsCommandForRunningAndDormant(t *testing.T) {
	m := gridTestModel(120, 40)
	targets := m.gridTargets()

	m.setGridCursor(targets, 0) // running
	if cmd := m.enterTarget(targets, 0); cmd == nil {
		t.Error("Enter on a running target should return a switch cmd")
	}

	dormant := len(targets) - 1
	if targets[dormant].Running() {
		t.Fatalf("setup: target %d should be dormant", dormant)
	}
	if cmd := m.enterTarget(targets, dormant); cmd == nil {
		t.Error("Enter on a dormant repo should return a jump cmd")
	}
}

func TestGridEnterOnEmptyGridIsSafe(t *testing.T) {
	m := gridTestModel(44, 24)
	m.sessions.Sessions = nil
	m.repos.Repos = nil
	if cmd := m.enterTarget(m.gridTargets(), 0); cmd != nil {
		t.Error("Enter on an empty grid should return nil, not a cmd")
	}
}

func TestGridDashboardToggle(t *testing.T) {
	m := gridTestModel(120, 40)
	m.handleGridKey(keyMsg("d"))
	if m.view != ViewDashboard {
		t.Errorf("d in grid view should switch to dashboard, view = %v", m.view)
	}
	m.handleNavKey(keyMsg("d"))
	if m.view != ViewGrid {
		t.Errorf("d in dashboard view should switch back to grid, view = %v", m.view)
	}
}

func TestGridQuitAndRefresh(t *testing.T) {
	m := gridTestModel(120, 40)
	if cmd := m.handleGridKey(keyMsg("q")); cmd == nil {
		t.Error("q should return a quit cmd")
	}
	if cmd := m.handleGridKey(keyMsg("r")); cmd == nil {
		t.Error("r should return a refresh cmd")
	}
}

func TestGridSearchKeyOpensOverlay(t *testing.T) {
	m := gridTestModel(120, 40)
	m.handleGridKey(keyMsg("/"))
	if m.mode != ModeSearch {
		t.Errorf("/ should enter search mode, mode = %v", m.mode)
	}
}

func TestProcessIndicator(t *testing.T) {
	cases := []struct {
		name  string
		infos []sources.ProcessInfo
		want  string
	}{
		{"none", nil, ""},
		{"all running", []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessRunning, Configured: true},
		}, "⚙ 1/1"},
		{"one dead", []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessRunning, Configured: true},
			{Name: "test", State: sources.ProcessDead, Configured: true},
		}, "⚙ 1/2"},
		{"not started counts against the total", []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessNotStarted, Configured: true},
		}, "⚙ 0/1"},
		{"unconfigured windows ignored", []sources.ProcessInfo{
			{Name: "shell", State: sources.ProcessRunning, Configured: false},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processIndicator(tc.infos); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestProcessIndicatorDegraded(t *testing.T) {
	dead := []sources.ProcessInfo{{Name: "dev", State: sources.ProcessDead, Configured: true}}
	if !processIndicatorDegraded(dead) {
		t.Error("a dead process must mark the tile degraded")
	}
	healthy := []sources.ProcessInfo{{Name: "dev", State: sources.ProcessRunning, Configured: true}}
	if processIndicatorDegraded(healthy) {
		t.Error("a running process is not degraded")
	}
	notStarted := []sources.ProcessInfo{{Name: "dev", State: sources.ProcessNotStarted, Configured: true}}
	if processIndicatorDegraded(notStarted) {
		t.Error("a process that was never started is idle, not degraded")
	}
}

func TestAttachProcesses(t *testing.T) {
	targets := []Target{{Label: "app"}, {Label: "other"}}
	byLabel := map[string][]sources.ProcessInfo{
		"app": {{Name: "dev", State: sources.ProcessRunning, Configured: true}},
	}
	got := AttachProcesses(targets, byLabel)
	if len(got[0].Processes) != 1 {
		t.Errorf("app should carry its processes: %+v", got[0])
	}
	if got[1].Processes != nil {
		t.Errorf("a target with no processes should stay empty: %+v", got[1])
	}
}

func TestRenderTileShowsProcessIndicator(t *testing.T) {
	target := Target{
		Label:   "app",
		Session: &sources.TmuxSession{Name: "app"},
		Processes: []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessRunning, Configured: true},
		},
	}
	out := renderTile(target, 24, false)
	if !strings.Contains(out, "⚙") {
		t.Errorf("a wide tile should show the process indicator:\n%s", out)
	}
}

func TestRenderTileKeepsHeightWithProcesses(t *testing.T) {
	target := Target{
		Label:   "app",
		Session: &sources.TmuxSession{Name: "app"},
		Repo:    &sources.GitRepoStatus{Label: "app", Branch: "main"},
		Processes: []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessDead, Configured: true},
		},
	}
	out := renderTile(target, 18, false)
	if got := lipgloss.Height(out); got != gridTileH {
		t.Errorf("tile height = %d, want %d — the indicator must not add a line:\n%s", got, gridTileH, out)
	}
}

func TestRenderTileShowsNeedsInput(t *testing.T) {
	s := sess("app")
	s.Status, s.StatusReported = sources.AgentStatusNeedsInput, true
	out := renderTile(Target{Label: "app", Session: &s, Status: sources.AgentStatusNeedsInput, StatusReported: true}, 22, false)

	if !strings.Contains(out, "needs you") {
		t.Errorf("a blocked agent must say so:\n%s", out)
	}
}

func TestRenderTileKeepsExistenceOnShape(t *testing.T) {
	// Shape carries existence; confidence is carried by dimming. A reported
	// status must not change the marker glyph.
	s := sess("app")
	s.Status, s.StatusReported = sources.AgentStatusWorking, true
	out := renderTile(Target{Label: "app", Session: &s, Status: sources.AgentStatusWorking, StatusReported: true}, 22, false)

	if !strings.Contains(out, "●") || strings.Contains(out, "○") {
		t.Errorf("a live session keeps the filled marker:\n%s", out)
	}
}

func TestBuildTargetsCarriesWhetherStatusWasReported(t *testing.T) {
	reported := sess("app")
	reported.Status, reported.StatusReported = sources.AgentStatusWorking, true
	guessed := sess("docs")

	targets := BuildTargets(
		[]sources.TmuxSession{reported, guessed}, nil,
		map[string]sources.AgentStatus{"app": sources.AgentStatusWorking, "docs": sources.AgentStatusWorking},
		"cockpit")

	byLabel := map[string]Target{}
	for _, t := range targets {
		byLabel[t.Label] = t
	}
	if !byLabel["app"].StatusReported {
		t.Error("app's status came from a hook and must say so")
	}
	if byLabel["docs"].StatusReported {
		t.Error("docs' status is a guess and must not claim otherwise")
	}
}

func TestBuildTargetsKeepsSameLabelOnTwoHostsApart(t *testing.T) {
	local := sess("docket")
	remote := sess("docket")
	remote.Host = "mini"

	targets := BuildTargets([]sources.TmuxSession{local, remote}, nil, nil, "cockpit")

	if len(targets) != 2 || targets[0].Key() == targets[1].Key() {
		t.Fatalf("want two distinct tiles, got %v", labels(targets))
	}
}

func TestBuildTargetsJoinsRemoteRepoOnHost(t *testing.T) {
	// A remote session and a remote repo with the same label are one tile; a
	// local repo with that label is a different one.
	remote := sess("docket")
	remote.Host = "mini"
	remoteRepo := repo("docket")
	remoteRepo.Host = "mini"
	localRepo := repo("docket")

	targets := BuildTargets([]sources.TmuxSession{remote}, []sources.GitRepoStatus{remoteRepo, localRepo}, nil, "cockpit")

	if len(targets) != 2 {
		t.Fatalf("want a joined remote tile and a dormant local one, got %v", labels(targets))
	}
	if targets[0].Repo == nil || targets[0].Repo.Host != "mini" {
		t.Errorf("remote session must join the remote repo, got %+v", targets[0].Repo)
	}
}

func TestBuildTargetsExcludesTheCockpitSessionPerHost(t *testing.T) {
	remoteCockpit := sess("cockpit")
	remoteCockpit.Host = "mini"
	if got := BuildTargets([]sources.TmuxSession{sess("cockpit"), remoteCockpit}, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("cockpit's own session is excluded on every host, got %v", labels(got))
	}
}

func TestBuildTargetsExcludesViewSessions(t *testing.T) {
	view := sess("mini")
	view.ViewOf = "mini"
	if got := BuildTargets([]sources.TmuxSession{view}, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("a local view of a remote host is not a project, got %v", labels(got))
	}
}

func TestRenderTileShowsHostPrefix(t *testing.T) {
	s := sess("docket")
	s.Host = "mini"
	out := renderTile(Target{Label: "docket", Host: "mini", Session: &s}, 22, false)
	if !strings.Contains(out, "mini/") {
		t.Errorf("remote tile must name its host:\n%s", out)
	}
}

func TestRenderTileShowsUnreachableOverLastKnownData(t *testing.T) {
	s := sess("docket")
	s.Host = "mini"
	r := repo("docket")
	r.Host = "mini"
	out := renderTile(Target{Label: "docket", Host: "mini", Session: &s, Repo: &r, Unreachable: true}, 22, false)

	if !strings.Contains(out, "unreachable") {
		t.Errorf("a dead link must be named:\n%s", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("last-known branch must still show:\n%s", out)
	}
}

func hermesTarget(st sources.HermesStatus) Target {
	st.Label = "hermes"
	return Target{Label: "hermes", Hermes: &st}
}

func TestRenderTileHermesRunning(t *testing.T) {
	out := renderTile(hermesTarget(sources.HermesStatus{Reachable: true, Gateway: "running", Platforms: []string{"photon", "slack"}}), 22, false)
	if !strings.Contains(out, "gateway") || !strings.Contains(out, "photon") {
		t.Errorf("want gateway state and platforms:\n%s", out)
	}
}

func TestRenderTileHermesStoppedAndUnreachableDiffer(t *testing.T) {
	stopped := renderTile(hermesTarget(sources.HermesStatus{Reachable: true, Gateway: "stopped"}), 22, false)
	down := renderTile(hermesTarget(sources.HermesStatus{Reachable: false}), 22, false)
	if !strings.Contains(stopped, "stopped") {
		t.Errorf("stopped must say stopped:\n%s", stopped)
	}
	if !strings.Contains(down, "unreachable") {
		t.Errorf("unreachable must say unreachable:\n%s", down)
	}
}

func TestBuildTargetsPlacesHermesAfterRunningBeforeDormant(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("zzz-running")},
		[]sources.GitRepoStatus{repo("aaa-dormant")},
		nil, "cockpit",
		sources.HermesStatus{Label: "hermes", Reachable: true, Gateway: "running"})

	got := labels(targets)
	if len(got) != 3 || got[0] != "zzz-running" || got[1] != "hermes" || got[2] != "aaa-dormant" {
		t.Errorf("order = %v", got)
	}
}
