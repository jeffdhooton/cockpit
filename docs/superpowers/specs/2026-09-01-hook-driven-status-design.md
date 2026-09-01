# Hook-Driven Status — Design

Date: 2026-09-01
Status: **draft, awaiting review** — decisions in §8 need your call before planning

Adapted from `BUILD_APP_SUBSYSTEMS.md` §2.6 and §6 ("hook-driven status instead
of output scraping"), re-grounded in what Cockpit actually is.

## 1. The problem

Cockpit decides whether a session is working or idle by hashing its pane
content between polls:

```go
// tui/sessions.go:24 — UpdateStatus
hash := sha256(content)
if hash == prev { idle } else { working }
```

It is a guess, and it is wrong in both directions:

- **False idle.** An agent thinking for 20 seconds without printing reads as
  idle. This is the common case at exactly the moment you care.
- **False working.** Any animated element — a spinner, a clock, a progress bar,
  a blinking status line — reads as working forever.
- **No third state.** The state you actually scan for across eight sessions is
  *"this one is waiting for me."* Pane hashing cannot see it at all. An agent
  waiting on a permission prompt is indistinguishable from an idle one.

It is also the most expensive poll Cockpit runs. `fetchSessionStatuses`
(`tui/app.go:1264`) issues one `capture-pane` per session every 5 seconds — nine
subprocesses per tick on this machine, purely to produce a guess.

## 2. What replaces it

Claude Code fires hooks at real lifecycle boundaries. A hook posts a small
structured event to Cockpit's daemon; the daemon records the state; the grid
reads it. Status stops being inferred and starts being reported.

The schema is already in use on this machine — `~/.claude/settings.json` wires
`scry hook pre-search` the same way, which is the pattern to follow:

```json
{ "hooks": { "Stop": [ { "hooks": [
  { "type": "command", "command": "/Users/jeff/.local/bin/cockpit hook status" }
] } ] } }
```

### Event → state mapping

| Hook event | State | Why |
|---|---|---|
| `UserPromptSubmit` | `working` | You just gave it something to do |
| `PreToolUse` | `working` | Definitely alive and acting |
| `Notification` | **`needs_input`** | Permission prompt or idle-waiting notice |
| `Stop` | `idle` | The turn ended |
| `SessionEnd` | *(cleared)* | Falls back to the pane-hash guess |

`needs_input` is the whole point. It is the state the current implementation
cannot represent, and the one worth walking across the room for.

## 3. Where the state lives

This is the load-bearing decision, because Cockpit's daemon is deliberately
stateless — every answer is re-read live from tmux, git, and markdown. Hook
events are inherently stateful: they arrive over time and must be remembered.

**Decision: store status in tmux itself, as session and window user options.**

```sh
tmux set-option -t <session> @cockpit_status needs_input
tmux set-option -t <session> @cockpit_status_at <unix seconds>
```

Verified working, including the read path Cockpit already uses:

```
$ tmux list-sessions -F '#{session_name}|#{@cockpit_status}|#{@cockpit_status_at}'
cockpit-opt|working|1788250998
docket||                          ← unset sessions report empty, not an error
```

Why this and not a ring buffer or a state file:

- **The daemon stays stateless.** It writes through to tmux and reads back. No
  in-memory map to lose, no file to prune or corrupt.
- **The lifetime is exactly right.** Status lives as long as the session and
  dies with it. No stale entry for a session that ended last Tuesday.
- **It survives a daemon restart.** `cockpit daemon stop && start` loses
  nothing, because the daemon was never the owner.
- **Reading is free.** The TUI already calls `tmux list-sessions -F …` every
  tick. Status becomes two more format fields on a call it already makes —
  which *removes* the N `capture-pane` subprocesses per tick rather than adding
  anything.

**Gotcha, verified.** Window options inherit from the session when unset:

```
zsh|session-level     ← inherited, no window option of its own
dev|window-level      ← its own value shadows the session's
```

So a process window with no status of its own reports the session's. Write
`@cockpit_status_window` alongside the status and compare it to the window name
when reading; a mismatch means inherited, not reported.

## 4. Components

```
Claude Code
  │  fires hook, writes event JSON to stdin
  ▼
cockpit hook status            (new subcommand — cmd/hook.go)
  │  resolves target, signs, POSTs; 500ms timeout; always exits 0
  ▼
POST /hooks/status             (new route on the existing daemon)
  │  verify token · allowlist fields · bound sizes
  ▼
tmux set-option @cockpit_status …
  │
  ▼
tmux list-sessions -F …        (the TUI's existing poll)
```

Four small pieces. No new server, no new dependency, no new store.

### 4.1 `cockpit hook status`

Reads the hook payload on stdin and posts it. Three rules:

1. **Never block the agent.** 500ms total timeout, and exit 0 unconditionally —
   including when the daemon is down, the port moved, or the JSON is
   unrecognisable. A hook that hangs or fails hangs or fails Claude Code. This
   is the single most important property in the design.
2. **Resolve its own target.** Prefer `$COCKPIT_STATUS_TARGET` (injected into
   processes Cockpit launches). Falling back to
   `tmux display-message -p '#{session_name}:#{window_name}'` covers every
   session you started by hand — which is most of them, and most of the value.
3. **No-op cleanly outside Cockpit.** No tmux, no daemon, no config → exit 0
   silently. The hook must be safe to leave installed globally forever.

### 4.2 Daemon endpoint

`POST /hooks/status` on the existing loopback server. Body capped at 16KB.
Fields pass a fixed allowlist with per-field caps; unknown fields are dropped,
not forwarded. This is Build's `FIELD_SPECS` discipline (§2.6), and the reason
is concrete: Claude's `Stop` event can carry an entire assistant message, and
none of it belongs in a tmux option.

| Field | Cap | Use |
|---|---|---|
| `hook_event_name` | 32 | Maps to a state |
| `session_id` | 64 | Correlation only |
| `target` | 128 | `session:window` |
| `tool_name` | 64 | Optional detail on the tile |
| `message` | 200 | `Notification` text, truncated |

Everything else is discarded. Alias spellings (`session_id` / `sessionId`)
normalise to one name.

### 4.3 Authentication

Loopback-only is the real boundary, but an unauthenticated write endpoint that
sets tmux options is still worth closing. Keep the daemon stateless with a
derived token rather than a stored one:

```
token = HMAC-SHA256(key, target)
key    = ~/.config/cockpit/status-key   (32 random bytes, mode 0600, made on first use)
```

The daemon recomputes and compares in constant time. Nothing is stored per run.

**Say plainly what this does and does not do.** Any process running as you can
read the key, so this is not a defence against local code. It stops a stray
request from another user or a browser on the machine from writing status, and
it means the endpoint cannot be driven by accident. Loopback binding does the
rest.

### 4.4 TUI

`GetTmuxSessions` gains two format fields. `ClaudeStatus` gains `NeedsInput`.
Tiles render reported status differently from guessed status — a filled marker
for reported, hollow for inferred — so the display never presents a guess as a
fact. `fetchSessionStatuses` and its per-session `capture-pane` loop are deleted
for sessions that report, and kept as the fallback for those that do not.

## 5. Degradation, named

Per Build's rule that nothing silently does less than it claims:

| Situation | Behaviour |
|---|---|
| Hook installed, daemon up | Reported status; tile marks it reported |
| Hook not installed | Pane-hash guess; tile marks it inferred |
| Daemon down | Hook exits 0, nothing recorded, falls back to guess |
| Non-Claude agent (codex, a shell) | Guess, marked inferred |
| Status older than 10 minutes | Treated as stale, falls back to guess |

The last row matters: a crashed agent stops sending events, and a `working`
option would otherwise persist forever. `@cockpit_status_at` makes staleness
detectable, which is why it is written alongside the status.

## 6. Testing

- **`cockpit hook status`:** exits 0 with no daemon, no tmux, malformed stdin,
  and an empty payload. Respects the timeout. Resolves target from env, then
  from tmux, then gives up quietly.
- **Endpoint:** each event maps to the right state; unknown fields dropped;
  oversized fields truncated; bad token rejected; oversized body rejected.
- **Store:** the exact `set-option` argv; parsing status out of `list-sessions`;
  inherited-vs-own window values; staleness.
- **Integration (real tmux):** post an event, read the state back through
  `list-sessions`, confirm it dies with the session.
- **End to end:** a real Claude Code session with the hook installed moving
  through working → needs_input → idle.

## 7. Scope

Roughly 500–600 lines including tests: `cmd/hook.go`, `daemon/hooks.go`,
`sources/status.go`, plus TUI edits. Two to three commits. No new dependencies.

## 8. Decisions I need from you

I have made a recommendation on each; none are locked.

1. **Hook installation.** Recommend `cockpit hook install` writing into
   `~/.claude/settings.json` (backed up, idempotent) — the same treatment
   `register-mcp.sh` got. The alternative is documenting a snippet you paste.
   Automatic editing of your Claude settings is the kind of thing you may want
   to keep manual.

2. **Fallback target resolution.** Recommend yes — the hook resolves its own
   tmux target when Cockpit did not launch the process. Without it, only
   processes Cockpit started report status, which is a small fraction of your
   sessions. With it, one global hook covers everything. The cost is that
   Cockpit reports on sessions it does not own.

3. **Keep or drop the pane-hash fallback.** Recommend keep, marked as inferred.
   Dropping it is simpler and more honest, but blanks the status column for
   every non-Claude session until hooks cover them.

4. **Does `needs_input` earn a signal?** Recommend yes — an agent waiting on you
   is more actionable than an unpushed commit, so it belongs at the top of the
   Signals order. Say if you would rather keep Signals to repository state.

5. **Codex.** I have not checked whether Codex has an equivalent hook surface.
   If it does, the same endpoint serves it with a different mapping. Worth a
   look before building, or worth explicitly deferring.

## 9. What this does not do

- No remote hosts. Build tunnels hook traffic from a remote box over SSH
  (§2.6); that is downstream of the unmade decision about whether Cockpit
  watches a second machine.
- No transcript, tool arguments, or message bodies. Only enough to colour a
  tile.
- No history. Current state only — the previous state is not kept, because
  keeping it would mean owning a store.
