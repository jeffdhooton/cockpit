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
