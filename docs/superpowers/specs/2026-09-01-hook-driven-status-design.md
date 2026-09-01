# Hook-Driven Status — Design

Date: 2026-09-01
Status: **decided** — §8 resolved, ready to plan

This is the first of two related projects. Remote hosts follow it, and the
reverse-tunnel status transport for a remote agent (§2.6 of the source
document) is designed in there rather than retrofitted here. §9 says what that
leaves out of this one.

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

Claude Code and Codex both fire hooks at real lifecycle boundaries. A hook
posts a small structured event to Cockpit's daemon; the daemon records the
state; the grid reads it. Status stops being inferred and starts being
reported.

The schema is already in use on this machine — `~/.claude/settings.json` wires
`scry hook pre-search` the same way, which is the pattern to follow:

```json
{ "hooks": { "Stop": [ { "hooks": [
  { "type": "command", "command": "/Users/jeff/.local/bin/cockpit hook status" }
] } ] } }
```

### Event → state mapping

Both engines report. Codex CLI 0.151.0 defines its hook events in
`codex-rs/config/src/hook_config.rs`, and the set is a superset of the one this
design needs:

> `PreToolUse` · `PermissionRequest` · `PostToolUse` · `PreCompact` ·
> `PostCompact` · `SessionStart` · `SessionEnd` · `UserPromptSubmit` ·
> `SubagentStart` · `SubagentStop` · `Stop`

| State | Claude Code | Codex | Why |
|---|---|---|---|
| `working` | `UserPromptSubmit` | `UserPromptSubmit` | You just gave it something to do |
| `working` | `PreToolUse` | `PreToolUse` | Definitely alive and acting |
| **`needs_input`** | `Notification` | `PermissionRequest` | It is blocked on you |
| `idle` | `Stop` | `Stop` | The turn ended |
| *(cleared)* | `SessionEnd` | `SessionEnd` | Falls back to the pane-hash guess |

`needs_input` is the whole point. It is the state the current implementation
cannot represent, and the one worth walking across the room for.

**The two `needs_input` events are not the same fact.** Codex's
`PermissionRequest` fires for exactly one reason. Claude's `Notification` is
broader — a permission prompt *or* an idle-waiting notice — so it is the
coarser signal of the two, and a tile driven by Claude will show `needs_input`
in some cases where a Codex tile would not.

Note what the table does *not* justify: the four shared rows use identical
names for identical meanings, and the two that diverge use disjoint names, so a
single merged lookup would produce correct results today. The per-engine table
is not needed to disambiguate. It is worth keeping anyway for two narrower
reasons — the payloads differ in shape, so the field allowlist is engine-aware
regardless; and "this event name means the agent is blocked" is a claim about
one engine's behaviour at one version, which is better written down as such
than merged into a list that hides whose claim it was. A future engine is then
a table entry rather than a new code path.

An event name that appears in no table sets no state, rather than falling
through to a guess.

### Installing into each engine

Claude Code takes a `hooks` array in `~/.claude/settings.json`. Codex takes
matcher groups in `~/.codex/config.toml` with `{ type = "command", command,
timeout, async }` handlers.

Codex adds a step that Claude does not have, and it is the kind that fails
silently: **a newly written hook lands `untrusted` and does not fire until it
is trusted, and trust is pinned to a hash of the hook's configuration**
(`currentHash: "sha256:…"`, and `codex --dangerously-bypass-hook-trust` exists
precisely to skip the check). Editing the command later re-untrusts it. So
writing the config is not the same as installing the hook, and an installer
that stops at the write reports success for something inert. `hook install`
reads back what it wrote and says plainly whether the hook is live or awaiting
trust — the preflight-before-effect habit applied to installation.

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
Claude Code / Codex
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
   unrecognisable. A hook that hangs or fails hangs or fails the agent that
   called it. This is the single most important property in the design.
2. **Resolve its own target.** Prefer `$COCKPIT_STATUS_TARGET` (injected into
   processes Cockpit launches). Falling back to
   `tmux display-message -p '#{session_name}:#{window_name}'` covers every
   session you started by hand — which is most of them, and most of the value.
