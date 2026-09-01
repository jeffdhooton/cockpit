# Project Processes & Cockpit Daemon — Design

Date: 2026-09-01
Status: approved

## Problem

Cockpit is a tmux-native dashboard. Two capabilities exist in Helm
(`~/workspace/helm`) that Cockpit lacks:

1. **Per-project background processes.** Helm lets a project declare processes
   (`dev-server`, `test-watch`) in `helm.yml` and manages them in PTYs. Cockpit
   has no notion of a project process at all — jumping to a repo gives you one
   bare shell.
2. **An MCP server.** Helm exposes twelve `helm_*` tools over HTTP JSON-RPC on
   `127.0.0.1:45678`, registered in `~/.claude.json` and `~/.codex/config.toml`.
   Agents use it to inspect and drive the workspace. Cockpit exposes nothing.

The goal is parity with Helm's capability, implemented in Cockpit's idiom —
tmux is the process manager, not a bespoke PTY layer — and a cutover so agents
talk to Cockpit instead of Helm.

## Non-goals

- No PTY multiplexer, no output ring buffers, no auto-restart supervision.
  tmux already does this; duplicating it is how Cockpit stops being tmux-native.
- No streaming status events. Helm parses PTY output live; Cockpit matches
  patterns against pane scrollback on demand (see Degradations).
- No trust/security model beyond loopback binding — matching Helm's posture.
- No changes to the grid/dashboard rendering beyond a process indicator.

---

## Part 1 — Project processes as tmux windows

### Config

Processes are declared in `~/.config/cockpit/config.toml`, nested under the
repo they belong to. No second config format, no in-repo file.

```toml
[[repos]]
path = "~/workspace/my-app"
label = "my-app"

  [[repos.processes]]
  name = "dev"                    # tmux window name
  command = "npm run dev"
  auto_start = true               # default true when omitted
  working_dir = "packages/web"    # optional; relative to repo path, or absolute
  env = { PORT = "3000" }         # optional

  [[repos.processes]]
  name = "test"
  command = "npm test -- --watch"
  auto_start = false              # declared, started on demand

    [repos.processes.status]      # optional; used by cockpit_status
    ready = 'Local:\s+(\S+)'
    error = 'error|failed'
```

Go types:

```go
type RepoConfig struct {
    Path      string          `toml:"path"`
    Label     string          `toml:"label"`
    Processes []ProcessConfig `toml:"processes"`
}

type ProcessConfig struct {
    Name       string            `toml:"name"`
    Command    string            `toml:"command"`
    AutoStart  *bool             `toml:"auto_start"`   // nil => true
    WorkingDir string            `toml:"working_dir"`
    Env        map[string]string `toml:"env"`
    Status     *StatusPatterns   `toml:"status"`
}

type StatusPatterns struct {
    Ready      string `toml:"ready"`
    Compiling  string `toml:"compiling"`
    Error      string `toml:"error"`
    Restarting string `toml:"restarting"`
}
```

`AutoStart` is a `*bool` because Go zero-values `bool` to `false` and the
default must be `true`. `ShouldAutoStart()` returns `p.AutoStart == nil || *p.AutoStart`.

### Validation

Fails config load with a clear message when:

- a process `name` is empty, duplicated within a repo, or fails
  `^[a-zA-Z0-9_-]+$` (tmux window names, and the same rule Cockpit already
  applies to session labels via `validLabel`)
- a process `command` is empty
- a `status` pattern is not a valid Go regexp

`WorkingDir` is resolved at launch: absolute paths used as-is, relative paths
joined to the repo path. Tildes expanded during `expandPaths`.

### Launch behavior

`tmuxJump(label, path)` in `tui/app.go` gains a process step. It becomes
`tmuxJumpWithProcesses(repo config.RepoConfig)`:

**Session does not exist:**
1. `tmux new-session -d -s <label> -c <path>` — window 0, a plain shell.
2. For each process with `auto_start`: `tmux new-window -d -t <label>: -n <name> -c <dir> [-e K=V ...] -- <command>`
3. `tmux set-window-option -t <label>:<name> remain-on-exit on` per process window.
4. `tmux select-window -t <label>:0` — you land on the shell, not a log.
5. `tmux switch-client -t <label>`

**Session exists (reconcile):**
1. `tmux list-windows -t <label>` to see what is already there.
2. Start any `auto_start` process that has no live window. A window that exists
   but is dead (`remain-on-exit` corpse) is respawned rather than duplicated.
