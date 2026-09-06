# Session view

Date: 2026-09-05
Status: proposed
Parent: [Workspace usability](2026-09-05-workspace-usability.md)

## Problem and success

Cockpit's grid is a wall of project tiles and its dashboard is five panels,
three of which (Today, Notes, visualizer) say nothing about the work. Neither
shows what Cockpit is actually for: tmux sessions holding coding agents,
processes and shells, on this machine and on others. Herdr shows the shape
this should take — panes roll up to a workspace, and status flows up from
the agent — but Cockpit already has more precise facts than screen scraping:
hook-reported pane records, process observations with ownership, and host
identity. This view puts those facts in one place.

Success means opening Cockpit and seeing, without scanning tiles, which
sessions have agents, which agent needs you, what each session is running,
and what a selected pane is showing right now — for local and remote hosts
alike, at desktop and phone sizes — and the same picture being available to
agents through one read-only MCP tool.

Decisions taken during design: borrow Herdr's model, not its runtime (tmux
stays underneath; no Herdr dependency); sessions are the unit; the view
replaces the dashboard; capture stays, task and note display goes; agents
are identified by hook record or foreground command, never by screen
heuristics; Enter is attach-only everywhere in this view; the session view is
the default and the grid stays on `g`; the MCP tool ships with the view.

## The workspace tree

A pure derivation in `sources`, `BuildWorkspace(in AttentionInput)
Workspace`, over the same per-host observations `DeriveAttention` reads, so
the view, the queue and the tool cannot disagree about freshness, identity
or coverage.

```text
Workspace
  Hosts []HostView          local first, then config order
    Name, Observation (fresh|stale|unavailable), ObservedAt, Err
    Sessions []SessionView  live sessions
      Key ("mini/api"), Name, ID, Generation, Attached, LastUsed, Windows
      Project *ProjectSummary   branch, dirty count, unpushed, when a repo matches
      Status  needs_input > working > idle > unknown   (rollup over agent panes)
      Agents int, ProcessesRunning int, ProcessesTotal int
      Panes []PaneView      window index order, then pane order
        WindowID, WindowName, PaneID, Generation, Active
        Kind    agent | process | shell | other
        Command (foreground), Path (current directory)
        Agent   *AgentView: Engine (claude|codex|other), Status, Reported,
                ReportedAt, Invocation
        Process *ProcessView: Name, Outcome, Display, Managed, ExitCode
    Dormant []DormantView   configured repos with no session: Key, Project
  Coverage []Coverage       identical to the attention report's records
```

Classification rules:

- **Agent**: the pane has a hook record (`@cockpit_pane_*`), or its foreground
  command is `claude` or `codex`. A record always wins. A command-only agent
  has status `unknown` and `Reported = false`, rendered dimmed. Needs-input
  is never inferred; the fail-closed rule from the attention queue holds.
- **Process**: the pane's window id appears in the project's process
  observation. `Outcome`, `Display`, `Managed` and `ExitCode` are copied from
  that observation verbatim, so this view and the process panel use the same
  words.
- **Shell**: the foreground command is a known shell (`zsh`, `bash`, `fish`,
  `sh`). Everything else is **other**, shown by its command name.
- A pane that is both an agent and a managed process (an agent launched as a
  configured process) is an agent for status and a process for controls.

Session rollup is the same `RollupPaneStatus` the tile uses: fresh
needs-input first, then working, then idle; unknown otherwise. Legacy
session-level reports (no pane records) roll up as the session's status with
`Reported` true and a coverage note, exactly as the queue treats them.

Sorting: within a host, sessions order by rollup band (needs_input, working,
idle, unknown), then by name. Order is recomputed every poll; selection is
held by session key and pane id, so a row moving cannot move the cursor.

A host whose read failed keeps its last tree with `Observation = stale`;
every pane in it renders dimmed and its actions are disabled. A host never
polled is `unavailable` with no sessions. Neither is drawn as empty.

## Collection

The pane format gains foreground command, current path and active flag. The
window name stays last because it may contain the separator; the path is
carried as `#{q:pane_current_path}` and unquoted, the command is a single
token. Local and remote hosts share `ObserveHost`, so remote panes carry
the same fields through the existing host poll.

Previews: selecting a pane requests one bounded `capture-pane -p -t %id`
of the visible screen (no scrollback, at most 40 lines) through that host's
runner. Requests carry a sequence and the pane id; a reply for another pane
or an older sequence is dropped. Preview refresh follows the local refresh
interval while the view is open, and never runs against a host whose last
read failed. Control sequences are stripped before rendering.

The per-session `capture-pane` hash loop that guessed working/idle is
removed, together with the dimmed "guess" rendering. Status is reported or
unknown. This is the one behaviour change outside the new view.

## Interaction

`ViewSessions` replaces the dashboard. `default_view` accepts `sessions`
(default for new configs), `grid`, and `dashboard` as an alias of
`sessions`; existing configs keep working.

Layout at 90 columns or more: a 32-cell session column beside the detail.
Below 90: session list above the detail. At 40×12 the detail shows only the
pane list; `l` opens the preview full screen and Esc returns to the same
pane.

Session column: hosts as muted headers (local first) with reachability and
the age of the last successful read; one line per session with status dot,
name, agent count and process count; a `DORMANT (n)` group of configured
repos with no session; the `Attention N · M unavailable` badge pinned at
the bottom. Live sessions are numbered `1–9,0` in list order for the digit
hotkeys, renumbered per poll like the grid.

