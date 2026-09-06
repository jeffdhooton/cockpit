package sources

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

func freshHost(host string, now time.Time) HostReport {
	return HostReport{Host: host, Outcome: ObservationFresh, ObservedAt: now}
}

func pane(session, sid, wid, pid, inv string, st AgentStatus, fresh bool) PaneReport {
	return PaneReport{Session: session, SessionID: sid, WindowID: wid, PaneID: pid, Invocation: inv,
		Generation: "g1", Status: st, Reported: fresh, Recorded: true, WindowName: "agent"}
}

func TestTwoBlockedPanesAreTwoItemsAndAThirdClearsNeither(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := freshHost("", now)
	h.Sessions = []TmuxSession{{Name: "app", ID: "$1", Generation: "g1"}}
	h.Panes = []PaneReport{
		pane("app", "$1", "@1", "%1", "inv-a", AgentStatusNeedsInput, true),
		pane("app", "$1", "@2", "%2", "inv-b", AgentStatusNeedsInput, true),
		pane("app", "$1", "@3", "%3", "inv-c", AgentStatusIdle, true),
	}
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if rep.Actionable() != 2 {
		t.Fatalf("want 2 actionable items, got %+v", rep.Items)
	}
	ids := map[string]bool{}
	for _, i := range rep.Items {
		ids[i.ID] = true
		if i.Target.PaneID == "" || i.Action != ActionOpenAgent {
			t.Errorf("item must target its pane: %+v", i)
		}
	}
	if !ids["needs_input:/g1/%1:inv-a"] || !ids["needs_input:/g1/%2:inv-b"] {
		t.Errorf("ids = %v", ids)
	}

	// The same pane reporting working under the same invocation clears it;
	// the other pane's item remains.
	h.Panes[0].Status = AgentStatusWorking
	rep = DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if rep.Actionable() != 1 || rep.Items[0].Target.PaneID != "%2" {
		t.Errorf("after pane 1 resumed: %+v", rep.Items)
	}
}

func TestSameLabelOnTwoHostsIsTwoItems(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	local := freshHost("", now)
	local.Panes = []PaneReport{pane("api", "$1", "@1", "%1", "a", AgentStatusNeedsInput, true)}
	remote := freshHost("mini", now)
	remote.Host = "mini"
	remote.Panes = []PaneReport{pane("api", "$1", "@1", "%1", "a", AgentStatusNeedsInput, true)}
	remote.Panes[0].Host = "mini"
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{local, remote}, Now: now})
	if len(rep.Items) != 2 || rep.Items[0].ID == rep.Items[1].ID {
		t.Fatalf("items = %+v", rep.Items)
	}
	if rep.Items[1].Target.Host != "mini" || rep.Items[1].Project != "mini/api" || rep.Items[0].Project != "api" {
		t.Errorf("routes must carry the host: %+v", rep.Items)
	}
}

func TestLegacySessionReportIsLimitedCoverage(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := freshHost("", now)
	h.Sessions = []TmuxSession{
		{Name: "old", ID: "$1", Generation: "g1", Status: AgentStatusNeedsInput, StatusReported: true, StatusSource: StatusSourceSession},
		{Name: "new", ID: "$2", Generation: "g1", Status: AgentStatusNeedsInput, StatusReported: true, StatusSource: StatusSourceSession},
	}
	// "new" also has a pane record, so its session-level value is ignored.
	h.Panes = []PaneReport{pane("new", "$2", "@1", "%1", "a", AgentStatusIdle, true)}
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if len(rep.Items) != 1 || rep.Items[0].Action != ActionOpenSession || rep.Items[0].Coverage == "" {
		t.Fatalf("items = %+v", rep.Items)
	}
	found := false
	for _, c := range rep.Coverage {
		if c.Source == SourceHook && strings.Contains(c.Detail, "session level") {
			found = true
		}
	}
	if !found {
		t.Errorf("legacy coverage must be visible: %+v", rep.Coverage)
	}
}

func TestExpiredPaneRecordIsNotHealthyAndNotAnItem(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := freshHost("", now)
	h.Sessions = []TmuxSession{{Name: "app", ID: "$1", StatusSource: StatusSourcePane}}
	h.Panes = []PaneReport{pane("app", "$1", "@1", "%1", "a", AgentStatusNeedsInput, false)}
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if len(rep.Items) != 0 {
		t.Errorf("an expired record is not a fresh prompt: %+v", rep.Items)
	}
}

