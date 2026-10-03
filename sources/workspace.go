package sources

import (
	"sort"
	"time"
)

// PaneKind is what a pane is doing.
type PaneKind string

const (
	PaneAgent   PaneKind = "agent"
	PaneProcess PaneKind = "process"
	PaneShell   PaneKind = "shell"
	PaneOther   PaneKind = "other"
)

// AgentStatusName is AgentStatus for the wire and the screen.
type AgentStatusName string

// StatusNameOf renders a status; unknown when not reported.
func StatusNameOf(st AgentStatus) AgentStatusName {
	switch st {
	case AgentStatusNeedsInput:
		return "needs_input"
	case AgentStatusWorking:
		return "working"
	case AgentStatusIdle:
		return "idle"
	default:
		return "unknown"
	}
}

// agentCommands are foreground commands that identify an agent pane even
// before its hook has reported.
var agentCommands = map[string]string{"claude": "claude", "codex": "codex"}

// shellCommands are foreground commands that make a pane a plain shell.
var shellCommands = map[string]bool{"zsh": true, "bash": true, "fish": true, "sh": true, "dash": true, "nu": true}

// Workspace is the host → session → pane tree the session view and the
// cockpit_workspaces tool share. It is derived from the same observations
// as the attention queue, so the two cannot disagree about freshness,
// identity or coverage.
type Workspace struct {
	Hosts    []HostView `json:"hosts"`
	Coverage []Coverage `json:"coverage"`
}

// HostView is one machine: local is the empty name.
type HostView struct {
	Name        string        `json:"name"`
	Observation Observation   `json:"observation"`
	ObservedAt  time.Time     `json:"observed_at"`
	Err         string        `json:"error,omitempty"`
	Sessions    []SessionView `json:"sessions"`
	Dormant     []DormantView `json:"dormant"`
}

// ProjectSummary is the git state shown beside a session or dormant repo.
type ProjectSummary struct {
	Branch   string `json:"branch"`
	Dirty    int    `json:"dirty"`
	Unpushed int    `json:"unpushed"`
}

// SessionView is one live tmux session with its rollup and panes.
type SessionView struct {
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Host       string          `json:"host,omitempty"`
	ID         string          `json:"session_id"`
	Generation string          `json:"generation"`
	Attached   bool            `json:"attached"`
	LastUsed   time.Time       `json:"last_used"`
	Windows    int             `json:"windows"`
	Project    *ProjectSummary `json:"project,omitempty"`
	Status     AgentStatusName `json:"status"`
	Reported   bool            `json:"reported"`
	// LegacyReport is true when the status came from a session-level
	// record: an older hook, limited precision.
	LegacyReport     bool       `json:"legacy_report,omitempty"`
	Agents           int        `json:"agents"`
	ProcessesRunning int        `json:"processes_running"`
	ProcessesTotal   int        `json:"processes_total"`
	Panes            []PaneView `json:"panes"`
}

// PaneView is one pane, classified.
type PaneView struct {
	WindowID   string       `json:"window_id"`
	WindowName string       `json:"window_name"`
	PaneID     string       `json:"pane_id"`
	Generation string       `json:"generation"`
	Active     bool         `json:"active"`
	Kind       PaneKind     `json:"kind"`
	Command    string       `json:"command"`
	Path       string       `json:"path"`
	Agent      *AgentView   `json:"agent,omitempty"`
	Process    *ProcessView `json:"process,omitempty"`
	// Attention is true when a fresh attention item targets this pane or
	// its window.
	Attention bool `json:"attention"`
}

// AgentView is the agent side of a pane.
type AgentView struct {
	Engine     string          `json:"engine"`
	Status     AgentStatusName `json:"status"`
	Reported   bool            `json:"reported"`
	ReportedAt time.Time       `json:"reported_at,omitempty"`
	Invocation string          `json:"invocation,omitempty"`
}