Detail: header with project key, branch and dirty/unpushed, session id and
generation, attached state and window count; one line per pane with its
window id, name, kind-specific status text and a short qualifier (engine and
age for agents, policy for processes, command for others); a rule; the
preview of the selected pane. Panes that back an attention item carry a
leading `!`.

| Key | Action |
|---|---|
| `j/k`, arrows | Move between sessions (session list focused) or panes (pane list focused) |
| `Tab` / `Shift-Tab` | Move focus between the session list and the pane list |
| `1–9`, `0` | Select and attach to the numbered live session's active pane |
| `Enter` | Attach to the selected pane, or to the session's active pane from the session list. Validated; creates nothing |
| `o` | Open project: create the session if missing and reconcile auto-start processes (the former jump). Also for dormant rows |
| `p` | Process panel for the selected session |
| `a` | Attention queue |
| `l` | Full-screen preview of the selected pane |
| `n` | New session dialog (existing) |
| `/` | Filter sessions by host or name; coverage stays visible |
| `c` | Capture one line to the Today file (existing); no task display |
| `g` | Grid view; `g` or `d` in the grid returns here |
| `r` | Refresh every source |
| `q` | Quit Cockpit (the session stays alive, as today) |
| `Esc` | Clear filter or return from a sub-view to the same session and pane |

Returning from the attention queue, the process panel or the preview lands
on the same session and pane through the existing return stack. Background
refresh never blocks input; a late reply cannot replace newer state.

## MCP tool

`cockpit_workspaces` is read-only, takes no arguments, and returns the tree
above with `schema_version: 1`:

```json
{
  "schema_version": 1,
  "observed_at": "2026-09-05T15:05:00-04:00",
  "hosts": [{
    "name": "mini", "observation": "fresh", "observed_at": "…",
    "sessions": [{
      "key": "mini/api", "name": "api", "session_id": "$3", "generation": "4060-1788634739",
      "attached": false, "windows": 5, "status": "needs_input", "agents": 2,
      "processes": {"running": 1, "total": 2},
      "project": {"branch": "main", "dirty": 85, "unpushed": 0},
      "panes": [{
        "window_id": "@2", "window_name": "reviewer", "pane_id": "%557", "active": true,
        "kind": "agent", "command": "claude", "path": "/Users/jclaw/workspace/api",
        "agent": {"engine": "claude", "status": "needs_input", "reported": true,
                  "reported_at": "…", "invocation": "3f9a"},
        "process": null
      }]
    }],
    "dormant": [{"key": "mini/docket", "project": {"branch": "main", "dirty": 0, "unpushed": 0}}]
  }],
  "coverage": [{"source": "tmux", "scope": "halo2", "observation": "unavailable", "detail": "…"}]
}
```

The daemon collects with the same bounded per-host walk as
`cockpit_attention` and feeds `BuildWorkspace`; the two tools are stateless
and independent. No pane content is included: `cockpit_read_output` gains an
optional `pane_id` argument so an agent can read a pane it found here. The
tool exposes what tmux exposes (names, commands, paths) and never
environment values, prompts or output.

## Errors

- A host read failure keeps the last tree as stale; nothing on it can be
  attached to or previewed until a fresh read.
- A preview failure marks that pane's preview unavailable and touches nothing
  else.
- Attach uses the validated attach-only path; a replaced or removed target
  reports "This target is no longer available" and refreshes.
- `o` on a project whose host is unreachable fails closed, as the jump does
  today; it is the only key in this view that can start anything.

## Acceptance

1. Two hosts with a session of the same name show two sessions with distinct
   keys, and Enter on each reaches its own pane.
2. A pane with a hook record is an agent with the reported status; a pane
   running `claude` with no record is an agent with unknown status, dimmed;
   a `zsh` pane is a shell. Needs-input never appears without a fresh record.
3. A managed process pane shows the process panel's exact display text and
   exit code; an unmanaged window of the same name shows as unmanaged.
4. A session whose agent starts needing input rises to the top of its host on
   the next poll while the cursor stays on the session it was on.
5. Selecting pane A, then B, then receiving A's late preview leaves B's
   preview in place; a reply with an older sequence is dropped.
6. A host that stops answering keeps its last sessions marked stale, with
   previews and attach disabled, and the coverage line names it; reconnect
   replaces the tree without duplicates.
7. Enter never creates a session or runs a command; `o` does both and only
   for the selected project.
8. `cockpit_workspaces` returns the same sessions, panes, kinds and statuses
   the TUI derives from the same observations, with `schema_version: 1`, and
   runs no mutating tmux command.
9. At 40×12 the session list, pane list, attach, `l` preview and return are
   usable; at 90 columns the two-column layout appears; control sequences in
   names, commands, paths or previews cannot affect the terminal.
10. The removed dashboard panels leave no dead keys: `c` still captures,
    `V`/`v`/`P`/`.` are gone from hints and handlers, and `default_view =
    "dashboard"` still starts Cockpit.

Tests: fixture-driven `BuildWorkspace` cases for every rule above; TUI tests
for selection survival, focus switching, late-preview rejection, both
breakpoints and stripping; a daemon test for the tool schema and TUI
equivalence; a private-server integration test that classifies and previews
a pane running a command named `claude`; a manual pass at desktop and phone
sizes.

## Deferred

Sending input to a pane from this view, killing panes or windows, drag-style
reordering, per-pane notifications, Herdr integration as a second source,
and deriving the grid from the workspace tree.