3. **No-op cleanly outside Cockpit.** No tmux, no daemon, no config → exit 0
   silently. The hook must be safe to leave installed globally forever.

**Do not trust the hook's environment.** A lesson paid for while building the
daemon: under launchd, `PATH` was `/usr/bin:/bin:/usr/sbin:/sbin` — no Homebrew,
so no tmux — and with no locale set at all, tmux silently replaced the tab
separators in `-F` format output with `_`, which made every query parse to
nothing. Both were invisible because the errors were swallowed as "no sessions".
The hook runs in whatever environment the agent hands it, so it inherits the
same exposure: resolve tmux by absolute path, keep using the printable field
separator, and never treat a failed lookup as an empty result.

### 4.2 Daemon endpoint

`POST /hooks/status` on the existing loopback server. Body capped at 16KB.
Fields pass a fixed allowlist with per-field caps; unknown fields are dropped,
not forwarded. This is Build's `FIELD_SPECS` discipline (§2.6), and the reason
is concrete: Claude's `Stop` event can carry an entire assistant message, and
none of it belongs in a tmux option.

The route goes behind the same `guard` the JSON-RPC routes now use
(`daemon/mcp.go`): no `Origin` header, `application/json` required, body
bounded. The guard is per-route rather than middleware today, so adding
`/hooks/status` means calling it there too — a new write endpoint that skips it
would reopen exactly what the guard closed.

| Field | Cap | Use |
|---|---|---|
| `engine` | 16 | Selects the event table; `claude` or `codex` |
| `hook_event_name` | 32 | Maps to a state within that table |
| `session_id` | 64 | Correlation only |
| `target` | 128 | `session:window` |
| `tool_name` | 64 | Optional detail on the tile |
| `message` | 200 | Notification or permission text, truncated |

`engine` is supplied by the installed hook command (`cockpit hook status
--engine codex`), not sniffed from the payload. The installer knows which file
it wrote to; the endpoint should not have to guess from field shapes.

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
read the key, so this is not a defence against local code. It stops a request
from another user on the machine and means the endpoint cannot be driven by
accident. The browser case is already handled a layer up by the guard in §4.2,
so the token is not carrying that weight; loopback binding does the rest.

### 4.4 TUI

`GetTmuxSessions` gains two format fields. The status type gains `NeedsInput`.
`fetchSessionStatuses` and its per-session `capture-pane` loop are deleted for
sessions that report, and kept as the fallback for those that do not.

**The marker scheme carries two facts, and one glyph pair cannot.** This design
originally proposed filled `●` for reported status and hollow `○` for inferred.
`StatusRing` (`tui/styles.go`) already claims that pair for a different axis —
`○` means no session exists, `●` means one does — deliberately, so that absence
stays legible without colour. Both axes are worth showing and they are
independent:

| | Session exists | No session |
|---|---|---|
| **Status reported** | live, known | *(impossible)* |
| **Status guessed** | live, inferred | — |

**Settled: shape keeps carrying existence, and dimming carries confidence.**
Shape is the better carrier for existence because that is the fact a reader
needs when colour is gone, so `○`/`●` stay as `StatusRing` defines them.
Reported-versus-inferred moves to a second channel: the status label is dimmed
when the status is a guess. That costs no width, and on a terminal without
styling it degrades to "looks the same" rather than "means the wrong thing" —
which a third glyph would not, since it would spend a column and invite
confusion with the ring.

The status type is currently named `sources.ClaudeStatus` (`tui/grid.go:20`),
which stops being true the moment Codex reports into it. `AgentStatus` is the
honest name. It is mechanical but not tiny — 22 non-test references — so it
wants its own commit rather than riding along inside a behaviour change. The
engine, when known, becomes a field on it.

## 5. Degradation, named

Per Build's rule that nothing silently does less than it claims:

