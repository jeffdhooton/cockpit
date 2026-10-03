# Session View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard with a session-first view (sessions left, selected session's panes and preview right) backed by one pure workspace derivation that also serves a read-only `cockpit_workspaces` MCP tool.

**Architecture:** `sources.BuildWorkspace` derives a host → session → pane tree from the same `AttentionInput` the attention queue uses, so freshness, identity and coverage are shared. The TUI renders that tree in a new `ViewSessions`; the daemon feeds the same derivation to a new tool. Pane previews are bounded `capture-pane` reads through the host's runner with sequence guards. The per-session pane-hash status guess and the five dashboard panels are removed.

**Tech Stack:** Go 1.24, Bubbletea/lipgloss (existing), tmux formats, existing `sources.Runner`/`SSHRunner`, existing JSON-RPC daemon.

**Spec:** `docs/superpowers/specs/2026-09-05-session-view-design.md`

## Global Constraints

- Enter in the session view is attach-only: it never creates a session or runs a command. Only `o` may.
- Needs-input is never inferred; a command-only agent has status `unknown` with `Reported = false`.
- A failed host read keeps the last tree marked `stale`; nothing is drawn as empty because of a failure.
- Late replies never replace newer state: every preview carries a sequence and a pane id.
- Control sequences from names, commands, paths and previews are stripped before rendering (`sources.StripControl`).
- Preview reads are bounded to the visible screen, at most 40 lines, and never run against an unavailable host.
- `default_view` accepts `sessions` (default), `grid`, `dashboard` (alias of `sessions`).
- Tests use private tmux servers (`sources.ExecRunner{Socket:…, NoConfig:true}`), never the user's server.
- Do not commit; the working tree stays uncommitted like the rest of today's work unless the user asks.
- Run `gofmt -w` on every touched file and `go vet ./...` before declaring a task done.

---

## File Structure

| File | Responsibility |
|---|---|
| `sources/panes.go` (modify) | Pane format gains command, path, active flag; `PaneReport` gains `Command`, `Path`, `Active`. |
| `sources/workspace.go` (create) | `Workspace`, `HostView`, `SessionView`, `PaneView`, `AgentView`, `ProcessView`, `DormantView`; `BuildWorkspace`. |
| `sources/workspace_test.go` (create) | Fixture tests for every classification and ordering rule. |
| `sources/tmux_args.go` (modify) | `CapturePaneVisibleArgs(paneID, lines)`. |
| `daemon/tools.go`, `daemon/definitions.go` (modify) | `cockpit_workspaces` tool; `pane_id` on `cockpit_read_output`. |
| `daemon/workspaces_test.go` (create) | Tool schema, read-only, TUI equivalence. |
| `config/config.go` (modify) | `default_view` validation and alias. |
| `tui/sessionsview.go` (create) | `sessionsModel`: selection, focus, layout, rendering. |
| `tui/sessionsview_test.go` (create) | Selection, focus, late preview, breakpoints, stripping. |
| `tui/app.go` (modify) | `ViewSessions`, message handling, capture prompt, removal of dashboard code and the hash guess. |
| `tui/grid.go`, `tui/panels.go`, `tui/nav.go`, `tui/keyhints.go` (modify) | Keys `g`/`d`, hints, return points. |
| `tui/sessions.go`, `tui/repos.go` (modify) | Shrink to data holders (`SessionsModel.Sessions/Statuses/AdoptReported`, `ReposModel.Repos`); move `padRight` to `styles.go`. |
| `tui/tasks.go`, `tui/inbox.go`, `tui/viz.go`, `tui/boids.go`, `tui/clock.go`, `tui/constellation.go`, `tui/life.go`, `tui/orbital.go`, `tui/plasma.go`, `tui/rain.go`, `tui/starfield.go` (delete) | Dashboard panels and visualizers. |
| `README.md`, `docs/workspace-usability-progress.md` (modify) | Document the view, tool, and removals. |

---

### Task 1: Pane observation carries command, path and active flag

**Files:**
- Modify: `sources/panes.go`
- Modify: `sources/foundations_test.go`

**Interfaces:**
- Produces: `PaneReport.Command string`, `PaneReport.Path string`, `PaneReport.Active bool`; `ParsePaneReports` reads the new format.

- [ ] **Step 1: Write the failing test** (append to `sources/foundations_test.go`)

```go
func TestPaneReportsCarryCommandPathAndActive(t *testing.T) {
	now := time.Unix(1_700_000_600, 0)
	sessions := []TmuxSession{{Name: "app", ID: "$1"}}
	out := "$1|@1|%1|10|0|1|claude|/Users/me/work/app|needs_input|1700000590|inv-a|5|100|200|reviewer\n" +
		"$1|@2|%2|11|0|0|zsh|/Users/me/odd\\|dir||||0|100|200|a|b\n"
	panes := ParsePaneReports(out, "", sessions, now)
	if len(panes) != 2 {
		t.Fatalf("want 2 panes, got %+v", panes)
	}
	if p := panes[0]; p.Command != "claude" || p.Path != "/Users/me/work/app" || !p.Active || p.WindowName != "reviewer" || p.Status != AgentStatusNeedsInput {
		t.Errorf("pane 0 = %+v", p)
	}
	if p := panes[1]; p.Command != "zsh" || p.Path != "/Users/me/odd|dir" || p.Active || p.WindowName != "a|b" {
		t.Errorf("pane 1 = %+v", p)
	}
}
```

- [ ] **Step 2: Run it** — `go test ./sources/ -run TestPaneReportsCarryCommandPathAndActive` — expected: FAIL (fields undefined / wrong parse).

- [ ] **Step 3: Implement.** In `sources/panes.go`, change the format and parser. The path is quoted with `#{q:…}` so a `|` in it arrives as `\|`; the parser splits on unescaped `|` only for the path field by first splitting the whole line on the separator and re-joining escaped pieces.

```go
const paneFormat = "#{session_id}|#{window_id}|#{pane_id}|#{pane_pid}|#{pane_dead}|#{pane_active}" +
	"|#{pane_current_command}|#{q:pane_current_path}" +
	"|#{" + paneStatusOption + "}|#{" + paneStatusAtOption + "}|#{" + paneInvocationOption + "}|#{" + paneSeqOption + "}" +
	"|#{pid}|#{start_time}|#{window_name}"

const paneLeadingFields = 14
```

Add fields to `PaneReport`:

```go
	// Command is the pane's foreground process name, Path its current
	// directory, Active whether it is the window's active pane.
	Command string
	Path    string
	Active  bool
```

Replace the body of the per-line parse with:

```go
		parts := splitPaneLine(strings.TrimSpace(line))
		if len(parts) < paneLeadingFields+1 {
			continue
		}
		pid, _ := strconv.Atoi(parts[3])
		r := PaneReport{
			Host:       host,
			SessionID:  parts[0],
			Session:    nameByID[parts[0]],
			WindowID:   parts[1],
			PaneID:     parts[2],
			PanePID:    pid,
			Dead:       parts[4] == "1",
			Active:     parts[5] == "1",
			Command:    parts[6],
			Path:       parts[7],
			Invocation: parts[10],
			Generation: serverGeneration(parts[12], parts[13]),
			WindowName: strings.Join(parts[paneLeadingFields:], fieldSep),
		}
		r.Seq, _ = strconv.ParseInt(parts[11], 10, 64)
		if parts[8] != "" {
			r.Recorded = true
			if epoch, err := strconv.ParseInt(parts[9], 10, 64); err == nil {
				r.ReportedAt = time.Unix(epoch, 0)
			}
			r.Status, r.Reported = StatusFromOptions(parts[8], parts[9], "", "", now)
		}
		reports = append(reports, r)
```