// ProcessView is the configured-process side of a pane, copied verbatim
// from the process observation so this view and the process panel agree.
type ProcessView struct {
	Name         string         `json:"name"`
	Outcome      ProcessOutcome `json:"outcome"`
	Display      string         `json:"display"`
	Managed      bool           `json:"managed"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	DesiredState string         `json:"desired_state,omitempty"`
}

// DormantView is a configured repo with no session.
type DormantView struct {
	Key     string          `json:"key"`
	Name    string          `json:"name"`
	Host    string          `json:"host,omitempty"`
	Project *ProjectSummary `json:"project,omitempty"`
}

// BuildWorkspace derives the tree from the same observations the attention
// queue reads. It is pure.
func BuildWorkspace(in AttentionInput) Workspace {
	rep := DeriveAttention(in)
	flagged := map[string]bool{} // host + pane id, or host + window id
	for _, i := range rep.Items {
		if i.Housekeeping || i.Observation != ObservationFresh {
			continue
		}
		if i.Target.PaneID != "" {
			flagged[i.Host+"/"+i.Target.PaneID] = true
		}
		if i.Target.WindowID != "" {
			flagged[i.Host+"/"+i.Target.WindowID] = true
		}
	}

	ws := Workspace{Coverage: rep.Coverage, Hosts: []HostView{}}
	for _, h := range in.Hosts {
		ws.Hosts = append(ws.Hosts, buildHost(in, h, flagged))
	}
	return ws
}

func buildHost(in AttentionInput, h HostReport, flagged map[string]bool) HostView {
	hv := HostView{Name: h.Host, Observation: h.Outcome, ObservedAt: h.ObservedAt, Err: h.Err, Sessions: []SessionView{}, Dormant: []DormantView{}}
	if h.Outcome == ObservationUnavailable && len(h.Sessions) > 0 {
		// Last-known data under a failed read is stale, not gone.
		hv.Observation = ObservationStale
	}

	git := map[string]GitRepoStatus{}
	for _, g := range h.Git {
		git[g.Label] = g
	}
	panesBySession := map[string][]PaneReport{}
	for _, p := range h.Panes {
		panesBySession[p.SessionID] = append(panesBySession[p.SessionID], p)
	}
	// Process views by window id, and running/total counts by session.
	procByWindow := map[string]ProcessView{}
	procCounts := map[string][2]int{}
	for _, obs := range h.Processes {
		running, total := 0, 0
		for _, p := range obs.Processes {
			if !p.Configured {
				continue
			}
			total++
			if p.Outcome == OutcomeRunning {
				running++
			}
			if p.WindowID != "" {
				procByWindow[p.WindowID] = ProcessView{Name: p.Name, Outcome: p.Outcome, Display: p.Display, Managed: p.Managed, ExitCode: p.ExitCode, DesiredState: p.DesiredState}
			}
		}
		procCounts[obs.Session] = [2]int{running, total}
	}

	live := map[string]bool{}
	for _, s := range h.Sessions {
		if s.Name == in.SelfSession || s.ViewOf != "" {
			continue
		}
		live[s.Name] = true
		sv := SessionView{
			Key: projectKey(h.Host, s.Name), Name: s.Name, Host: h.Host, ID: s.ID, Generation: s.Generation,
			Attached: s.Attached, LastUsed: s.LastUsed, Windows: s.Windows, Panes: []PaneView{},
		}
		if g, ok := git[s.Name]; ok && g.Error == nil {
			sv.Project = &ProjectSummary{Branch: g.Branch, Dirty: g.DirtyCount, Unpushed: g.Unpushed}
		}
		counts := procCounts[s.Name]
		sv.ProcessesRunning, sv.ProcessesTotal = counts[0], counts[1]

		var records []PaneReport
		for _, p := range panesBySession[s.ID] {
			pv := buildPane(h.Host, p, procByWindow, flagged)
			if pv.Kind == PaneAgent {
				sv.Agents++
			}
			if p.Recorded {
				records = append(records, p)
			}
			sv.Panes = append(sv.Panes, pv)
		}
		switch {
		case len(records) > 0:
			st, ok := RollupPaneStatus(records)
			sv.Status, sv.Reported = StatusNameOf(st), ok
		case s.StatusReported:
			sv.Status, sv.Reported, sv.LegacyReport = StatusNameOf(s.Status), true, true
		default:
			sv.Status = "unknown"
		}
		hv.Sessions = append(hv.Sessions, sv)
	}
	sort.SliceStable(hv.Sessions, func(i, j int) bool {
		a, b := hv.Sessions[i], hv.Sessions[j]
		if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
			return ra > rb
		}
		return a.Name < b.Name
	})

	for _, g := range h.Git {
		if live[g.Label] {
			continue
		}
		dv := DormantView{Key: g.Key(), Name: g.Label, Host: g.Host}
		if g.Error == nil {
			dv.Project = &ProjectSummary{Branch: g.Branch, Dirty: g.DirtyCount, Unpushed: g.Unpushed}
		}
		hv.Dormant = append(hv.Dormant, dv)
	}
	sort.Slice(hv.Dormant, func(i, j int) bool { return hv.Dormant[i].Name < hv.Dormant[j].Name })
	return hv
}

// buildPane classifies one pane. A pane that is both an agent and a managed
// process is an agent for status and a process for controls.
func buildPane(host string, p PaneReport, procByWindow map[string]ProcessView, flagged map[string]bool) PaneView {
	pv := PaneView{
		WindowID: p.WindowID, WindowName: p.WindowName, PaneID: p.PaneID, Generation: p.Generation,
		Active: p.Active, Command: p.Command, Path: p.Path, Kind: PaneOther,
		Attention: flagged[host+"/"+p.PaneID] || flagged[host+"/"+p.WindowID],
	}
	if proc, ok := procByWindow[p.WindowID]; ok {
		proc := proc
		pv.Process = &proc
		pv.Kind = PaneProcess
	}
	engine, isAgentCmd := agentCommands[p.Command]
	if p.Recorded || isAgentCmd {
		if engine == "" {
			engine = "other"
		}
		av := &AgentView{Engine: engine, Status: "unknown", Invocation: p.Invocation, ReportedAt: p.ReportedAt}
		if p.Reported {
			av.Status, av.Reported = StatusNameOf(p.Status), true
		}
		pv.Agent = av
		pv.Kind = PaneAgent
	} else if pv.Kind == PaneOther && shellCommands[p.Command] {
		pv.Kind = PaneShell
	}
	return pv
}

func statusRank(s AgentStatusName) int {
	switch s {
	case "needs_input":
		return 3
	case "working":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}