func TestUnavailableHostKeepsLastKnownItemsAsStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := HostReport{Host: "mini", Outcome: ObservationUnavailable, Err: "host unreachable", ObservedAt: now.Add(-3 * time.Minute)}
	h.Panes = []PaneReport{pane("app", "$1", "@1", "%1", "a", AgentStatusNeedsInput, true)}
	h.Processes = map[string]ProcessObservation{"mini/app": {Session: "app", Processes: []ProcessInfo{
		{Name: "dev", Configured: true, Outcome: OutcomeExited, WindowID: "@2", Generation: "g1"},
	}}}
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if len(rep.Items) != 2 {
		t.Fatalf("last-known items must be retained: %+v", rep.Items)
	}
	for _, i := range rep.Items {
		if i.Observation != ObservationStale || i.Actionable() {
			t.Errorf("a stale item cannot act: %+v", i)
		}
	}
	if rep.Actionable() != 0 || rep.Unavailable != 1 {
		t.Errorf("badge %d unavailable %d", rep.Actionable(), rep.Unavailable)
	}
	// The unavailable host does not produce one failure per cached process.
	kinds := map[AttentionKind]int{}
	for _, i := range rep.Items {
		kinds[i.Kind]++
	}
	if kinds[AttentionProcessExited] != 1 {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestProcessItemsSeparateCrashFromCompletionAndStop(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := freshHost("", now)
	h.Processes = map[string]ProcessObservation{"site": {Session: "site", ObservedAt: now, Processes: []ProcessInfo{
		{Name: "dev", Configured: true, Outcome: OutcomeExited, Display: "Exited (code 1)", WindowID: "@1", Generation: "g1"},
		{Name: "build", Configured: true, Outcome: OutcomeCompleted, WindowID: "@2"},
		{Name: "worker", Configured: true, Outcome: OutcomeExited, DesiredState: DesiredStopped, WindowID: "@3"},
		{Name: "shell", Outcome: OutcomeExited},
	}}}
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if len(rep.Items) != 1 || rep.Items[0].Target.Process != "dev" || rep.Items[0].Action != ActionInspectProcess {
		t.Fatalf("items = %+v", rep.Items)
	}
	if rep.Items[0].ID != "process:site:dev:g1/@1" {
		t.Errorf("id must carry the window identity: %s", rep.Items[0].ID)
	}
}

func TestCIItemsCarryRunIdentityAndBranchCoverage(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	gh := &GitHubStatus{RepoChecks: []RepoCheck{
		{RepoLabel: "site", CIStatus: "failing", Coverage: CoverageChecked, Repo: "me/site", Branch: "main", RunID: "42", RunURL: "https://github.com/me/site/actions/runs/42"},
		{RepoLabel: "docket", CIStatus: "none", Coverage: CoverageRemoteUnsupported},
		{RepoLabel: "broken", CIStatus: "none", Coverage: CoverageError, Err: errors.New("gh: not logged in")},
	}}
	rep := DeriveAttention(AttentionInput{Config: config.SignalsConfig{ShowFailingCI: true}, GitHub: gh, GitHubAt: now, Now: now})
	if len(rep.Items) != 1 || rep.Items[0].Target.RunID != "42" || rep.Items[0].Target.Branch != "main" || rep.Items[0].ID != "ci:site:42" {
		t.Fatalf("items = %+v", rep.Items)
	}
	cov := rep.Coverage[0]
	if cov.Source != SourceGitHub || !strings.Contains(cov.Detail, "1 repo checked on main") ||
		!strings.Contains(cov.Detail, "1 remote not checked") || !strings.Contains(cov.Detail, "1 unreadable") {
		t.Errorf("coverage = %+v", cov)
	}
}

func TestGitHubReadFailureIsUnavailableNotPassing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	gh := &GitHubStatus{Error: errors.New("gh not installed")}
	rep := DeriveAttention(AttentionInput{Config: config.SignalsConfig{ShowFailingCI: true}, GitHub: gh, GitHubAt: now, Now: now})
	if rep.Unavailable != 1 || rep.Coverage[0].Observation != ObservationUnavailable {
		t.Errorf("coverage = %+v", rep.Coverage)
	}
}

