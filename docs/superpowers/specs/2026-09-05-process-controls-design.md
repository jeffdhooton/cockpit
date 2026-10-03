# Process controls

Date: 2026-09-05  
Status: proposed  
Parent: [Workspace usability](2026-09-05-workspace-usability.md)

## Problem and success

Configured processes already launch in tmux windows and the daemon can control
them, but people must remember tmux window commands. Worse, a process explicitly
stopped today can restart on the next project entry because reconciliation sees
an absent auto-start window. The process panel should make inspection and
intentional lifecycle control obvious and predictable.

## Entry and layout

- `p` opens Processes for the selected project/session from the grid. `P` opens
  it from the dashboard's Sessions or Repos panel, preserving dashboard `p`
  (Pomodoro) and `R` (clock reset). Hints are context-sensitive.
- The attention queue can open it with a particular process selected.
- A host tile is not a project: `p` there explains "Open a project to manage its
  processes." An unmanaged session can show its windows for inspection.
- Opening this panel only reads. A dormant project's declared commands appear
  without creating a session or starting an auto-start process.
- Show configured processes first, in config order, then other windows ordered
  by index. Other windows are labeled **Unmanaged** and cannot be restarted or
  stopped from this panel in V1.

```text
Processes · mini/api                         Connected

  dev       Running          starts with project
> worker    Stopped by you    starts with project
  tests     Not started      manual

Other windows
  shell     Unmanaged
  reviewer  Unmanaged

Enter attach · l output · s start · x stop · R restart · Esc back
```

On a wide terminal, the selected row has an output preview and details beside
or below it. On a narrow terminal, `l` opens full-screen output; Esc returns to
the same row. Show actual available actions only. Selection stays bound to
identity through refresh. `q` closes the panel, not Cockpit or any process.

## Actions

| Input | Behavior |
|---|---|
| Enter | Attach to an existing validated window/pane. Absent process: explain that it must be started first. |
| `l` | Read the last 200 output lines; permit an explicit load up to 2,000 lines. |
| `s` | Start the selected configured command; no-op with feedback if already running. |
| `x` | Stop a running configured command after a confirmation naming host/project/process. |
| `R` | Restart a running configured command after the same scoped confirmation; a dead/stopped row offers Start instead. |
| `A` | For an eligible unmarked legacy window matching a configured process, open the explicit adoption review described below. |
| `r` | Refresh this project's process observations. |
| Esc | Cancel confirmation/output, then return to the originating view. |

Confirmation defaults to Cancel and states that the tmux window's running work
will be terminated; this is not a graceful application shutdown guarantee.
There is no stop-all/restart-all action in V1. Start needs no confirmation:
selecting Start is already the explicit request to run the displayed configured
command. Never execute a command read from logs or an inferred package script.

Output refresh is bounded by the normal local refresh interval and follows the
tail only while already at the bottom. Pausing or scrolling preserves position.
Capture output only for the visible selection; cancel superseded reads. Strip
terminal control sequences and render text; keep output in memory only. Hide
environment values, and do not include commands or output in routine operation
logs. The user may explicitly inspect their configured command in details.

## Observed state and desired behavior

Separate observation from the user's stop override:

| Display | Meaning |
|---|---|
| Running | The managed pane is live; this does not prove application readiness. |
| Exited (code N) | A retained pane exited; output remains readable. A nonzero/unknown code contributes an attention item. |
| Completed | A retained configured command exited with code 0; no crash alert. |
| Not started | A successful read proves no managed window exists and no stop override exists. |
| Stopped by you | Stop override exists and no managed pane is running. |
| Unknown / Unavailable | Inspection failed or an action outcome cannot be established. |

Show ready/error regex matches as **recent output matches**, not authoritative
health. Old scrollback is not evidence that a newly launched server is ready.

For compatibility, existing MCP `state` values remain `running`, `dead`, and
`not_started` for successful observations. Add `desired_state`, `exit_code`,
`outcome`, stable IDs, and observation metadata. Failed inspection returns a
structured error/unknown result, not a fabricated legacy state. The TUI uses
the richer display states above.

## Stop stays stopped

Use a versioned per-process stop override on the owning tmux session. Its key
is derived from the configured process name; it contains no command or secret.
It survives TUI/daemon restart and is visible to all Cockpit clients using that
tmux session, including clients connected over SSH. It is session-lifetime
state, not a second process registry.

- Stop records the override **before** terminating the validated window.
- Reconciliation skips overridden processes, including missing or dead ones.
- Start explicitly clears the override as part of its launch transaction.
- Restart means keep running; it cannot leave an old stop override behind.
- A session removed externally, a tmux server restart, or a machine reboot
  removes the override. Details explain this boundary. Editing `auto_start`
  in config remains the way to change the permanent startup policy.
