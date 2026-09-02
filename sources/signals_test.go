package sources

import (
	"testing"
	"time"

	"github.com/jhoot/cockpit/config"
)

func TestComputeSignalsStaleSession(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	in := SignalInput{
		Config: config.SignalsConfig{StaleSessionThreshold: "24h", ShowStaleSessions: true},
		Sessions: []TmuxSession{
			{Name: "old", LastUsed: now.Add(-48 * time.Hour)},
			{Name: "fresh", LastUsed: now.Add(-1 * time.Hour)},
			{Name: "attached-old", LastUsed: now.Add(-48 * time.Hour), Attached: true},
		},
		Now: now,
	}

	got := ComputeSignals(in)

	if len(got) != 1 {
		t.Fatalf("want only the stale detached session, got %+v", got)
	}
	if got[0].Subject != "old" || got[0].Kind != SignalStaleSession {
		t.Errorf("got %+v", got[0])
	}
}

func TestComputeSignalsStaleSessionsSuppressed(t *testing.T) {
	now := time.Now()
	in := SignalInput{
		Config:   config.SignalsConfig{StaleSessionThreshold: "24h", ShowStaleSessions: false},
		Sessions: []TmuxSession{{Name: "old", LastUsed: now.Add(-48 * time.Hour)}},
		Now:      now,
	}
	if got := ComputeSignals(in); len(got) != 0 {
		t.Errorf("show_stale_sessions = false must suppress them: %+v", got)
	}
}

func TestComputeSignalsInvalidThresholdFallsBack(t *testing.T) {
	now := time.Now()
	in := SignalInput{
		Config:   config.SignalsConfig{StaleSessionThreshold: "banana", ShowStaleSessions: true},
		Sessions: []TmuxSession{{Name: "old", LastUsed: now.Add(-48 * time.Hour)}},
		Now:      now,
	}
	if got := ComputeSignals(in); len(got) != 1 {
		t.Errorf("an unparseable threshold should fall back to 24h, not drop the signal: %+v", got)
	}
}

func TestComputeSignalsUnpushed(t *testing.T) {
	in := SignalInput{
		Config: config.SignalsConfig{ShowUnpushed: true},
		Git: []GitRepoStatus{
			{Label: "app", Unpushed: 3},
			{Label: "clean", Unpushed: 0},
		},
		Now: time.Now(),
	}

	got := ComputeSignals(in)

	if len(got) != 1 || got[0].Kind != SignalUnpushed || got[0].Subject != "app" {
		t.Fatalf("want one unpushed signal for app, got %+v", got)
	}
	if got[0].Detail != "3 unpushed commits" {
		t.Errorf("detail = %q", got[0].Detail)
	}
}

func TestComputeSignalsSkipsReposWithGitErrors(t *testing.T) {
	in := SignalInput{
		Config: config.SignalsConfig{ShowUnpushed: true},
		Git:    []GitRepoStatus{{Label: "broken", Unpushed: 5, Error: errNotARepo}},
		Now:    time.Now(),
	}
	if got := ComputeSignals(in); len(got) != 0 {
		t.Errorf("a repo we could not read is not a repo with unpushed work: %+v", got)
	}
}

func TestComputeSignalsFailingCI(t *testing.T) {
	in := SignalInput{
		Config: config.SignalsConfig{ShowFailingCI: true},
		GitHub: &GitHubStatus{RepoChecks: []RepoCheck{
			{RepoLabel: "app", CIStatus: "failing"},
			{RepoLabel: "ok", CIStatus: "passing"},
			{RepoLabel: "wait", CIStatus: "pending"},
		}},
		Now: time.Now(),
	}

	got := ComputeSignals(in)

	if len(got) != 1 || got[0].Kind != SignalFailingCI || got[0].Subject != "app" {
		t.Fatalf("only a failing check is a signal, got %+v", got)
	}
}

func TestComputeSignalsDeadProcess(t *testing.T) {
	in := SignalInput{
		Processes: map[string][]ProcessInfo{
			"app": {
				{Name: "dev", State: ProcessDead, Configured: true},
				{Name: "test", State: ProcessRunning, Configured: true},
				{Name: "scratch", State: ProcessDead, Configured: false},
			},
		},
		Now: time.Now(),
	}

	got := ComputeSignals(in)

	if len(got) != 1 {
		t.Fatalf("want one dead-process signal, got %+v", got)
	}
	if got[0].Kind != SignalDeadProcess || got[0].Subject != "app/dev" {
		t.Errorf("got %+v", got[0])
	}
}

