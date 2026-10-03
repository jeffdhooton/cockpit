# Workspace usability: attention, processes, and setup

Date: 2026-09-05  
Status: proposed; specification requested, implementation not started

## Outcome

Cockpit should make three things straightforward: find work that needs a person,
control a project's declared processes, and understand why a new installation is
not working. These improvements should serve the existing personal workflow and
make a first installation useful without help from the author.

This package specifies:

1. [Attention queue](2026-09-05-attention-queue-design.md)
2. [Process controls](2026-09-05-process-controls-design.md)
3. [Doctor and guided setup](2026-09-05-doctor-setup-design.md)

The designs make concrete default choices for review. They do not authorize a
release, changes to the user's installed hooks/configuration, or a commercial
product launch.

## Shared product rules

- Existing tmux sessions remain useful independently of Cockpit. No replacement
  terminal runtime or mandatory remote daemon is introduced.
- Navigation and inspection do not start or restart project commands. Existing
  project launch-on-jump remains a distinct action; attention links and process
  inspection use an attach-only path.
- A failed read is unknown state, never proof that a process stopped, a session
  disappeared, or every issue resolved.
- Host, tmux server, session, window, and pane identity are separate from display
  names. Local and remote projects with the same label cannot collide.
- The TUI and MCP share process operations and signal derivation. Neither
  reimplements launch rules independently.
- Background refresh never blocks keyboard input. Late responses cannot replace
  newer state or apply to a different selection.
- Optional integrations stay optional. A machine without Obsidian, GitHub,
  Hermes, or installed coding agents can have a healthy Cockpit installation.

## Source baseline and discovered prerequisites

The current working tree, including existing uncommitted UI changes, was read
on 2026-09-05. Code takes precedence over the older design documents.

| Existing surface | Consequence for these features |
|---|---|
| `sources/signals.go` returns kind/subject/detail | Queue entries need typed targets, freshness, and stable identity. |
| `daemon/hooks.go` writes session-level status | Independent blocked panes need a small reporting upgrade; legacy reports remain usable with reduced precision. |
| `sources/processes.go` reconciles auto-start windows on project entry | Stop needs explicit intent that reconciliation respects. |
| `sources/tmux_args.go` lists names and indexes | Process actions need stable window/pane IDs and protection against stale targets. |
| `daemon/tools.go` holds a single runner | Remote process controls require explicit host routing, not just new UI buttons. |
| `sources/github.go` checks the latest run on `main` and omits run URLs | V1 must label this coverage honestly and carry run identity for navigation. |
| Some session/process/GitHub reads collapse errors into empty results | Correct those paths before using them as evidence for queue clearing or process creation. |
| `cmd/root.go` creates a config template | Preserve the existing command while adding optional guided setup. |
| `README.md`, `go.mod`, release workflow, installer | Reconcile installation/version documentation and exercise a fresh install before wider distribution. |

## Delivery order

1. **Foundations:** reliable read outcomes, stable targets, host-aware process
   service, and tests for duplicate labels and delayed responses.
2. **Process controls:** explicit stop intent, shared operations, and the process
   panel. This makes the queue's dead-process destination useful immediately.
3. **Attention queue:** pane reporting upgrade, collection/coverage model,
   actionable queue, and MCP representation.
4. **Doctor and setup:** diagnostic core followed by guided config creation.
   This can be implemented independently once the shared identity and error
   vocabulary is settled.

Each feature has its own acceptance bar. Do not hold useful process controls
behind notifications, a setup wizard, or broader agent orchestration.

## Package acceptance

Demonstrate on an isolated local tmux server and an explicitly designated SSH
test host, never by stopping the user's active work:

1. A blocked agent on a remote project appears in the global queue; opening it
   reaches the correct existing pane without launching a command.
2. A crashed configured process opens in the process panel with readable output.
3. A manually stopped auto-start process stays stopped after project navigation
   and TUI/daemon restart while its tmux session survives.
4. A dropped SSH connection produces unavailable state and prevents mutations;
   reconnect does not duplicate a command or erase unresolved issues.
5. A new user creates a minimal config, discovers existing sessions, and gets
   actionable diagnostics without installing optional integrations.

Implementation should run focused unit tests, isolated integration tests for
process lifecycle and targeting, the full Go suite, and a manual TUI pass at
desktop and narrow terminal sizes. These are future implementation gates; no
runtime behavior has been changed or validated by this specification task.

## Deferred

Desktop/push notifications, snooze/history persistence, worktree management,
agent conversation recovery, process supervision, arbitrary shell command
editing, team accounts, and a plugin system are outside this package.