func TestHermesUnreachableIsCoverageNotAnItem(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rep := DeriveAttention(AttentionInput{Hermes: []HermesStatus{
		{Label: "h1", Reachable: false, Err: errors.New("timeout")},
		{Label: "h2", Reachable: true, Gateway: "stopped", Host: "mini"},
	}, HermesAt: now, Now: now})
	if len(rep.Items) != 1 || rep.Items[0].Kind != AttentionHermesDown || rep.Items[0].Target.Host != "mini" {
		t.Errorf("items = %+v", rep.Items)
	}
	if rep.Unavailable != 1 {
		t.Errorf("an unreachable dashboard is unknown coverage: %+v", rep.Coverage)
	}
}

func TestHousekeepingDoesNotInflateTheBadge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := freshHost("", now)
	h.Git = []GitRepoStatus{{Label: "site", Unpushed: 2}, {Label: "err", Unpushed: 9, Error: errors.New("x")}}
	h.Sessions = []TmuxSession{{Name: "old", ID: "$1", LastUsed: now.Add(-48 * time.Hour)}}
	rep := DeriveAttention(AttentionInput{
		Config: config.SignalsConfig{ShowUnpushed: true, ShowStaleSessions: true, StaleSessionThreshold: "24h"},
		Hosts:  []HostReport{h}, Now: now,
	})
	if len(rep.Items) != 2 || rep.Actionable() != 0 {
		t.Errorf("items = %+v badge %d", rep.Items, rep.Actionable())
	}
}

func TestTrackerKeepsFirstObservedAndSortsStably(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var tr AttentionTracker
	h := freshHost("", now)
	h.Panes = []PaneReport{pane("b", "$2", "@2", "%2", "b", AgentStatusNeedsInput, true)}
	rep := DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	tr.Track(&rep, now)

	// A newer, otherwise identical item arrives: it sorts after the old one.
	h.Panes = append(h.Panes, pane("a", "$1", "@1", "%1", "a", AgentStatusNeedsInput, true))
	rep = DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	tr.Track(&rep, now.Add(time.Minute))
	if rep.Items[0].Target.PaneID != "%2" || !rep.Items[0].FirstObserved.Equal(now) {
		t.Errorf("first seen must lead: %+v", rep.Items)
	}
	// Repeated polls do not duplicate.
	rep = DeriveAttention(AttentionInput{Hosts: []HostReport{h}, Now: now})
	tr.Track(&rep, now.Add(2*time.Minute))
	if len(rep.Items) != 2 || len(tr.first) != 2 {
		t.Errorf("duplicates: %+v", rep.Items)
	}
}

func TestSignalsAdaptTheSameFacts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	in := SignalInput{
		Config:   config.SignalsConfig{ShowUnpushed: true, ShowFailingCI: true},
		Sessions: []TmuxSession{{Name: "app", ID: "$1"}},
		Panes:    []PaneReport{pane("app", "$1", "@1", "%1", "a", AgentStatusNeedsInput, true)},
		GitHub:   &GitHubStatus{RepoChecks: []RepoCheck{{RepoLabel: "site", CIStatus: "failing", Coverage: CoverageChecked, Branch: "main", RunID: "7"}}},
		Hermes:   []HermesStatus{{Label: "h", Reachable: true, Gateway: "stopped"}},
		Now:      now,
	}
	got := ComputeSignals(in)
	want := []SignalKind{SignalBlockedAgent, SignalHermesDown, SignalFailingCI}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, k := range want {
		if got[i].Kind != k {
			t.Errorf("signal %d = %+v, want %s", i, got[i], k)
		}
	}
	if got[0].Subject != "app" || got[2].Detail != "checks failing on main" {
		t.Errorf("got %+v", got)
	}
}

func TestFilterAttentionMatchesHostProjectAndKind(t *testing.T) {
	items := []AttentionItem{
		{ID: "1", Host: "mini", Project: "mini/api", Kind: AttentionNeedsInput, Title: "mini/api"},
		{ID: "2", Project: "site", Kind: AttentionProcessExited, Title: "local/site · dev"},
	}
	if got := FilterAttention(items, "MINI"); len(got) != 1 || got[0].ID != "1" {
		t.Errorf("host filter: %+v", got)
	}
	if got := FilterAttention(items, "process_exited"); len(got) != 1 || got[0].ID != "2" {
		t.Errorf("kind filter: %+v", got)
	}
	if got := FilterAttention(items, "local"); len(got) != 1 || got[0].ID != "2" {
		t.Errorf("local host filter: %+v", got)
	}
}