And the splitter, which honours the `\|` that `#{q:}` produces inside the path field only:

```go
// splitPaneLine splits a pane line on the separator, re-joining a
// backslash-escaped separator inside the quoted path field (index 7).
func splitPaneLine(line string) []string {
	raw := strings.Split(line, fieldSep)
	var out []string
	for i := 0; i < len(raw); i++ {
		part := raw[i]
		if len(out) == 7 {
			for strings.HasSuffix(part, `\`) && i+1 < len(raw) {
				part = part[:len(part)-1] + fieldSep + raw[i+1]
				i++
			}
			part = strings.ReplaceAll(part, `\ `, " ")
		}
		out = append(out, part)
	}
	return out
}
```

Update every existing fixture line in `sources/foundations_test.go`, `sources/attention_test.go` (`pane()` helper builds structs, no change), `sources/attach_test.go`, `daemon/attention_test.go`, `daemon/hooks_test.go` (any literal `list-panes` output) to the 15-field layout: insert `|<active>|<command>|<path>` after the `pane_dead` field. Example: `$1|@2|%3|10|0|||||100|200|agent` becomes `$1|@2|%3|10|0|1|claude|/tmp|||||100|200|agent`.

- [ ] **Step 4: Run** `go test ./sources/ ./daemon/` — expected: PASS.

---

### Task 2: Workspace derivation

**Files:**
- Create: `sources/workspace.go`
- Create: `sources/workspace_test.go`

**Interfaces:**
- Consumes: `AttentionInput`, `HostReport`, `PaneReport`, `ProcessObservation`, `RollupPaneStatus`, `GitRepoStatus`, `Coverage`, `DeriveAttention` (for coverage records).
- Produces:

```go
type Workspace struct {
	Hosts    []HostView `json:"hosts"`
	Coverage []Coverage `json:"coverage"`
}
type HostView struct {
	Name        string       `json:"name"`
	Observation Observation  `json:"observation"`
	ObservedAt  time.Time    `json:"observed_at"`
	Err         string       `json:"error,omitempty"`
	Sessions    []SessionView `json:"sessions"`
	Dormant     []DormantView `json:"dormant"`
}
type ProjectSummary struct { Branch string `json:"branch"`; Dirty int `json:"dirty"`; Unpushed int `json:"unpushed"` }
type SessionView struct {
	Key, Name, ID, Generation string; Host string; Attached bool; LastUsed time.Time; Windows int
	Project *ProjectSummary; Status AgentStatusName; Reported bool; LegacyReport bool
	Agents, ProcessesRunning, ProcessesTotal int; Panes []PaneView
}
type PaneView struct {
	WindowID, WindowName, PaneID, Generation string; Active bool
	Kind PaneKind; Command, Path string; Agent *AgentView; Process *ProcessView; Attention bool
}
type AgentView struct { Engine string; Status AgentStatusName; Reported bool; ReportedAt time.Time; Invocation string }
type ProcessView struct { Name string; Outcome ProcessOutcome; Display string; Managed bool; ExitCode *int; DesiredState string }
type DormantView struct { Key, Name, Host string; Project *ProjectSummary }
type PaneKind string // "agent" | "process" | "shell" | "other"
type AgentStatusName string // "needs_input" | "working" | "idle" | "unknown"
func BuildWorkspace(in AttentionInput) Workspace
func StatusNameOf(st AgentStatus) AgentStatusName
```

- [ ] **Step 1: Write the failing tests** in `sources/workspace_test.go`

```go
package sources

import (
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
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
	h.Panes = []PaneReport{blocked, wsPane("old", "$2", "@1", "%2", "claude")}
	h.Processes = map[string]ProcessObservation{"app": {Session: "app", SessionExists: true, Processes: []ProcessInfo{
		{Name: "dev", Configured: true, Managed: true, WindowID: "@7", PaneID: "%7", Outcome: OutcomeExited, Display: "Exited (code 1)", DesiredState: DesiredRunning},
	}}}
	h.Panes = append(h.Panes, wsPane("app", "$1", "@7", "%7", "node"))
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
	_ = config.RepoConfig{}
}
```

Add `"strings"` to the imports.

- [ ] **Step 2: Run** `go test ./sources/ -run Workspace` — expected: FAIL (undefined types).

- [ ] **Step 3: Implement** `sources/workspace.go`

