package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

func TestBackoffStepsAndCaps(t *testing.T) {
	want := []time.Duration{5 * time.Second, 30 * time.Second, 60 * time.Second, 60 * time.Second}
	for failures, d := range want {
		if got := backoffDelay(failures + 1); got != d {
			t.Errorf("after %d failures: %v, want %v", failures+1, got, d)
		}
	}
}

func TestMergeHostKeepsLastDataWhenUnreachable(t *testing.T) {
	prev := hostState{poll: hostPoll{Host: "mini", Sessions: []sources.TmuxSession{{Name: "docket", Host: "mini"}}}}

	got := mergeHost(prev, hostPoll{Host: "mini", Err: sources.ErrHostUnreachable})

	if !got.unreachable || got.failures != 1 {
		t.Errorf("want unreachable after one failure, got %+v", got)
	}
	if len(got.poll.Sessions) != 1 {
		t.Error("last-known sessions must survive a failed poll, so the grid never goes blank")
	}
}

func TestMergeHostSuccessReplacesDataAndResetsBackoff(t *testing.T) {
	prev := hostState{failures: 3, unreachable: true,
		poll: hostPoll{Host: "mini", Sessions: []sources.TmuxSession{{Name: "old", Host: "mini"}}}}

	got := mergeHost(prev, hostPoll{Host: "mini", Sessions: []sources.TmuxSession{{Name: "new", Host: "mini"}}})

	if got.unreachable || got.failures != 0 {
		t.Errorf("a success clears unreachable and resets backoff, got %+v", got)
	}
	if len(got.poll.Sessions) != 1 || got.poll.Sessions[0].Name != "new" {
		t.Errorf("a success replaces the data, got %+v", got.poll.Sessions)
	}
}

func TestMergeHostNonTransportErrorIsNotUnreachable(t *testing.T) {
	// A remote tmux that is missing is a remote problem the tile can name.
	// It is not a dead link, so it must not trigger backoff.
	got := mergeHost(hostState{}, hostPoll{Host: "mini", Err: errors.New("tmux: not found")})
	if got.unreachable || got.failures != 0 {
		t.Errorf("only ErrHostUnreachable backs off, got %+v", got)
	}
}

func TestGridTargetsIncludeRemoteHosts(t *testing.T) {
	m := Model{config: testConfig()}
	m.config.Hosts = []config.HostConfig{{Name: "mini", Tmux: "/opt/homebrew/bin/tmux"}}
	m.sessions.Sessions = []sources.TmuxSession{{Name: "local"}}
	m.hosts = map[string]hostState{"mini": {
		unreachable: true,
		poll: hostPoll{Host: "mini",
			Sessions: []sources.TmuxSession{{Name: "docket", Host: "mini", Status: sources.AgentStatusWorking, StatusReported: true}},
			Repos:    []sources.GitRepoStatus{{Label: "site", Host: "mini", Branch: "main"}},
		},
	}}

	targets := m.gridTargets()
	keys := labels(targets)
	if len(targets) != 2 {
		t.Fatalf("the root wants the local session and one box for mini, got %v", keys)
	}

	// The host's own tiles are one level down, inside its box.
	m.openHost("mini")
	targets = m.gridTargets()
	if len(targets) != 2 {
		t.Fatalf("want mini's session and its dormant repo, got %v", labels(targets))
	}
	var remote Target
	for _, tg := range targets {
		if tg.Key() == "mini/docket" {
			remote = tg
		}
	}
	if !remote.Unreachable {
		t.Error("tiles for an unreachable host must say so")
	}
	if remote.Status != sources.AgentStatusWorking || !remote.StatusReported {
		t.Errorf("a remote session's reported status must reach its tile, got %+v", remote)
	}
}
