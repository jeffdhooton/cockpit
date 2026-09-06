# Attention queue

Date: 2026-09-05  
Status: proposed  
Parent: [Workspace usability](2026-09-05-workspace-usability.md)

## Problem and success

Cockpit already computes Signals, but a person still has to locate the relevant
project, machine, and window. The queue turns an observed problem into a direct
route to the work. Success means finding the next actionable item without
scanning every project or entering host grids one at a time.

## Interaction

- `a` opens Attention from the grid or dashboard. It always starts with all
  configured hosts plus local sessions, even when opened inside a host grid.
- A persistent `Attention N` hint shows the number of fresh actionable items.
  Unavailable sources have a separate count; unknown coverage is not zero work.
- `j/k` or arrows select, `/` filters by project/host/kind, `Enter` performs the
  selected row's displayed primary action, `r` refreshes, and `Esc` returns to
  the exact previous view, host, and selection. `q` closes this view, not Cockpit.
- `Tab` switches between **Needs attention** and **Housekeeping**. Unpushed
  commits and old sessions belong in Housekeeping and do not inflate the badge.
- Queue refresh preserves selection by item ID. If the selected item resolves,
  show a resolved placeholder until the next navigation key; a queued Enter
  must not suddenly act on the row that moved into its place.
- At narrow widths, use two lines per item and a full-screen detail view.
  Host/project and the action remain visible; details wrap or truncate safely.

Illustrative content (ages mean time observed by this Cockpit instance):

```text
Attention 3                            1 host unavailable

  mini/api · reviewer      Needs input           4m
    Open agent
  local/site · dev         Exited with code 1    2m
    Inspect process
  local/site · main        CI failed             8m
    View check details

  mini-2                   Unavailable — last checked 3m ago

Enter open · / filter · Tab housekeeping · r refresh · Esc back
```

## Included items and destinations

| Order | Evidence | Primary action |
|---|---|---|
| 1 | Fresh reported agent needs input | Attach to the reporting pane when identity is proven; otherwise open session details. |
| 2 | Configured process exited with nonzero/unknown code and is not intentionally stopped | Open Processes with that process selected and output visible. |
| 3 | Failed CI run from the supported GitHub source | Open details showing repository, checked branch, run ID and URL. |
| 4 | Configured Hermes gateway is observed down | Open gateway details with its host and last check; shell navigation stays explicit. |
| Housekeeping | Observed unpushed commits or stale sessions, when enabled | Open project/session details; attach only if a session exists. |

Unavailable hosts/sources appear in a separate coverage section. They do not
produce one fresh failure per cached child process. They remain visible even
when filters hide normal items.

V1 keeps existing CI branch coverage (`main`) and labels it explicitly; it must
not describe that result as the selected feature branch's check. Add run ID,
URL, branch, and fetch outcome to the source. Remote CI remains unsupported and
is shown as not checked, not passing. In CI details, `o` opens a validated HTTPS
run URL with the local platform opener; if unavailable, display a selectable
URL. Opening the queue itself never launches a browser.

Do not infer successful completion from idle status, or create alerts by
matching arbitrary occurrences of "error" in old terminal output.

## Identity and freshness

Introduce a shared attention record with:

- stable item ID, kind, priority, host and project key;
- title/detail, typed target and allowed primary action;
- evidence source (`hook`, `process`, `github`, `hermes`, `git`);
- observation outcome (`fresh`, `stale`, `unavailable`), observation timestamp,
  and first-observed timestamp for the current occurrence;
- source-specific identity: tmux server generation/session/window/pane,
  configured process key and live generation, or repository/run ID.

Display names and concatenated subject strings are never executable targets.
Deduplicate by source identity and kind, including host. Renaming a live window
changes its label, not its item identity. Replacing a pane/run creates a new
identity. Ages are locally measured observation ages; remote event freshness
continues to use the remote clock as the current host poll does.

Sort by the table above, then oldest first-observed, then stable ID. First
observation times are in memory and reset on TUI restart; do not imply a durable
event history. Repeated polls update one item rather than append duplicates.