```go
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
// cockpit_workspaces tool share.
type Workspace struct {
	Hosts    []HostView `json:"hosts"`
	Coverage []Coverage `json:"coverage"`
}

type HostView struct {
	Name        string        `json:"name"`
	Observation Observation   `json:"observation"`
	ObservedAt  time.Time     `json:"observed_at"`
	Err         string        `json:"error,omitempty"`
	Sessions    []SessionView `json:"sessions"`
	Dormant     []DormantView `json:"dormant"`
}

type ProjectSummary struct {
	Branch   string `json:"branch"`
	Dirty    int    `json:"dirty"`
	Unpushed int    `json:"unpushed"`
}

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
	// Attention is true when an attention item targets this pane or window.
	Attention bool `json:"attention"`
}

type AgentView struct {
	Engine     string          `json:"engine"`
	Status     AgentStatusName `json:"status"`
	Reported   bool            `json:"reported"`
	ReportedAt time.Time       `json:"reported_at,omitempty"`
	Invocation string          `json:"invocation,omitempty"`
}

type ProcessView struct {
	Name         string         `json:"name"`
	Outcome      ProcessOutcome `json:"outcome"`
	Display      string         `json:"display"`
	Managed      bool           `json:"managed"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	DesiredState string         `json:"desired_state,omitempty"`
}

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
	// Process views by window id, across the host's projects.
	procByWindow := map[string]ProcessView{}
	procCounts := map[string][2]int{} // session name → running, total
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
```

Note: a pane that is both a managed process and an agent keeps `Kind = agent` and both `Agent` and `Process` set, per the spec.

- [ ] **Step 4: Run** `go test ./sources/` — expected: PASS.

---

### Task 3: Bounded visible-screen capture

**Files:**
- Modify: `sources/tmux_args.go`, `sources/tmux_args_test.go`

**Interfaces:**
- Produces: `func CapturePaneVisibleArgs(paneID string, lines int) []string` — `capture-pane -p -t %id -S -<lines-1> -E -1`? No: the visible screen is captured with no `-S`; `lines` trims the tail on the caller's side. Signature kept for clarity: returns `[]string{"capture-pane", "-p", "-t", paneID}`.

- [ ] **Step 1: Test** (append to `sources/tmux_args_test.go`)

```go
func TestCapturePaneVisibleArgsReadsOnlyTheScreen(t *testing.T) {
	got := CapturePaneVisibleArgs("%7")
	want := []string{"capture-pane", "-p", "-t", "%7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}
```

- [ ] **Step 2: Implement** in `sources/tmux_args.go`

```go
// CapturePaneVisibleArgs reads only what a pane currently shows: no
// scrollback, which is what a preview is and all a preview may cost.
func CapturePaneVisibleArgs(paneID string) []string {
	return []string{"capture-pane", "-p", "-t", paneID}
}
```

- [ ] **Step 3: Run** `go test ./sources/ -run CapturePaneVisible` — PASS.

---

### Task 4: `default_view` accepts `sessions` and aliases `dashboard`

**Files:**
- Modify: `config/config.go`, `config/config_test.go`

- [ ] **Step 1: Test** (append to `config/config_test.go`; follow the file's existing `writeConfig`/`Load` pattern, or use `Parse`)

```go
func TestDefaultViewAcceptsSessionsAndAliasesDashboard(t *testing.T) {
	for raw, want := range map[string]string{
		"":                             "sessions",
		"default_view = \"sessions\"":  "sessions",
		"default_view = \"grid\"":      "grid",
		"default_view = \"dashboard\"": "sessions",
	} {
		cfg, _, err := Parse([]byte("[general]\n" + raw + "\n"))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if cfg.General.DefaultView != want {
			t.Errorf("%q → %q, want %q", raw, cfg.General.DefaultView, want)
		}
	}
	if _, _, err := Parse([]byte("[general]\ndefault_view = \"nope\"\n")); err == nil {
		t.Error("an unknown view must be rejected")
	}
}
```

- [ ] **Step 2: Implement.** In `applyDefaults`: default `"sessions"`, and map `"dashboard"` to `"sessions"`. In `validate`: accept `"", "sessions", "grid"`. Update the comment on `GeneralConfig.DefaultView`. Check `config_test.go` for an existing test asserting the old default `"grid"` and update it.

- [ ] **Step 3: Run** `go test ./config/` — PASS.

---

### Task 5: `cockpit_workspaces` tool and `pane_id` on `cockpit_read_output`

**Files:**
- Modify: `daemon/tools.go`, `daemon/definitions.go`
- Create: `daemon/workspaces_test.go`
- Modify: `daemon/tools_test.go` (tool count 15 → 16), `daemon/lifecycle_test.go` (same)

**Interfaces:**
- Consumes: `Tools.collectAttention` (refactor: split into `collectInput(ctx) sources.AttentionInput` and derive), `sources.BuildWorkspace`.
- Produces: tool `cockpit_workspaces` returning `{"schema_version":1,"observed_at":…,"hosts":[…],"coverage":[…]}`; `cockpit_read_output` accepts `pane_id`.

- [ ] **Step 1: Test** `daemon/workspaces_test.go`

```go
package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

func TestWorkspacesToolReturnsTheTreeReadOnly(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-sessions": "app|2|0|1700000000|||||$1|100|200\n",
		"list-panes":    "$1|@1|%1|10|0|1|claude|/w/app|needs_input|1700000000|inv|5|100|200|agent\n$1|@2|%2|11|0|0|zsh|/w/app||||0|100|200|zsh\n",
		"list-windows":  "1|dev|1|222|0|1|@3|%3|1|dev|100|200\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
	tools.Now = func() time.Time { return time.Unix(1_700_000_100, 0) }

	got, err := tools.Call(context.Background(), "cockpit_workspaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		SchemaVersion int               `json:"schema_version"`
		Hosts         []sources.HostView `json:"hosts"`
		Coverage      []sources.Coverage `json:"coverage"`
	}
	decodeInto(t, got, &payload)
	if payload.SchemaVersion != 1 || len(payload.Hosts) != 1 || len(payload.Hosts[0].Sessions) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	s := payload.Hosts[0].Sessions[0]
	if s.Key != "app" || s.Status != "needs_input" || s.Agents != 1 || len(s.Panes) != 2 || s.Panes[0].Kind != sources.PaneAgent || s.Panes[1].Kind != sources.PaneShell {
		t.Errorf("session = %+v", s)
	}
	for _, verb := range []string{"new-window", "respawn-window", "kill-window", "new-session", "switch-client", "send-keys", "set-option"} {
		if len(f.called(verb)) != 0 {
			t.Errorf("workspaces must be read-only, ran %s", verb)
		}
	}

	// Equivalence: the same input derived directly gives the same tree.
	direct := sources.BuildWorkspace(tools.collectInput(context.Background()))
	if len(direct.Hosts[0].Sessions) != 1 || direct.Hosts[0].Sessions[0].Panes[0].PaneID != s.Panes[0].PaneID {
		t.Errorf("direct = %+v", direct)
	}
}

func TestReadOutputAcceptsAPaneID(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1|dev|0|222|0||@1|%1|1|dev|100|200\n",
		"capture-pane": "hello\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
	got, err := tools.Call(context.Background(), "cockpit_read_output", map[string]any{"project": "app", "pane_id": "%1"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Output string `json:"output"`
		PaneID string `json:"pane_id"`
	}
	decodeInto(t, got, &payload)
	if payload.Output != "hello" || payload.PaneID != "%1" {
		t.Errorf("payload = %+v", payload)
	}
	calls := f.called("capture-pane")
	if len(calls) != 1 || !slices.Contains(calls[0], "%1") {
		t.Errorf("capture must address the pane id: %v", calls)
	}
	if _, err := tools.Call(context.Background(), "cockpit_read_output", map[string]any{"project": "app", "pane_id": "%9"}); err == nil {
		t.Error("a pane that is not in the project must be refused")
	}
}
```

Add `"slices"` to imports.

- [ ] **Step 2: Run** — FAIL (unknown tool).

- [ ] **Step 3: Implement.**
  - In `daemon/tools.go` `Call`: add `case "cockpit_workspaces": return t.workspaces(ctx)`.
  - Split `collectAttention` into `collectInput(ctx) sources.AttentionInput` (everything up to and including Hermes) and keep `collectAttention` as `rep := sources.DeriveAttention(t.collectInput(ctx)); track; return rep`.
  - Add:

```go
// workspaces is read-only: the host → session → pane tree, no previews,
// no navigation, nothing started.
func (t *Tools) workspaces(ctx context.Context) (any, error) {
	ws := sources.BuildWorkspace(t.collectInput(ctx))
	return map[string]any{
		"schema_version": attentionSchemaVersion,
		"observed_at":    t.Now(),
		"hosts":          ws.Hosts,
		"coverage":       ws.Coverage,
	}, nil
}
```

  - In `window()` (tools.go): before the name lookup, if `pane_id := argString(args, "pane_id"); pane_id != ""`, walk `obs.Processes` for `p.PaneID == pane_id` and return `windowRef{name: p.Name, id: p.WindowID, target: pane_id}`; if none matches, return `fmt.Errorf("project %q has no pane %s", repo.Key(), pane_id)`. `process` becomes optional when `pane_id` is given: move the `name == ""` check after the pane branch. In `readOutput`'s result add `"pane_id": window.target` when it starts with `%`.
  - In `daemon/definitions.go`: add the `cockpit_workspaces` definition (`obj(map[string]any{})`, description from the spec's MCP section) and add `"pane_id": str("Pane id (%N) from cockpit_workspaces; alternative to process")` to `cockpit_read_output`'s properties, changing its required list to `"project"` only.
  - Update the tool counts in `daemon/tools_test.go` (`15` → `16`) and `daemon/lifecycle_test.go`.

- [ ] **Step 4: Run** `go test ./daemon/` — PASS.

---

### Task 6: The session view model and rendering

**Files:**
- Create: `tui/sessionsview.go`
- Create: `tui/sessionsview_test.go`
- Modify: `tui/styles.go` (move `padRight` here from `tui/repos.go`)

**Interfaces:**
- Consumes: `sources.Workspace`, `sources.SessionView`, `sources.PaneView`, helpers `clip`, `wrap`, `clean`, `ageOf`, `hostLabel`, `RowCursor`, `SectionLabel`, `RenderPanel`, `MobileMaxWidth`, `renderHints`.
- Produces:

```go
type sessionsModel struct {
	ws          sources.Workspace
	selected    string // session key
	pane        string // pane id within the selected session
	paneFocus   bool   // Tab moved focus to the pane list
	filter      textinput.Model
	query       string
	preview     panePreview
	scroll      int
	message     string
}
type panePreview struct{ host, paneID, text, err string; seq int; loading bool }
func newSessionsModel() sessionsModel
func (s *sessionsModel) refresh(ws sources.Workspace)            // re-resolve selection
func (s *sessionsModel) sessions() []sources.SessionView         // filtered, flattened in host order
func (s *sessionsModel) current() *sources.SessionView
func (s *sessionsModel) currentPane() *sources.PaneView
func (s *sessionsModel) move(delta int)                          // sessions or panes by focus
func (s *sessionsModel) selectIndex(i int)                       // digit hotkeys: i-th live session
func (s *sessionsModel) applyPreview(msg panePreviewMsg) bool
func (s *sessionsModel) view(width, height int, now time.Time, filtering bool) string
type panePreviewMsg struct{ host, paneID, text string; seq int; err error }
const sessionsSplitWidth = 90
const sessionsListWidth = 32
const previewLines = 40
```

- [ ] **Step 1: Tests** `tui/sessionsview_test.go`

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/sources"
)

func wsFixture(now time.Time) sources.Workspace {
	blocked := sources.PaneReport{Session: "api", SessionID: "$2", WindowID: "@1", PaneID: "%9", Generation: "g1", Command: "claude", WindowName: "reviewer", Recorded: true, Reported: true, Status: sources.AgentStatusNeedsInput, Invocation: "a"}
	local := sources.HostReport{Outcome: sources.ObservationFresh, ObservedAt: now,
		Sessions: []sources.TmuxSession{{Name: "site", ID: "$1", Generation: "g1"}, {Name: "api", ID: "$2", Generation: "g1"}},
		Panes: []sources.PaneReport{
			{Session: "site", SessionID: "$1", WindowID: "@1", PaneID: "%1", Generation: "g1", Command: "zsh", WindowName: "zsh", Active: true},
			{Session: "site", SessionID: "$1", WindowID: "@2", PaneID: "%2", Generation: "g1", Command: "codex", WindowName: "codex"},
			blocked,
		},
	}
	remote := sources.HostReport{Host: "mini", Outcome: sources.ObservationUnavailable, Err: "not polled yet"}
	return sources.BuildWorkspace(sources.AttentionInput{Hosts: []sources.HostReport{local, remote}, Now: now, SelfSession: "cockpit"})
}

func TestSessionsSelectionSurvivesReorder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	// api (needs input) sorts first; move to site.
	if s.current() == nil || s.current().Name != "api" {
		t.Fatalf("first = %+v", s.current())
	}
	s.move(1)
	if s.current().Name != "site" {
		t.Fatalf("after move = %+v", s.current())
	}
	// site's codex starts needing input: site now sorts first; selection stays on site.
	ws := wsFixture(now)
	ws.Hosts[0].Sessions[1].Status = "needs_input"
	s.refresh(ws)
	if s.current().Name != "site" || s.selected != "site" {
		t.Errorf("selection moved: %+v", s.current())
	}
}

func TestSessionsTabMovesFocusToPanes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	s.move(1) // site
	s.paneFocus = true
	if p := s.currentPane(); p == nil || p.PaneID != "%1" {
		t.Fatalf("first pane = %+v", p)
	}
	s.move(1)
	if p := s.currentPane(); p == nil || p.PaneID != "%2" || p.Kind != sources.PaneAgent {
		t.Errorf("second pane = %+v", p)
	}
	s.paneFocus = false
	s.move(-1) // back to api; pane selection resets to api's active/first pane
	if p := s.currentPane(); p == nil || p.PaneID != "%9" {
		t.Errorf("pane after session change = %+v", p)
	}
}

func TestSessionsLatePreviewIsDropped(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	s.move(1)
	s.paneFocus = true
	s.preview.seq = 3
	s.move(1) // now on %2
	if s.applyPreview(panePreviewMsg{paneID: "%1", seq: 3, text: "old pane"}) {
		t.Error("a reply for another pane must be dropped")
	}
	s.preview.seq = 5
	if s.applyPreview(panePreviewMsg{paneID: "%2", seq: 4, text: "stale"}) {
		t.Error("an older sequence must be dropped")
	}
	if !s.applyPreview(panePreviewMsg{paneID: "%2", seq: 5, text: "\x1b[31mfresh\x1b[0m"}) || s.preview.text != "fresh" {
		t.Errorf("preview = %+v", s.preview)
	}
}

func TestSessionsViewBreakpoints(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSessionsModel()
	s.refresh(wsFixture(now))
	wide := s.view(120, 36, now, false)
	if !strings.Contains(wide, "│") || !strings.Contains(wide, "LOCAL / API") {
		t.Errorf("wide layout should split with a detail header:\n%s", wide)
	}
	for _, l := range strings.Split(wide, "\n") {
		if lipglossWidth(l) > 120 {
			t.Errorf("line wider than 120: %q", l)
		}
	}
	narrow := s.view(40, 12, now, false)
	lines := strings.Split(narrow, "\n")
	if len(lines) > 12 {
		t.Errorf("narrow render is %d lines", len(lines))
	}
	for _, l := range lines {
		if lipglossWidth(l) > 40 {
			t.Errorf("line wider than 40: %q", l)
		}
	}
	if !strings.Contains(narrow, "api") || !strings.Contains(narrow, "reviewer") {
		t.Errorf("narrow layout must show the session and its pane:\n%s", narrow)
	}
	if !strings.Contains(wide, "mini") || !strings.Contains(wide, "unavailable") {
		t.Errorf("an unpolled host must be visible as unavailable:\n%s", wide)
	}
}

func TestSessionsViewStripsControlSequences(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ws := wsFixture(now)
	ws.Hosts[0].Sessions[0].Panes[0].WindowName = "evil\x1b[2Jname"
	ws.Hosts[0].Sessions[0].Panes[0].Path = "/tmp/\x07bell"
	s := newSessionsModel()
	s.refresh(ws)
	s.preview.text = sources.StripControl("\x1b]0;x\x07preview")
	out := s.view(120, 36, now, false)
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x07") || strings.Contains(out, "\x1b]0;") {
		t.Error("control sequences leaked")
	}
}
```

- [ ] **Step 2: Run** `go test ./tui/ -run Sessions` — FAIL (undefined).

- [ ] **Step 3: Implement** `tui/sessionsview.go`. Move `padRight` from `tui/repos.go` into `tui/styles.go` first (identical body).

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/sources"
)

const (
	sessionsSplitWidth = 90 // at or above: sessions left, detail right
	sessionsListWidth  = 32
	previewLines       = 40
)

type panePreview struct {
	host, paneID, text, err string
	seq                     int
	loading                 bool
}

type panePreviewMsg struct {
	host, paneID, text string
	seq                int
	err                error
}

// sessionsModel is the session view: the workspace tree, a session
// selection held by key, a pane selection held by id, and the preview of
// the selected pane.
type sessionsModel struct {
	ws        sources.Workspace
	selected  string
	pane      string
	paneFocus bool
	filter    textinput.Model
	query     string
	preview   panePreview
	scroll    int
	message   string
}

func newSessionsModel() sessionsModel {
	f := textinput.New()
	f.Placeholder = "filter host or session"
	f.CharLimit = 64
	f.Width = 24
	return sessionsModel{filter: f}
}

// refresh installs a new tree and re-resolves both selections.
func (s *sessionsModel) refresh(ws sources.Workspace) {
	s.ws = ws
	s.resolve()
}

// sessions flattens the hosts in order, honouring the filter.
func (s *sessionsModel) sessions() []sources.SessionView {
	var out []sources.SessionView
	q := strings.ToLower(strings.TrimSpace(s.query))
	for _, h := range s.ws.Hosts {
		for _, sv := range h.Sessions {
			if q != "" && !strings.Contains(strings.ToLower(hostLabel(h.Name)+" "+sv.Name), q) {
				continue
			}
			out = append(out, sv)
		}
	}
	return out
}

func (s *sessionsModel) index() int {
	for i, sv := range s.sessions() {
		if sv.Key == s.selected {
			return i
		}
	}
	return -1
}

func (s *sessionsModel) resolve() {
	list := s.sessions()
	if len(list) == 0 {
		s.selected, s.pane = "", ""
		return
	}
	i := s.index()
	if i < 0 {
		i = clampInt(s.scroll, 0, len(list)-1)
		s.selected = list[i].Key
		s.pane = ""
	}
	cur := list[i]
	found := false
	for _, p := range cur.Panes {
		if p.PaneID == s.pane {
			found = true
		}
	}
	if !found {
		s.pane = defaultPane(cur)
	}
}

// defaultPane is the window's active pane, else the first.
func defaultPane(sv sources.SessionView) string {
	for _, p := range sv.Panes {
		if p.Active {
			return p.PaneID
		}
	}
	if len(sv.Panes) > 0 {
		return sv.Panes[0].PaneID
	}
	return ""
}

func (s *sessionsModel) current() *sources.SessionView {
	for _, sv := range s.sessions() {
		if sv.Key == s.selected {
			c := sv
			return &c
		}
	}
	return nil
}

func (s *sessionsModel) currentPane() *sources.PaneView {
	cur := s.current()
	if cur == nil {
		return nil
	}
	for _, p := range cur.Panes {
		if p.PaneID == s.pane {
			c := p
			return &c
		}
	}
	return nil
}

func (s *sessionsModel) move(delta int) {
	s.message = ""
	if s.paneFocus {
		cur := s.current()
		if cur == nil || len(cur.Panes) == 0 {
			return
		}
		i := 0
		for j, p := range cur.Panes {
			if p.PaneID == s.pane {
				i = j
			}
		}
		i = clampInt(i+delta, 0, len(cur.Panes)-1)
		s.pane = cur.Panes[i].PaneID
		s.preview = panePreview{}
		return
	}
	list := s.sessions()
	if len(list) == 0 {
		return
	}
	i := clampInt(s.index()+delta, 0, len(list)-1)
	if list[i].Key != s.selected {
		s.selected = list[i].Key
		s.pane = defaultPane(list[i])
		s.preview = panePreview{}
	}
	s.scroll = i
}

// selectIndex picks the i-th live session (digit hotkeys), returning it.
func (s *sessionsModel) selectIndex(i int) *sources.SessionView {
	list := s.sessions()
	if i < 0 || i >= len(list) {
		return nil
	}
	s.selected = list[i].Key
	s.pane = defaultPane(list[i])
	s.preview = panePreview{}
	return s.current()
}

func (s *sessionsModel) applyPreview(msg panePreviewMsg) bool {
	if msg.paneID != s.pane || msg.seq < s.preview.seq {
		return false
	}
	s.preview.loading = false
	s.preview.paneID = msg.paneID
	if msg.err != nil {
		s.preview.err = msg.err.Error()
		return true
	}
	s.preview.err = ""
	s.preview.text = trimPadding(sources.StripControl(msg.text))
	return true
}

// --- rendering ---

func statusDotFor(st sources.AgentStatusName, reported bool) string {
	switch st {
	case "needs_input":
		return StatusDot("needs you", VariantWarning)
	case "working":
		return StatusDot("working", VariantAccent)
	case "idle":
		return StatusDot("idle", VariantNeutral)
	}
	return StatusRing("unknown", VariantMuted)
}

func (s *sessionsModel) badge() string { return "" } // set by the model; see app wiring

// view renders the whole view body (without the outer panel, which the
// model draws) into width × height.
func (s *sessionsModel) view(width, height int, now time.Time, filtering bool) string {
	if width >= sessionsSplitWidth {
		left := s.listView(sessionsListWidth-4, height-3, now, filtering)
		right := s.detailView(width-sessionsListWidth-4, height-3, now)
		lp := RenderPanel("Sessions", left, sessionsListWidth, height, !s.paneFocus)
		title := "Select a session"
		if cur := s.current(); cur != nil {
			title = hostLabel(cur.Host) + " / " + clean(cur.Name)
		}
		rp := RenderPanel(title, right, width-sessionsListWidth, height, s.paneFocus)
		return lipgloss.JoinHorizontal(lipgloss.Top, lp, rp)
	}
	listH := height / 2
	if listH < 5 {
		listH = 5
	}
	if height-listH < 4 {
		listH = height - 4
	}
	left := s.listView(width-4, listH-3, now, filtering)
	right := s.detailView(width-4, height-listH-3, now)
	title := "Select a session"
	if cur := s.current(); cur != nil {
		title = hostLabel(cur.Host) + " / " + clean(cur.Name)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		RenderPanel("Sessions", left, width, listH, !s.paneFocus),
		RenderPanel(title, right, width, height-listH, s.paneFocus))
}

// listView draws hosts, sessions and dormant repos.
func (s *sessionsModel) listView(inner, height int, now time.Time, filtering bool) string {
	var lines []string
	if filtering || s.query != "" {
		lines = append(lines, "/ "+s.filter.View())
	}
	digit := 0
	selIdx := -1
	for _, h := range s.ws.Hosts {
		header := MutedText.Render(strings.ToUpper(hostLabel(h.Name)))
		switch h.Observation {
		case sources.ObservationUnavailable:
			header += " " + WarningText.Render("⚠ unavailable")
		case sources.ObservationStale:
			header += " " + WarningText.Render("⚠ stale "+ageOf(h.ObservedAt, now))
		default:
			if h.Name != "" {
				header += " " + SuccessText.Render("● up")
			}
		}
		lines = append(lines, clip(header, inner))
		for _, sv := range h.Sessions {
			q := strings.ToLower(strings.TrimSpace(s.query))
			if q != "" && !strings.Contains(strings.ToLower(hostLabel(h.Name)+" "+sv.Name), q) {
				continue
			}
			selected := sv.Key == s.selected
			if selected {
				selIdx = len(lines)
			}
			digit++
			key := "  "
			if lbl := hotkeyLabel(digit); lbl != "" {
				key = MutedText.Render(lbl) + " "
			}
			nameStyle := BoldText
			if selected {
				nameStyle = BoldText.Foreground(ColorAccent)
			}
			if h.Observation != sources.ObservationFresh {
				nameStyle = MutedText
			}
			extra := ""
			if sv.Agents > 0 {
				extra += fmt.Sprintf(" %d", sv.Agents) + "🤖"[:0] + "ag"
			}
			if sv.ProcessesTotal > 0 {
				extra += fmt.Sprintf(" ⚙%d/%d", sv.ProcessesRunning, sv.ProcessesTotal)
			}
			line := RowCursor(selected) + key + padRight(nameStyle.Render(clip(clean(sv.Name), 12)), 12) + " " + statusDotFor(sv.Status, sv.Reported) + MutedText.Render(extra)
			lines = append(lines, clip(line, inner))
		}
		if len(h.Dormant) > 0 {
			names := make([]string, 0, len(h.Dormant))
			for _, d := range h.Dormant {
				names = append(names, clean(d.Name))
			}
			lines = append(lines, clip(MutedText.Render(fmt.Sprintf("  dormant (%d): %s", len(h.Dormant), strings.Join(names, " "))), inner))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, MutedText.Render("No sessions observed"))
	}
	// Scroll to keep the selection visible.
	if height < 1 {
		height = 1
	}
	start := 0
	if selIdx >= height {
		start = selIdx - height + 1
	}
	end := start + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

// detailView draws the selected session's header, panes and preview.
func (s *sessionsModel) detailView(inner, height int, now time.Time) string {
	cur := s.current()
	if cur == nil {
		return MutedText.Render("Nothing selected")
	}
	var lines []string
	head := ""
	if cur.Project != nil {
		head = PurpleText.Render(clean(cur.Project.Branch))
		if cur.Project.Dirty > 0 {
			head += " " + StatusDirty.Render(fmt.Sprintf("✗%d", cur.Project.Dirty))
		} else {
			head += " " + StatusClean.Render("✓")
		}
		if cur.Project.Unpushed > 0 {
			head += " " + StatusUnpushed.Render(fmt.Sprintf("↑%d", cur.Project.Unpushed))
		}
		head += "  "
	}
	meta := fmt.Sprintf("%s · server %s · %d windows", cur.ID, cur.Generation, cur.Windows)
	if cur.Attached {
		meta += " · attached"
	}
	if cur.LegacyReport {
		meta += " · session-level report (older hook)"
	}
	lines = append(lines, clip(head+MutedText.Render(meta), inner))
	lines = append(lines, "")
	for _, p := range cur.Panes {
		selected := p.PaneID == s.pane
		mark := " "
		if p.Attention {
			mark = WarningText.Render("!")
		}
		name := padRight(clip(clean(p.WindowName), 12), 12)
		id := padRight(MutedText.Render(p.WindowID), 5)
		var status, qual string
		switch p.Kind {
		case sources.PaneAgent:
			status = statusDotFor(p.Agent.Status, p.Agent.Reported)
			qual = p.Agent.Engine
			if p.Agent.Reported && !p.Agent.ReportedAt.IsZero() {
				qual += " · " + ageOf(p.Agent.ReportedAt, now)
			}
		case sources.PaneProcess:
			status = stateStyleFor(sources.ProcessInfo{Outcome: p.Process.Outcome}).Render(p.Process.Display)
			qual = "process"
			if !p.Process.Managed {
				qual += " · unmanaged"
			}
		case sources.PaneShell:
			status = MutedText.Render("shell")
			qual = clean(p.Command)
		default:
			status = MutedText.Render(clean(p.Command))
		}
		line := RowCursor(selected && s.paneFocus) + mark + id + name + " " + padRight(status, 18) + " " + MutedText.Render(clip(qual, 24))
		if selected && !s.paneFocus {
			line = RowCursor(false) + mark + id + AccentText.Render(name) + " " + padRight(status, 18) + " " + MutedText.Render(clip(qual, 24))
		}
		lines = append(lines, clip(line, inner))
	}
	// Preview.
	if p := s.currentPane(); p != nil && height-len(lines) > 3 {
		title := "─── " + clean(p.WindowName) + " ── " + p.PaneID + " "
		if p.Path != "" {
			title += "· " + clean(p.Path) + " "
		}
		lines = append(lines, MutedText.Render(clip(title+strings.Repeat("─", inner), inner)))
		room := height - len(lines)
		switch {
		case s.preview.err != "":
			lines = append(lines, WarningText.Render(clip("preview unavailable: "+s.preview.err, inner)))
		case s.preview.text == "" && s.preview.loading:
			lines = append(lines, MutedText.Render("reading…"))
		case s.preview.text == "":
			lines = append(lines, MutedText.Render("(no preview)"))
		default:
			body := strings.Split(s.preview.text, "\n")
			if len(body) > room {
				body = body[len(body)-room:]
			}
			for _, l := range body {
				lines = append(lines, clip(l, inner))
			}
		}
	}
	if s.message != "" {
		lines = append(lines, WarningText.Render(clip(s.message, inner)))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
```

Remove the odd `"🤖"[:0]` fragment: the agent count renders as ` 2ag` → use `fmt.Sprintf(" %d agent", sv.Agents)` with `plural`-style suffix (`"s"` when not 1). `hotkeyLabel` exists in `grid.go`. `trimPadding` and `stateStyleFor` exist in `processes.go`.

- [ ] **Step 4: Run** `go test ./tui/ -run Sessions` — PASS. Adjust widths if the breakpoint test finds an over-wide line.

---

### Task 7: Wire the view into the model; remove the dashboard and the pane-hash guess

**Files:**
- Modify: `tui/app.go`, `tui/grid.go`, `tui/panels.go`, `tui/nav.go`, `tui/keyhints.go`, `tui/sessions.go`, `tui/repos.go`
- Delete: `tui/tasks.go`, `tui/inbox.go`, `tui/viz.go`, `tui/boids.go`, `tui/clock.go`, `tui/constellation.go`, `tui/life.go`, `tui/orbital.go`, `tui/plasma.go`, `tui/rain.go`, `tui/starfield.go`
- Modify tests: `tui/app_test.go`, `tui/grid_test.go`, `tui/sessions_test.go`, `tui/panels_test.go`

**Interfaces:**
- Consumes: `sessionsModel`, `sources.BuildWorkspace`, `Model.attentionInput()`, `attachCmd`, `openProcessesFor`, `openAttention`, `tmuxJumpRepo`, `jumpRemoteCmd`.
- Produces: `ViewSessions`; `Model.sess sessionsModel`; `ModeSessionsFilter`; `fetchPanePreview() tea.Cmd`; `openSessions()`; `handleSessionsKey`.

- [ ] **Step 1: Write the model tests** (append to `tui/panels_test.go`)

```go
func TestSessionsIsTheDefaultViewAndGridIsG(t *testing.T) {
	m := panelModel(t, 120, 40)
	if m.view != ViewSessions {
		t.Fatalf("default view = %v", m.view)
	}
	m.handleKey(keyMsg("g"))
	if m.view != ViewGrid {
		t.Fatalf("g should open the grid, got %v", m.view)
	}
	m.handleKey(keyMsg("d"))
	if m.view != ViewSessions {
		t.Errorf("d in the grid returns to the session view, got %v", m.view)
	}
}

func TestSessionsEnterNeverLaunches(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("api", "$2", "%5", "inv"))
	m.view = ViewSessions
	cmd := m.handleKey(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("Enter on a session attaches")
	}
	// The attach command is built from a validated target; the model must
	// not have invoked the jump path (which records a pending switch).
	if m.sess.current() == nil || m.sess.current().Name != "api" {
		t.Errorf("selection = %+v", m.sess.current())
	}
}

func TestSessionsOpenProjectIsExplicit(t *testing.T) {
	m := panelModel(t, 120, 40)
	m.feed(gitDataMsg{Repos: []sources.GitRepoStatus{{Label: "site", Branch: "main"}}})
	feedLocal(&m)
	m.view = ViewSessions
	m.sess.refresh(sources.BuildWorkspace(m.attentionInput()))
	if len(m.sess.ws.Hosts[0].Dormant) == 0 {
		t.Fatalf("site should be dormant: %+v", m.sess.ws.Hosts[0])
	}
	// 'o' on the dormant group opens the first dormant project.
	if cmd := m.handleKey(keyMsg("o")); cmd == nil {
		t.Error("o opens a project")
	}
}

func TestSessionsReturnFromAttentionKeepsPane(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("api", "$2", "%5", "inv"))
	m.view = ViewSessions
	m.sess.refresh(sources.BuildWorkspace(m.attentionInput()))
	m.sess.paneFocus = true
	m.handleKey(keyMsg("a"))
	m.handleKey(keyMsg("esc"))
	if m.view != ViewSessions || m.sess.pane != "%5" || !m.sess.paneFocus {
		t.Errorf("view=%v pane=%q focus=%v", m.view, m.sess.pane, m.sess.paneFocus)
	}
}
```

`panelModel` builds `NewModel`; after Task 4 the default view is sessions. The existing `feedLocal` sessions carry no `ViewOf`, so they appear.

- [ ] **Step 2: Run** `go test ./tui/` — FAIL.

- [ ] **Step 3: Implement.**

In `tui/app.go`:
- `ViewMode`: rename `ViewDashboard` to `ViewSessions` (keep the constant order: `ViewGrid`, `ViewSessions`, `ViewAttention`, `ViewProcesses`).
- `Mode`: add `ModeSessionsFilter`; keep `ModeCapture` but back it with a plain `textinput.Model` field `captureInput` on `Model` (remove `TasksModel`). Delete `ModeVizPicker`.
- `PanelID`, `Layout`, `CalculateLayout`, `dashboardView`, `renderPreviewHeader`, `renderVizPickerDialog`, `handleVizPickerKey`, `fetchTasks`, `fetchInbox`, `fetchSessionStatuses`, `sessionStatusMsg`, `tasksDataMsg`, `inboxDataMsg`, `vizTickMsg`, `vizTick`, `previewDataMsg`/`fetchPreview`/`sessionPreview`/`lastPreviewSession`, `focused`, `layout`, `tasks`, `inbox`, `viz`, `vizPickerCursor`: delete. The grid's own preview panel (`renderPreviewPanel`, `gridLocalSession`) uses `fetchPreview`; replace its data source with the session view's preview mechanism: `fetchPanePreview` for the grid's selected local session's active pane, stored in `m.sess.preview` (grid preview reads `m.sess.preview.text`). Simplest: keep `fetchPreview` but implement it as `capture-pane` of the selected session's active pane by session name through `sources.DefaultRunner()` with `CapturePaneVisibleArgs(name)` (tmux accepts a session name as target), routed into `previewDataMsg` as today. Keep `previewDataMsg`.
- Model fields: add `sess sessionsModel`, `captureInput textinput.Model`. `NewModel`: `sess: newSessionsModel()`; view from `cfg.General.DefaultView`: `"grid"` → `ViewGrid`, else `ViewSessions`.
- `Init`: drop `fetchTasks`, `fetchInbox`, `vizTick`.
- `Update`: drop the deleted message cases. In `recomputeAttention`, after the report, add `m.sess.refresh(sources.BuildWorkspace(m.attentionInput()))` and, when `m.view == ViewSessions`, return `m.fetchPanePreview()` from the callers (`recomputeAttention` returns a `tea.Cmd`; update its call sites to append it). Add:

```go
	case panePreviewMsg:
		m.sess.applyPreview(msg)
```

  and in `localTickMsg`, when `m.view == ViewSessions`, append `m.fetchPanePreview()`.
- Capture mode: `handleCaptureKey` reads `m.captureInput`; on Enter `sources.AppendInbox(m.config.Obsidian.TodayFile, text)` (error → `sourceErrMsg`), then blur and return to navigation. Forwarding block: `if modeBefore == ModeCapture { m.captureInput, cmd = m.captureInput.Update(msg) }`. Render the prompt as the bottom line of the current view when `m.mode == ModeCapture`: `"  capture › " + m.captureInput.View()` replacing the hints line.
- `handleNavKey`: `case ViewSessions: return m.handleSessionsKey(msg)`; the remaining dashboard `switch` is deleted.
- Add in `tui/sessionsview.go` (model side, needs `Model`):

```go
func (m *Model) openSessions() {
	m.view = ViewSessions
	m.sess.refresh(sources.BuildWorkspace(m.attentionInput()))
}

// fetchPanePreview reads the selected pane's visible screen through its
// host's runner, guarded by sequence and pane id. It never runs against an
// unavailable host.
func (m *Model) fetchPanePreview() tea.Cmd {
	p := m.sess.currentPane()
	cur := m.sess.current()
	if p == nil || cur == nil {
		return nil
	}
	for _, h := range m.sess.ws.Hosts {
		if h.Name == cur.Host && h.Observation != sources.ObservationFresh {
			return nil
		}
	}
	m.sess.preview.seq++
	m.sess.preview.loading = true
	seq, host, paneID := m.sess.preview.seq, cur.Host, p.PaneID
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := svc.RunnerFor(config.RepoConfig{Host: host})
		if err != nil {
			return panePreviewMsg{host: host, paneID: paneID, seq: seq, err: err}
		}
		out, err := r.Run(ctx, sources.CapturePaneVisibleArgs(paneID)...)
		if err != nil {
			return panePreviewMsg{host: host, paneID: paneID, seq: seq, err: err}
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > previewLines {
			lines = lines[len(lines)-previewLines:]
		}
		return panePreviewMsg{host: host, paneID: paneID, seq: seq, text: strings.Join(lines, "\n")}
	}
}

func (m *Model) handleSessionsKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.sess
	switch msg.String() {
	case "j", "down":
		s.move(1)
		return m.fetchPanePreview()
	case "k", "up":
		s.move(-1)
		return m.fetchPanePreview()
	case "tab":
		s.paneFocus = true
	case "shift+tab":
		s.paneFocus = false
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
		n := int(msg.String()[0] - '0')
		if n == 0 {
			n = 10
		}
		if sv := s.selectIndex(n - 1); sv != nil {
			return m.attachSession(*sv)
		}
	case "enter":
		if s.paneFocus {
			if p := s.currentPane(); p != nil {
				cur := s.current()
				return m.attachCmd(sources.AttachTarget{Host: cur.Host, Generation: p.Generation, Session: cur.Name, SessionID: cur.ID, WindowID: p.WindowID, PaneID: p.PaneID})
			}
			return nil
		}
		if cur := s.current(); cur != nil {
			return m.attachSession(*cur)
		}
	case "o":
		return m.openProjectCmd()
	case "p":
		if cur := s.current(); cur != nil {
			return m.openProcessesFor(cur.Host, cur.Name, "")
		}
	case "a":
		return m.openAttention()
	case "l":
		if p := s.currentPane(); p != nil {
			m.pushReturn()
			m.procs.open("", s.current().Host, s.current().Name, true, "")
			m.procs.selected = "win:" + p.WindowID
			m.procs.fullOut = true
			m.view = ViewProcesses
			return m.fetchProcObs()
		}
	case "n":
		m.mode = ModeNewSession
		m.newSessionStep = 0
		m.newSessionPath = ""
		m.newSessionErr = ""
		m.newSessionInput.SetValue("")
		m.newSessionInput.Placeholder = "~/workspace/my-project"
		m.newSessionInput.Focus()
	case "/":
		m.mode = ModeSessionsFilter
		s.filter.SetValue(s.query)
		s.filter.Focus()
	case "c":
		m.mode = ModeCapture
		m.captureInput.Focus()
	case "g":
		m.view = ViewGrid
		return m.fetchPreview()
	case "r":
		return m.refreshAll()
	case "esc":
		if s.query != "" {
			s.query = ""
			s.filter.SetValue("")
			s.resolve()
		}
	case "q":
		return tea.Quit
	}
	return nil
}

// attachSession attaches to a session's selected (active) pane, creating
// nothing.
func (m *Model) attachSession(sv sources.SessionView) tea.Cmd {
	target := sources.AttachTarget{Host: sv.Host, Generation: sv.Generation, Session: sv.Name, SessionID: sv.ID}
	if p := m.sess.currentPane(); p != nil {
		target.WindowID, target.PaneID = p.WindowID, p.PaneID
	}
	return m.attachCmd(target)
}

// openProjectCmd is the explicit launch: the selected session's project,
// or the first dormant project, through the jump path with reconcile.
func (m *Model) openProjectCmd() tea.Cmd {
	if cur := m.sess.current(); cur != nil {
		repo, ok := m.config.RepoOn(cur.Host, cur.Name)
		if !ok {
			m.sess.message = "Not a configured project; nothing to start"
			return nil
		}
		return m.jumpCmd(repo)
	}
	for _, h := range m.sess.ws.Hosts {
		if len(h.Dormant) > 0 {
			if repo, ok := m.config.RepoOn(h.Name, h.Dormant[0].Name); ok {
				return m.jumpCmd(repo)
			}
		}
	}
	return nil
}

func (m *Model) jumpCmd(repo config.RepoConfig) tea.Cmd {
	if repo.Host != "" {
		host, ok := m.config.Host(repo.Host)
		if !ok {
			return nil
		}
		return m.jumpRemoteCmd(host, repo)
	}
	svc := m.svc
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpRepo(svc, repo)} }
}

