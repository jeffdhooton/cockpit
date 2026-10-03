package sources

import (
	"strings"
	"testing"
	"time"
)

func wsHost(host string, now time.Time) HostReport {
	return HostReport{Host: host, Outcome: ObservationFresh, ObservedAt: now}
}

func wsPane(session, sid, wid, pid, cmd string) PaneReport {
	return PaneReport{Session: session, SessionID: sid, WindowID: wid, PaneID: pid, Generation: "g1", Command: cmd, Path: "/w/" + session, WindowName: cmd}
}

func TestWorkspaceClassifiesPanes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := wsHost("", now)
	h.Sessions = []TmuxSession{{Name: "app", ID: "$1", Generation: "g1", Windows: 4}}
	reported := wsPane("app", "$1", "@1", "%1", "claude")
	reported.Recorded, reported.Reported, reported.Status, reported.Invocation = true, true, AgentStatusNeedsInput, "inv"
	h.Panes = []PaneReport{
		reported,
		wsPane("app", "$1", "@2", "%2", "codex"), // no record: agent, unknown
		wsPane("app", "$1", "@3", "%3", "zsh"),
		wsPane("app", "$1", "@4", "%4", "node"),
	}
	h.Processes = map[string]ProcessObservation{"app": {Project: "app", Session: "app", SessionExists: true, Processes: []ProcessInfo{
		{Name: "dev", Configured: true, Managed: true, WindowID: "@4", PaneID: "%4", Outcome: OutcomeRunning, Display: "Running", DesiredState: DesiredRunning, State: ProcessRunning},
		{Name: "worker", Configured: true, Outcome: OutcomeNotStarted, Display: "Not started", WindowIndex: -1},
	}}}
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{h}, Now: now})
	if len(ws.Hosts) != 1 || len(ws.Hosts[0].Sessions) != 1 {
		t.Fatalf("ws = %+v", ws)
	}
	s := ws.Hosts[0].Sessions[0]
	if s.Key != "app" || s.Status != "needs_input" || !s.Reported || s.Agents != 2 || s.ProcessesRunning != 1 || s.ProcessesTotal != 2 {
		t.Errorf("session = %+v", s)
	}
	kinds := map[string]PaneView{}
	for _, p := range s.Panes {
		kinds[p.PaneID] = p
	}
	if p := kinds["%1"]; p.Kind != PaneAgent || p.Agent == nil || p.Agent.Engine != "claude" || p.Agent.Status != "needs_input" || !p.Agent.Reported {
		t.Errorf("reported agent = %+v", p)
	}
	if p := kinds["%2"]; p.Kind != PaneAgent || p.Agent == nil || p.Agent.Status != "unknown" || p.Agent.Reported {
		t.Errorf("command-only agent must be unknown and unreported: %+v", p)
	}
	if p := kinds["%3"]; p.Kind != PaneShell {
		t.Errorf("zsh = %+v", p)
	}
	if p := kinds["%4"]; p.Kind != PaneProcess || p.Process == nil || p.Process.Name != "dev" || p.Process.Display != "Running" || !p.Process.Managed {
		t.Errorf("process pane = %+v", p)
	}
}

func TestWorkspaceNeverInfersNeedsInput(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := wsHost("", now)
	h.Sessions = []TmuxSession{{Name: "app", ID: "$1", Generation: "g1"}}
	stale := wsPane("app", "$1", "@1", "%1", "claude")
	stale.Recorded, stale.Reported, stale.Status = true, false, AgentStatusNeedsInput // expired record
	h.Panes = []PaneReport{stale}
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{h}, Now: now})
	p := ws.Hosts[0].Sessions[0].Panes[0]
	if p.Agent.Status != "unknown" || p.Agent.Reported || ws.Hosts[0].Sessions[0].Status != "unknown" {
		t.Errorf("an expired record is unknown: %+v", p)
	}
}