- Older Cockpit binaries do not understand stop overrides. All clients that
  reconcile a managed session must be upgraded before relying on this behavior;
  doctor reports discoverable version mismatches. Do not claim an old client
  can be prevented from issuing its existing tmux commands.
- A process started externally while overridden is shown truthfully as Running
  with automatic startup paused. Reconciliation neither stops nor duplicates it.
- Removing and later re-adding the same process name in a surviving session
  retains its override until explicit Start; changing labels creates a new key.

If termination fails after the override is recorded, show "Automatic startup
paused; process may still be running" and re-inspect. Do not report Stopped.
If override persistence fails, do not terminate: otherwise the user's stop
would silently be undone at their next project entry.

On Start of an absent session, create only the project's shell and the selected
command. Starting one row must not launch all other auto-start commands.
Ordinary project entry retains existing reconciliation for non-overridden
commands; this feature does not introduce a continuous restart supervisor.

## Shared service and target safety

Implement shared inspect/start/stop/restart operations over the existing Runner
seam, then route the TUI and existing MCP tools through that service. The TUI
does not require the HTTP daemon just to control local or remote processes.

Resolve projects by `RepoConfig.Key()` and select the correct local/SSH runner.
Qualified remote project arguments must never execute on the local runner.
Keep bare local project names compatible; never reinterpret an ambiguous bare
name as an arbitrary remote project. No remote daemon is required for process
controls; SSH and remote tmux remain sufficient.

Extend observations to include server generation, session ID, window ID, pane
IDs, and process ownership. A window name alone is insufficient proof that its
contents are a configured process. Mark windows Cockpit creates; unmarked
pre-existing windows with matching names remain inspection-only until an
explicit **Adopt existing window** action shows the target and obtains consent.
Adoption must not execute or replace a process. Name collisions block Start
with an explanation rather than producing a duplicate window.

Configured commands continue to occupy one managed window. If a user splits a
managed window or changes its contents such that ownership is uncertain, allow
inspection/attach but disable Stop/Restart and explain the conflict. V1 does
not kill neighboring panes to restart one configured command.

Serialize lifecycle operations and reconciliation per project on the owning
host/tmux server, across TUI and daemon processes. A mutex inside one process
is insufficient. Re-read live identity, ownership and stop intent under that
lock before mutation; include generation checks in execution so a reused name
or stale selection cannot target replacement work. Lock acquisition is bounded
and does not leave an indefinite lock after a failed/crashed caller.

All mutation entry points, including existing MCP operations, must use this
path. Overlapping Start requests result in one launch. While an operation is
pending, disable duplicate UI actions for that row and show progress.

A lost SSH response after dispatch is an **unknown outcome**. Do not retry the
mutation automatically. Re-read when connectivity returns; explicit retry is
available only after resolving the target state, or with a displayed conflict
that requires the user to inspect existing work. A timeout is not proof of
non-execution. Preserve partial success details (for example, session created
but process launch failed).

Some current read helpers swallow command errors. Correct these before reusing
them: only verified absence may permit creation; permission errors, missing
binaries, malformed output, and transport errors are distinct failures.

## Acceptance

1. Opening a dormant project lists its commands with zero launch/reconcile calls.
2. Start of one command creates at most its shell and command; repeated and
   concurrent TUI/MCP Start requests do not duplicate it.
3. Stop survives project re-entry and TUI/daemon restart while tmux survives.
   Explicit Start resumes it; unrelated configured processes retain their policy.
4. An instant command failure preserves output and exit code. Successful exit
   appears Completed and does not generate a crash alert.
5. Stop/Restart target the validated managed window despite index changes;
   reused names, unmanaged collisions and split windows cannot kill other work.
6. A legacy matching window requires explicit adoption; adoption does not launch
   anything. The normal shell and ad-hoc agents remain inspection-only.
7. A remote project sharing a local label uses the remote runner. An unreachable
   host or unreadable window list never results in local launch or duplication.
8. Fault injection after stop-intent write and after remote dispatch produces
   truthful partial/unknown outcomes with no automatic destructive retry.
9. Returning from attach/output restores project, process selection, and the
   originating attention/grid view. A late output response cannot replace the
   selected process's preview.
10. MCP and TUI observe the same stop override and lifecycle rules. Existing
    local arguments remain valid; incompatible safety cases return clear errors.

Use fake runners for failure/race cases and private tmux servers for actual
process lifecycle, ownership, multi-client locking, and cleanup tests. Remote
end-to-end acceptance requires a designated test host; mocks alone do not prove
the SSH outcome behavior.

## Deferred

Bulk actions, arbitrary command editing, supervision/restart policies, graceful
shutdown protocols, OS-wide process management, durable stop state across
reboots, and restarting unmanaged agent conversations.