3. Do **not** select a process window, do **not** touch window 0's selection —
   just `switch-client`. Reconciliation is silent and idempotent.

`remain-on-exit on` is set per-window, not per-session, so a crashed dev server
leaves a readable dead pane while your own ad-hoc windows keep normal behavior.

Process launch failures never block the jump: the switch happens regardless and
the error is surfaced as a signal, not a modal.

### Surfacing in the TUI

- Grid tile for a repo/session with configured processes shows `⚙ n/m`
  (live/configured), rendered muted when `n == m` and in the error color when a
  process window is dead.
- Dead process windows become signals: `my-app/dev exited`.

### tmux navigation (documented in README)

With `Ctrl+Space` as prefix:

| Keys | Action |
|---|---|
| `prefix` `0`–`9` | Window by index — 0 is your shell, 1+ are processes |
| `prefix` `n` / `p` | Next / previous window |
| `prefix` `l` | Last window (toggle) |
| `prefix` `w` | Interactive window picker |
| `prefix` `,` / `&` | Rename / kill window |
| `prefix` `S` | Back to cockpit (existing recommended binding) |

Optional prefix-free bindings recommended in the README:

```bash
bind -n M-1 select-window -t 1
bind -n M-h previous-window
bind -n M-l next-window
```

---

## Part 2 — `cockpit daemon` and the MCP server

### Shape

A standalone process, not a goroutine in the TUI. All of Cockpit's state lives
in tmux, git, and markdown files — the daemon derives every answer live and
holds no session state, so it works whether or not the TUI is running, and
restarting it loses nothing.

```toml
[daemon]
enabled = true
port = 45679          # Helm used 45678; Cockpit takes the next port
```

CLI:

| Command | Behavior |
|---|---|
| `cockpit daemon` | Run in the foreground (log to stderr) |
| `cockpit daemon start` | Fork to background, write pidfile, log to file |
| `cockpit daemon stop` | SIGTERM the pid in the pidfile, remove it |
| `cockpit daemon status` | Report running/not, pid, port, uptime |
| `cockpit daemon install` | Write a LaunchAgent plist and `launchctl load` it |
| `cockpit daemon uninstall` | Unload and remove the plist |

State files in `~/.config/cockpit/`: `daemon.pid`, `daemon.log`.
LaunchAgent label: `com.jeffdhooton.cockpit.daemon`.

`daemon start` detects a stale pidfile (pid absent or not a cockpit process) and
overwrites it rather than refusing to start.

### Transport

HTTP JSON-RPC 2.0, mirroring `helm/src-tauri/src/mcp.rs` exactly so the client
contract is unchanged:

- Routes `/` and `/mcp`, both handling POST (rpc), GET (405), DELETE (202).
- `mcp-session-id` response header, session id `cockpit-<hex nanos>`.
- `initialize` echoes the client's `protocolVersion` when it is one of
  `2025-11-25`, `2025-06-18`, `2025-03-26`; otherwise `2024-11-05`.
- `serverInfo: {name: "cockpit", version: <build version>}`, `capabilities: {tools: {}}`.
- Notifications (`id` absent) get `202 Accepted` with an empty body.
- Tool errors are returned as a **successful** JSON-RPC result with
  `isError: true` and the message in `content[0].text` — same as Helm.
- Bind `127.0.0.1` only. No auth, matching Helm.

Go standard library only (`net/http`, `encoding/json`). No new dependencies.

### Tools

Twelve parity tools, named `cockpit_*` with the same arguments as their `helm_*`
counterparts, plus two Cockpit-native ones.

| Tool | Implementation |
|---|---|
| `cockpit_list_projects` | Configured repos: session state, git status, process health |
| `cockpit_list_processes` | `tmux list-windows`; each configured process reported live / dead / not started, plus unmanaged windows |
| `cockpit_read_output` | `tmux capture-pane -p -t <sess>:<win> -S -<lines>` |
| `cockpit_start` | Create the window for a configured process (respawn if a dead window exists) |
| `cockpit_stop` | `tmux kill-window -t <sess>:<win>` |
| `cockpit_restart` | `tmux respawn-window -k -t <sess>:<win>` |
| `cockpit_signals` | Stale sessions, unpushed commits, failing CI, dead process windows |
| `cockpit_git_status` | `sources.GetGitStatus`, one repo or all |
| `cockpit_spawn_agent` | New tmux window running `command`; optional `prompt` delivered via `send-keys` once output settles |
| `cockpit_write_input` | `tmux send-keys -t <target> -l <input>`, then `Enter` when `submit` (default true) |
| `cockpit_whoami` | Daemon version, pid, port, config path, session name, live sessions |
| `cockpit_status` | `status` regexes matched against pane content, newest-first events |
| `cockpit_capture` | Append to the inbox file (`sources.AppendInbox`) |
| `cockpit_tasks` | Read today's tasks; toggle one by line number |