func TestWorkspaceSortsByRollupAndKeepsHostsApart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	local := wsHost("", now)
	local.Sessions = []TmuxSession{{Name: "zeta", ID: "$1", Generation: "g1"}, {Name: "alpha", ID: "$2", Generation: "g1"}, {Name: "beta", ID: "$3", Generation: "g1"}}
	working := wsPane("zeta", "$1", "@1", "%1", "codex")
	working.Recorded, working.Reported, working.Status = true, true, AgentStatusWorking
	blocked := wsPane("beta", "$3", "@1", "%9", "claude")
	blocked.Recorded, blocked.Reported, blocked.Status = true, true, AgentStatusNeedsInput
	local.Panes = []PaneReport{working, blocked, wsPane("alpha", "$2", "@1", "%5", "zsh")}
	remote := wsHost("mini", now)
	remote.Sessions = []TmuxSession{{Name: "alpha", ID: "$1", Generation: "r1", Host: "mini"}}
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{local, remote}, Now: now})
	var order []string
	for _, s := range ws.Hosts[0].Sessions {
		order = append(order, s.Name)
	}
	if got := strings.Join(order, ","); got != "beta,zeta,alpha" {
		t.Errorf("order = %s", got)
	}
	if ws.Hosts[1].Name != "mini" || ws.Hosts[1].Sessions[0].Key != "mini/alpha" || ws.Hosts[0].Sessions[2].Key != "alpha" {
		t.Errorf("hosts = %+v", ws.Hosts)
	}
}

func TestWorkspaceKeepsStaleHostAndListsDormant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := HostReport{Host: "mini", Outcome: ObservationUnavailable, Err: "host unreachable", ObservedAt: now.Add(-time.Minute)}
	h.Sessions = []TmuxSession{{Name: "api", ID: "$1", Generation: "g1", Host: "mini"}}
	h.Git = []GitRepoStatus{{Label: "api", Host: "mini", Branch: "main", DirtyCount: 3}, {Label: "docket", Host: "mini", Branch: "dev", Unpushed: 2}}
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{h}, Now: now})
	host := ws.Hosts[0]
	if host.Observation != ObservationStale || len(host.Sessions) != 1 || host.Err == "" {
		t.Errorf("host = %+v", host)
	}
	if host.Sessions[0].Project == nil || host.Sessions[0].Project.Dirty != 3 {
		t.Errorf("project summary = %+v", host.Sessions[0].Project)
	}
	if len(host.Dormant) != 1 || host.Dormant[0].Key != "mini/docket" || host.Dormant[0].Project.Unpushed != 2 {
		t.Errorf("dormant = %+v", host.Dormant)
	}
	never := HostReport{Host: "halo", Outcome: ObservationUnavailable, Err: "not polled yet"}
	ws = BuildWorkspace(AttentionInput{Hosts: []HostReport{never}, Now: now})
	if ws.Hosts[0].Observation != ObservationUnavailable || len(ws.Hosts[0].Sessions) != 0 {
		t.Errorf("never-polled host = %+v", ws.Hosts[0])
	}
}

func TestWorkspaceMarksAttentionPanesAndLegacyReports(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := wsHost("", now)
	h.Sessions = []TmuxSession{
		{Name: "app", ID: "$1", Generation: "g1"},
		{Name: "old", ID: "$2", Generation: "g1", Status: AgentStatusWorking, StatusReported: true, StatusSource: StatusSourceSession},
	}
	blocked := wsPane("app", "$1", "@1", "%1", "claude")
	blocked.Recorded, blocked.Reported, blocked.Status = true, true, AgentStatusNeedsInput
	h.Panes = []PaneReport{blocked, wsPane("old", "$2", "@1", "%2", "claude"), wsPane("app", "$1", "@7", "%7", "node")}
	h.Processes = map[string]ProcessObservation{"app": {Session: "app", SessionExists: true, Processes: []ProcessInfo{
		{Name: "dev", Configured: true, Managed: true, WindowID: "@7", PaneID: "%7", Outcome: OutcomeExited, Display: "Exited (code 1)", DesiredState: DesiredRunning},
	}}}
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{h}, Now: now, SelfSession: "cockpit"})
	by := map[string]SessionView{}
	for _, s := range ws.Hosts[0].Sessions {
		by[s.Name] = s
	}
	flagged := 0
	for _, p := range by["app"].Panes {
		if p.Attention {
			flagged++
		}
	}
	if flagged != 2 {
		t.Errorf("blocked agent and crashed process panes must be flagged, got %d", flagged)
	}
	if !by["old"].LegacyReport || by["old"].Status != "working" || !by["old"].Reported {
		t.Errorf("legacy session report must roll up with a flag: %+v", by["old"])
	}
	if len(ws.Coverage) == 0 {
		t.Error("coverage records must be carried")
	}
}