Only a successful authoritative read can clear an issue. If a host or source
fails, retain its last-known entries as stale in the coverage/details section
and disable actions requiring fresh targets. If an agent hook expires, show
status unknown; expiration is not a successful answer to its prompt. The empty
state is "No attention items observed" with coverage information, never an
unqualified "Everything is healthy" when data is missing.

## Narrow prerequisite: independently reported panes

Current hooks store one status and a window name on a session. A later event
from another agent can overwrite a blocked agent. An actionable queue must not
ship with an implicit claim that this aggregate covers all agents.

Upgrade reporting to store separate records for live panes and aggregate those
records for existing tiles. Resolve the hook's own pane explicitly from its
environment, not whichever pane is selected in a tmux client. Bind reports to
the actual server/pane and agent invocation generation; a late report from an
exited invocation cannot recolor its replacement. Preserve the current bounded
payload, target-scoped authentication, 500ms total hook budget, and fail-open
hook behavior. No prompt, response, or terminal transcript enters the report.

Tile rollup is fresh needs-input first, then working, then idle. Unknown panes
do not become healthy merely because another pane reports. The queue contains
one item per fresh blocked reporting pane. A new working/idle report from that
same invocation clears it; another pane's event does not.

Continue reading old session-level reports as one **session report** when no
new pane reports cover that session. Show reduced coverage and offer session
details; do not guess an exact pane from a reused name. Mixed-version remote
hosts keep working and receive a doctor recommendation to update hooks.

Implementation must verify current supported engine hook behavior before
changing integrations. This spec does not introduce new provider event names
or promise detection of prompts the existing integrations cannot observe.

## Navigation and collection architecture

Extend the shared source model in `sources/signals.go`; keep signal derivation
pure and separate occurrence tracking in the UI. Collect from existing local
and remote polling results, preserving per-source errors and observation times.
Refresh requests coalesce and follow existing host backoff; no per-row polling
loop or additional daemon is needed.

Add an attach-existing operation distinct from project entry/reconciliation.
It revalidates the host, server generation and target IDs immediately before
selection. A missing or replaced target returns "This target is no longer
available" and refreshes. It never falls back to creating the named session.
Remote navigation may create a local SSH view window as the existing transport
does, but must not create a remote session or run project commands.

Use the process panel for dead processes. Launching its inspection path must
not invoke `ReconcileProcesses`, which would erase the crash by restarting it.

Add `cockpit_attention` as a read-only MCP tool returning records and coverage
with `schema_version: 1`; it has no navigation or dismissal side effects.
Use the same collector/derivation as the TUI. Preserve the existing
`cockpit_signals` envelope and legacy fields, adapting the same facts; avoid a
second implementation that disagrees about freshness or host identity.

## Acceptance

1. Two blocked panes in one session produce two items. A third pane completing
   work clears neither; a late event cannot apply to a replacement invocation.
2. Local and remote projects with the same label have distinct items and routes.
3. Selecting a blocked pane, switching the active tmux pane externally, and
   pressing Enter still reaches the reporting pane.
4. Renaming, removing, or replacing the target between poll and action either
   reaches the same validated identity or fails visibly without creation.
5. Opening a crash item preserves the dead pane and its output. No start or
   restart call occurs during inspection or queue refresh.
6. Repeated polls produce no duplicates; selection is stable while new urgent
   rows arrive; resolving a selected row cannot redirect a pending action.
7. SSH, tmux, and GitHub read failures cannot clear unresolved items or show
   passing status. Reconnect reconciles against fresh evidence.
8. CI details identify the checked branch and exact run. Remote unsupported
   coverage and legacy hook coverage are visible.
9. TUI and MCP derive equivalent records from equivalent observations.
10. At 40 columns and 12 rows, navigation, primary actions, coverage, and return
    remain usable. Control sequences in names/details cannot affect the TUI.

## Deferred

Notifications, snooze/dismiss controls, durable history, AI summaries,
automatically answering agents, and automatic repairs. An item clears through
new evidence rather than a user declaring the underlying issue resolved.
