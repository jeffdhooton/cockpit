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
	statuses := map[string]sources.ClaudeStatus{"my-app": sources.ClaudeStatusWorking}
	targets := BuildTargets([]sources.TmuxSession{sess("my-app")}, nil, statuses, "cockpit")
	if targets[0].Status != sources.ClaudeStatusWorking {
		t.Errorf("Status = %v, want Working", targets[0].Status)
	}
}

func TestBuildTargetsEmpty(t *testing.T) {
	if got := BuildTargets(nil, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("got %d targets from empty inputs, want 0", len(got))
	}
}

func TestGridCols(t *testing.T) {
	cases := []struct{ width, want int }{
		{30, 1},  // very narrow phone pane
		{44, 2},  // Termius vertical
		{70, 3},
		{120, 4}, // capped
		{200, 4}, // capped
	}
	for _, c := range cases {
		if got := GridCols(c.width); got != c.want {
			t.Errorf("GridCols(%d) = %d, want %d", c.width, got, c.want)
		}
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
