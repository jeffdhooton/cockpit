package tui

import (
	"testing"

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