Project identity is the repo `label`, which is also the tmux session name —
the same string Helm calls "project". Process identity is the window name.

**`cockpit_spawn_agent` prompt delivery** reproduces Helm's heuristic: sleep
150ms, then poll `capture-pane` every 100ms; when the content is non-empty and
unchanged for ~600ms, send the prompt with `send-keys -l` followed by `Enter`.
Cap at 10s, then send anyway. Because tmux has no `last_output_at`, quiet is
measured by hashing successive pane captures.

### Degradations from Helm (deliberate)

- **`cockpit_status` is a point-in-time scan.** Helm's process manager tags
  output as it streams and keeps an event log. Cockpit matches patterns against
  the pane's current scrollback when the tool is called. Same tool shape, but
  bounded by tmux's history limit rather than complete since start. Events get a
  `source: "scrollback"` field so a caller can tell.
- **No auto-restart / backoff / file-watching.** tmux's `remain-on-exit` keeps
  the corpse readable; restarting is `cockpit_restart` or `prefix &` + rejump.
- **No resource metrics.** Helm reports CPU/memory per process; Cockpit reports
  the window's pane PID and lets the caller decide.

### Registration cutover

Exactly two files on this machine reference Helm's MCP:

- `~/.claude.json` → `mcpServers.helm` = `{"url": "http://127.0.0.1:45678/mcp"}`
- `~/.codex/config.toml` → `[mcp_servers.helm]` with the same url

Both are backed up (`.bak-YYYYMMDD-HHMMSS`) then rewritten to a `cockpit` key
pointing at `http://127.0.0.1:45679/mcp`. The `helm` entry is removed, not kept
alongside — two overlapping workspace toolsets make agents pick wrong.

`~/.claude.json` is edited with a JSON parse/modify/write so the rest of the
(very large) file is preserved byte-for-byte in structure. `~/.codex/config.toml`
is edited as text, replacing only the `[mcp_servers.helm]` block.

---

## Architecture

```
cmd/
  root.go          existing
  daemon.go        NEW  daemon start|stop|status|install|uninstall
config/
  config.go        MOD  ProcessConfig, DaemonConfig, validation
sources/
  tmux.go          MOD  Runner interface + argv builders + window ops
  processes.go     NEW  reconcile/launch logic over the Runner
  signals.go       NEW  signal computation (shared by daemon and TUI)
  obsidian.go      existing (reused by cockpit_capture / cockpit_tasks)
daemon/
  daemon.go        NEW  lifecycle, pidfile, logging, launchagent
  mcp.go           NEW  JSON-RPC transport
  tools.go         NEW  tool definitions + dispatch
tui/
  app.go           MOD  tmuxJump launches/reconciles processes
  grid.go          MOD  process indicator on tiles
```

### The `Runner` seam

Every tmux interaction goes through:

```go
type Runner interface {
    Run(ctx context.Context, args ...string) (string, error)
}
```

with `ExecRunner` in production and a fake in tests. Window operations are
written as pure argv builders (`NewWindowArgs(...) []string`) tested directly,
plus thin functions that hand the argv to a `Runner`. This is what makes the
whole feature testable without spawning tmux, and it is the one structural
change to existing code.

## Testing

- `config`: process parsing, `auto_start` default, duplicate/invalid names,
  invalid status regex, working_dir resolution, daemon defaults.
- `sources`: argv builders (exact arguments for new-window with env, capture,
  respawn, kill, send-keys); reconcile logic against a fake Runner covering
  fresh session / all-running / partially-running / dead-window cases; signal
  computation.
- `daemon`: transport via `httptest` — bad jsonrpc version, `initialize`
  version negotiation, `tools/list` shape, `tools/call` success and error
  envelopes, notification 202, unknown method -32601; tool dispatch against a
  fake Runner and a temp config.
- Manual: launch a real project with processes, confirm window layout, landing
  window, dead-pane retention, and an end-to-end MCP call from Claude Code.

## Verification

`go build ./... && go vet ./... && go test ./...` must pass, plus a live
`tools/list` against the running daemon, before the work is called done.