| Situation | Behaviour |
|---|---|
| Hook installed, daemon up | Reported status; tile marks it reported |
| Hook not installed | Pane-hash guess; tile marks it inferred |
| Hook written but untrusted (Codex) | Nothing fires; `hook install` says so rather than reporting success |
| Daemon down | Hook exits 0, nothing recorded, falls back to guess |
| Neither engine (a shell, vim, a build) | Guess, marked inferred |
| Unrecognised event name | Ignored; no state is invented for it |
| Status older than 10 minutes | Treated as stale, falls back to guess |

The last row matters: a crashed agent stops sending events, and a `working`
option would otherwise persist forever. `@cockpit_status_at` makes staleness
detectable, which is why it is written alongside the status.

## 6. Testing

- **`cockpit hook status`:** exits 0 with no daemon, no tmux, malformed stdin,
  and an empty payload. Respects the timeout. Resolves target from env, then
  from tmux, then gives up quietly.
- **Endpoint:** each engine's events map to the right state; an event name
  belonging to the other engine, or to neither, sets no state at all; unknown
  fields dropped; oversized fields truncated; bad token rejected; oversized
  body rejected.
- **Install:** merging into a `~/.claude/settings.json` that already holds
  other hooks preserves them; running twice adds nothing the second time; a
  backup is written first; the Codex path reports untrusted rather than
  claiming success.
- **Store:** the exact `set-option` argv; parsing status out of `list-sessions`;
  inherited-vs-own window values; staleness.
- **Integration (real tmux):** post an event, read the state back through
  `list-sessions`, confirm it dies with the session.
- **End to end:** a real session on each engine, hook installed, moving through
  working → needs_input → idle.

## 7. Scope

Roughly 700–800 lines including tests: `cmd/hook.go`, `daemon/hooks.go`,
`sources/status.go`, plus TUI edits. Three to four commits. No new dependencies.

The estimate grew from 500–600 once both engines were in scope. The extra is
almost entirely installation, not reporting: two config formats to merge
idempotently, and Codex's trust state to read back and report. The event
tables themselves are data.

## 8. Decisions

All five are settled. Recorded with their reasoning, because the reasoning is
what a later reader needs.

1. **`cockpit hook install` edits the config files.** Backed up, idempotent,
   safe to run twice — the treatment `register-mcp.sh` already got. A hook you
   forget to install is a feature that silently does not exist. It covers both
   engines, and because of Codex's trust step (§2) it verifies rather than
   assumes: the command's last act is to read back what it wrote and report
   whether the hook is live.

2. **The hook resolves its own tmux target.** Without it only Cockpit-launched
   processes report, which is a small fraction of the sessions on the grid.
   The stated cost — Cockpit reporting on sessions it does not own — is already
   the status quo: `BuildTargets` (`tui/grid.go:88`) puts every running tmux
   session on the grid regardless of what the config knows about.

3. **The pane-hash guess stays, marked inferred.** Dropping it is cheaper and
   more honest, and with both engines reporting it was nearly justified. Remote
   hosts decide it: a session on the hermes box cannot report until hook
   traffic has a reverse tunnel, so dropping the guess now blanks every remote
   tile the day it appears. Scoped as §4.4 describes — no `capture-pane` for a
   session that reports, the guess only for one that never has.

4. **`needs_input` goes to the top of Signals.** An agent blocked on you is the
   most time-sensitive thing Cockpit knows. It will churn more than the other
   signals, since it clears the moment you answer it, and that is an acceptable
   price for the one signal you can act on immediately.

5. **Codex reports too, and its mapping is the more precise of the two.**
   Resolved in §2. The endpoint is engine-agnostic with a per-engine event
   table.

## 9. What this does not do

- No remote hosts. Cockpit will watch and drive a second machine — that is
  decided, and it is the project after this one — but a remote agent's hook
  traffic needs the reverse tunnel from §2.6 of the source document, and the
  immutable-port-across-reconnect detail that goes with it. Building it here
  would mean designing the tunnel before the transport it rides on exists.
  What this design owes that project is a status store and an endpoint that do
  not assume the agent is local: the target is already a string, and the
  endpoint already takes it from the payload rather than inferring it.
- No transcript, tool arguments, or message bodies. Only enough to colour a
  tile.
- No history. Current state only — the previous state is not kept, because
  keeping it would mean owning a store.