func TestComputeSignalsOrdering(t *testing.T) {
	now := time.Now()
	in := SignalInput{
		Config: config.SignalsConfig{
			StaleSessionThreshold: "24h",
			ShowStaleSessions:     true,
			ShowUnpushed:          true,
			ShowFailingCI:         true,
		},
		Sessions:  []TmuxSession{{Name: "old", LastUsed: now.Add(-48 * time.Hour)}},
		Git:       []GitRepoStatus{{Label: "app", Unpushed: 1}},
		GitHub:    &GitHubStatus{RepoChecks: []RepoCheck{{RepoLabel: "app", CIStatus: "failing"}}},
		Processes: map[string][]ProcessInfo{"app": {{Name: "dev", State: ProcessDead, Configured: true}}},
		Now:       now,
	}

	got := ComputeSignals(in)

	want := []SignalKind{SignalDeadProcess, SignalFailingCI, SignalUnpushed, SignalStaleSession}
	if len(got) != len(want) {
		t.Fatalf("want %d signals, got %d: %+v", len(want), len(got), got)
	}
	for i, kind := range want {
		if got[i].Kind != kind {
			t.Errorf("signal %d = %s, want %s — urgency order must be stable", i, got[i].Kind, kind)
		}
	}
}

func TestComputeSignalsDeterministicAcrossRuns(t *testing.T) {
	in := SignalInput{
		Processes: map[string][]ProcessInfo{
			"b-app": {{Name: "dev", State: ProcessDead, Configured: true}},
			"a-app": {{Name: "dev", State: ProcessDead, Configured: true}},
		},
		Now: time.Now(),
	}

	first := ComputeSignals(in)
	for range 20 {
		got := ComputeSignals(in)
		for i := range got {
			if got[i].Subject != first[i].Subject {
				t.Fatalf("map iteration order leaked into output: %+v vs %+v", got, first)
			}
		}
	}
	if first[0].Subject != "a-app/dev" {
		t.Errorf("want alphabetical subjects, got %+v", first)
	}
}

var errNotARepo = errStr("not a git repository")

type errStr string

func (e errStr) Error() string { return string(e) }

func TestUnpushedDetailIsSingularForOneCommit(t *testing.T) {
	in := SignalInput{
		Config: config.SignalsConfig{ShowUnpushed: true},
		Git:    []GitRepoStatus{{Label: "app", Unpushed: 1}},
		Now:    time.Now(),
	}
	if got := ComputeSignals(in)[0].Detail; got != "1 unpushed commit" {
		t.Errorf("detail = %q, want the singular form", got)
	}
}

func TestBlockedAgentOutranksEveryOtherSignal(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	in := SignalInput{
		Config:   config.SignalsConfig{ShowUnpushed: true},
		Sessions: []TmuxSession{{Name: "app", Status: AgentStatusNeedsInput, StatusReported: true}},
		Git:      []GitRepoStatus{{Label: "other", Unpushed: 3}},
		Processes: map[string][]ProcessInfo{
			"other": {{Name: "dev", Configured: true, State: ProcessDead}},
		},
		Now: now,
	}

	got := ComputeSignals(in)

	if len(got) < 2 {
		t.Fatalf("want the agent and the other signals, got %+v", got)
	}
	if got[0].Kind != SignalBlockedAgent || got[0].Subject != "app" {
		t.Errorf("a blocked agent must sort above a dead process and an unpushed commit, got %+v", got[0])
	}
}

func TestBlockedAgentSignalNeedsAReport(t *testing.T) {
	// The pane-hash guess cannot see this state, so an unreported needs_input
	// is a contradiction and must not become a signal.
	in := SignalInput{
		Sessions: []TmuxSession{{Name: "app", Status: AgentStatusNeedsInput}},
		Now:      time.Now(),
	}
	if got := ComputeSignals(in); len(got) != 0 {
		t.Errorf("want no signal from an unreported status, got %+v", got)
	}
}

func TestStoppedHermesSignalsButUnreachableDoesNot(t *testing.T) {
	in := SignalInput{
		Hermes: []HermesStatus{
			{Label: "hermes", Reachable: true, Gateway: "stopped"},
			{Label: "other", Reachable: false},
		},
		Now: time.Now(),
	}
	got := ComputeSignals(in)
	if len(got) != 1 || got[0].Kind != SignalHermesDown || got[0].Subject != "hermes" {
		t.Errorf("want one signal for the stopped gateway only, got %+v", got)
	}
}