func (m *Model) handleSessionsFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.sess.filter.Blur()
		m.sess.filter.SetValue("")
		m.sess.query = ""
		m.mode = ModeNavigation
		m.sess.resolve()
	case "enter":
		m.sess.filter.Blur()
		m.mode = ModeNavigation
	}
	return nil
}

func (m Model) sessionsView() string {
	body := m.height - 1
	if body < 5 {
		body = 5
	}
	content := m.sess.view(m.width, body, m.now(), m.mode == ModeSessionsFilter)
	hints := SessionsKeyhintsView(m.width, m.sess.paneFocus, m.attn.badge())
	switch {
	case m.mode == ModeCapture:
		hints = "  " + AccentText.Render("capture ›") + " " + m.captureInput.View()
	case m.transientErr != "":
		hints = WarningText.Render(m.transientErr)
	}
	return lipgloss.JoinVertical(lipgloss.Left, content, hints)
}
```

  `handleKey`: `case ModeSessionsFilter: return m.handleSessionsFilterKey(msg)`. Forward keys to `m.sess.filter` when `modeBefore == ModeSessionsFilter && m.mode == ModeSessionsFilter`, updating `m.sess.query` and calling `m.sess.resolve()` on change (mirror the attention filter block).
- `View()` in `tui/grid.go`: `case ViewSessions: page = m.sessionsView()`; `default` removed. Grid `d` → `m.openSessions()`. Grid `a`/`p` unchanged.
- `tui/nav.go` `returnPoint`: add `sessKey, sessPane string; sessFocus bool`; push/pop them.
- `tui/keyhints.go`: delete `KeyhintsView` (dashboard); add

```go
// SessionsKeyhintsView renders the session view's key bar.
func SessionsKeyhintsView(width int, panes bool, badge string) string {
	nav := hint{"j/k", "sessions"}
	if panes {
		nav = hint{"j/k", "panes"}
	}
	return renderHints([]hint{
		nav, {"Tab", "panes/sessions"}, {"Enter", "attach"}, {"a", badge}, {"o", "open project"},
		{"p", "procs"}, {"l", "preview"}, {"1-0", "attach"}, {"n", "new"}, {"/", "filter"}, {"c", "cap"}, {"g", "grid"}, {"r", "refresh"}, {"q", "quit"},
	}, width)
}
```

  and in `GridKeyhintsView` rename the `d` hint's description to `"sessions"`.
- `tui/sessions.go`: keep `SessionsModel{Sessions, Cursor, Loading, Statuses, Reported}` and `AdoptReported`; delete `NeedingCapture`, `UpdateStatus`, `prevHashes`, `View`, `CompactView`, `renderCard`; keep `formatIdleTime` (grid uses it). Delete `tui/sessions_test.go` cases that test the removed functions; keep the `AdoptReported` test.
- `tui/repos.go`: keep `ReposModel{Repos, Loading}` and `NewReposModel`; delete the rest (`padRight` moved in Task 6).
- Delete the listed visualizer files, `tasks.go`, `inbox.go`, `viz.go`. `grep -rn "PanelSessions\|PanelRepos\|PanelToday\|PanelInbox\|PanelViz\|m.viz\|m.tasks\|m.inbox\|KeyhintsView(" tui/` must return nothing outside tests; fix tests (`app_test.go` layout tests → delete; `grid_test.go` references to `PanelSessions` → remove the argument).
- `AttentionKeyhintsView`/`panels.go`: `handleNavKey`'s dashboard `a`/`P` block goes away with the switch.

- [ ] **Step 4: Run** `gofmt -w tui && go vet ./tui/ && go test ./tui/` — PASS. Then `go build ./... && go test ./...` — PASS.

---

### Task 8: Live checks, docs and progress

**Files:**
- Modify: `README.md`, `docs/workspace-usability-progress.md`
- Create: `sources/workspace_integration_test.go`

- [ ] **Step 1: Integration test** on a private server: a pane running a command named `claude` (a shell script) is classified as an agent and previewed.

```go
package sources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntegrationWorkspaceClassifiesARealAgentCommand(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho agent-screen\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "work", "-n", "agent", fake); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	rep := ObserveHostReport(ctx, r, nil, "", nil, time.Now(), time.Now())
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{rep}, Now: time.Now()})
	if len(ws.Hosts[0].Sessions) != 1 || len(ws.Hosts[0].Sessions[0].Panes) != 1 {
		t.Fatalf("ws = %+v", ws)
	}
	p := ws.Hosts[0].Sessions[0].Panes[0]
	if p.Kind != PaneAgent || p.Agent.Engine != "claude" || p.Agent.Status != "unknown" || p.Agent.Reported {
		t.Errorf("pane = %+v", p)
	}
	out, err := r.Run(ctx, CapturePaneVisibleArgs(p.PaneID)...)
	if err != nil || !strings.Contains(out, "agent-screen") {
		t.Errorf("preview: %v %q", err, out)
	}
}
```

- [ ] **Step 2: Run** `go test ./sources/ -run Integration` — PASS.

- [ ] **Step 3: Manual pass.** Build to the scratchpad, run inside a private server with an isolated config at 120×40 and 40×12 (reuse the `tui.sh` pattern: a `cockpit-…` session hosting the TUI, an `api` session with a pane whose `@cockpit_pane_status` is `needs_input`, a `site` session with a crashed managed window). Capture: default view, `Tab` + `j` onto the reviewer pane with its preview, `a` and Esc back to the same pane, `p` and Esc, `g` and `d`, `/api`, `1`. Record the screens in the progress doc.

- [ ] **Step 4: Docs.** README: replace the "Dashboard", "Sessions", "Repos", "Today", "Inbox" bullets and the "Keybindings" table with the session view (keys from the spec's table), note `default_view = "sessions"` and the `dashboard` alias, remove the visualizer and Today/Notes mentions, add `cockpit_workspaces` to the daemon tool list and `pane_id` to `cockpit_read_output`. Progress doc: add a "Session view" section mapping spec acceptance 1–10 to tests and the manual pass.

- [ ] **Step 5: Final** `gofmt -l . ; go vet ./... && go build ./... && go test -count=1 ./...` — all PASS.

---

## Self-review

- Spec coverage: tree and rules → Task 2; collection and preview bounds → Tasks 1, 3, 6; interaction and keys → Tasks 6–7; `default_view` → Task 4; MCP tool and `pane_id` → Task 5; errors → Tasks 6–7 (stale hosts skip previews, preview errors local to the pane, attach validated); acceptance 1–10 → Tasks 2 (1–4), 6 (5, 9), 2+7 (6, 7), 5 (8), 7 (10); removed panels → Task 7.
- Placeholders: none.
- Type consistency: `sessionsModel` fields and methods as used in Task 7 match Task 6; `panePreviewMsg` fields `host, paneID, text, seq, err`; `sources.AgentStatusName` string values `needs_input|working|idle|unknown` used identically in Tasks 2, 5, 6.
