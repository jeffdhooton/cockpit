# Helm

A native terminal workspace for managing projects, processes, and AI agents.
Tauri + xterm.js. Own it, change it, no subscription.

## What it does

One window. Left sidebar shows your projects and their processes. Right side
is a real terminal. Select a process, you're typing into it. Everything starts
together, crashes get restarted, agents can see your stack via MCP.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Tauri (Rust backend)                                       │
│  ├─ Process manager: spawn, kill, restart, PID tracking     │
│  ├─ PTY multiplexer: one pty per process, stream to frontend│
│  ├─ File watcher: restart_when_changed globs                │
│  ├─ Config loader: helm.yml parser + watcher                │
│  ├─ MCP server: HTTP, exposes process state to agents       │
│  └─ Project scanner: git status, editor integration         │
│                                                             │
│  Frontend (TypeScript + xterm.js)                           │
│  ├─ Sidebar: project tree with status indicators            │
│  ├─ Terminal pane: xterm.js instance per process            │
│  ├─ Command palette: Cmd+K                                  │
│  └─ Notifications: crash alerts, agent activity             │
└─────────────────────────────────────────────────────────────┘
```

### Backend (Rust)

The backend owns all process lifecycle. Each process is a PTY child. Output
streams to the frontend over Tauri's IPC (invoke/events). The backend never
touches the frontend's rendering — it just emits bytes and status changes.

Key crates:
- `portable-pty` — cross-platform PTY spawning
- `notify` — file system watching for restart_when_changed
- `serde_yaml` — helm.yml parsing
- `axum` or `tower` — MCP HTTP server
- `git2` — git status without shelling out

### Frontend (TypeScript)

The sidebar is a tree component. Each project expands to show its processes
grouped by type (agents, commands, terminals). Status dots update via Tauri
event listeners. The terminal pane is an xterm.js instance that attaches to
whichever process is selected — switching processes swaps the data stream,
not the DOM element.

---

## helm.yml

Per-project manifest. Lives at project root. Committed to the repo.

```yaml
name: My Project
icon: public/favicon.png

processes:
  # Agents — AI coding tools
  claude-code:
    command: claude
    type: agent
    auto_start: false

  # Commands — dev stack
  dev-server:
    command: npm run dev
    type: command
    auto_start: true
    auto_restart: true
    restart_when_changed:
      - src/**/*.ts
      - vite.config.ts

  queue:
    command: php artisan queue:work
    type: command
    auto_start: true
    auto_restart: true
    env:
      QUEUE_CONNECTION: redis

  # Terminals — interactive shells
  shell:
    command: $SHELL
    type: terminal
    auto_start: false
```

### Process types

- **agent** — AI coding tools (claude, codex, gemini, aider). Shown under
  "Agents" in the sidebar. No auto_restart by default (agents manage their
  own lifecycle).
- **command** — Dev stack processes (servers, workers, watchers). Shown under
  "Commands". These are the things that should stay running.
- **terminal** — Interactive shells. Shown under "Terminals". Blank slates
  for ad-hoc work.

### Fields

- `command` — required. Shell command to run.
- `type` — agent | command | terminal. Defaults to command.
- `auto_start` — start when project opens. Defaults to true for commands.
- `auto_restart` — restart on exit. Defaults to false.
- `restart_when_changed` — glob patterns for file watching.
- `working_dir` — relative to project root. Defaults to project root.
- `env` — key-value env vars, merged on top of shell environment.

---

## Sidebar UI

```
┌─ Helm ──────────────────┐
│                          │
│ ▼ polymarket             │
│   AGENTS ──────── 2/3   │
│   ◐ claude-code          │
│   ● codex                │
│   ○ amp                  │
│   COMMANDS ─────── 3/4   │
│   ● dev-server           │
│   ● queue                │
│   ● scheduler            │
│   ○ migrations           │
│   TERMINALS ────── 0/1   │
│   ○ shell                │
│                          │
│ ▶ cockpit          ✓ 3/3 │
│ ▶ book-writing     ✗ 1/4 │
│ ▶ scry             · 0/0 │
│                          │
│ main ×3 ↑1               │
│                          │
├──────────────────────────┤
│ s start  x stop  r restart│
│ z zed  ⌘K palette        │
└──────────────────────────┘
```

### Status indicators

- `●` running (green)
- `◐` working/busy — for agents, detected by output activity
- `✗` crashed (red) — exited non-zero
- `○` stopped (gray)

### Collapsed project row

Shows project name + summary health: `✓ 3/3` (all running), `✗ 1/4`
(something crashed), `· 0/0` (nothing configured/running).

### Inline git status

Below the process tree for the selected project, show the branch, dirty
count, and unpushed commits on one line.

---

## Features

### Terminal emulation

Each process gets a dedicated xterm.js instance. Full ANSI/256-color/truecolor
support. Scrollback buffer of 10,000 lines per process (configurable). When
switching between processes, the terminal state is preserved — scroll position,
content, cursor. No re-rendering, just swapping which xterm instance is visible.

Terminal resize events propagate from the frontend pane dimensions through
Tauri IPC to the PTY's `set_size`. The sidebar width is fixed (resizable via
drag handle), and the terminal pane fills the remaining space.

Search within terminal output via `Cmd+F` when the terminal pane is focused.
Uses xterm.js's built-in search addon.

### Environment handling

When Helm launches, it captures the user's full shell environment by running
`$SHELL -ilc env`. This becomes the base environment for all child processes.
Per-process `env` vars in helm.yml are merged on top.

The captured environment is cached and refreshed on demand (command palette:
"Refresh shell environment") or when Helm regains focus after being in the
background for 10+ minutes.

`$SHELL`, `$HOME`, `$PATH`, and version manager paths (nvm, asdf, rbenv, etc.)
are all inherited automatically. Helm does not load `.env` files — frameworks
that need them (dotenv, Laravel) do that themselves.

### Project auto-detection

`helm init` scans the project directory and generates a helm.yml based on what
it finds:

- `package.json` with scripts → `npm run dev`, `npm run build`
- `Cargo.toml` → `cargo run`, `cargo watch`
- `go.mod` → `go run .`
- `artisan` → `php artisan serve`, `php artisan queue:work`
- `manage.py` → `python manage.py runserver`
- `Gemfile` with rails → `bin/rails server`
- `docker-compose.yml` → `docker compose up`
- `Procfile` → parse and import entries

The generated file is a starting point. User edits it to taste.

### Trust and security

When helm.yml changes (e.g. after `git pull`), Helm detects the modification
and shows a confirmation banner before running any new or changed commands.
Processes that haven't changed keep running undisturbed.

Trust is per-command. Once a command string is approved, it stays trusted until
it changes. Processes added through the Helm UI are trusted automatically.

The trust store lives at `~/.config/helm/trust.json` — a map of
`project_path + command_hash → trusted_at`.

### Local-only processes

Not everything belongs in the committed helm.yml. Helm supports local-only
processes stored in `~/.config/helm/local/{project_hash}.yml`. These show up
in the sidebar alongside yml processes but are never committed.

A process can be moved between local and yml via the command palette or
context menu. If helm.yml is deleted, all its processes convert to local-only
automatically.

### Notifications

Native OS notifications for:
- Process crash (non-zero exit) when Helm is in the background
- Agent completion (detected by output going idle after sustained activity)
- File-watch restart triggered

In-app toast notifications when Helm is in the foreground. Clicking a crash
notification navigates to the crashed process.

Per-project toggle to disable notifications. Per-process toggle for terminal
escape sequence notifications (OSC 9, OSC 777, OSC 99).

### Orphan detection

Helm tracks all child PIDs in `~/.config/helm/pids.json`. On launch, it
checks for still-running processes from a previous session. If found, shows
a dialog: "These processes are still running from a previous session" with
options to kill all, adopt all, or dismiss.

Adopted processes reattach to the sidebar as running. Their previous output
is gone (the PTY is already closed), but new output streams normally.

### Process output persistence

On process restart (manual or auto), the previous output is cleared and the
terminal starts fresh. This matches the mental model — a restart is a clean
slate.

For crash forensics, the last 1,000 lines of output before a crash are saved
to `~/.config/helm/crash-logs/{project}/{process}/{timestamp}.log`. These
accumulate up to 50 per process, oldest deleted first.

### Command palette

`Cmd+K` opens a fuzzy-searchable palette. Actions include:
- Start/stop/restart any process by name
- Jump to a project
- Add new project or process
- Open project in editor
- Open project in terminal
- Refresh shell environment
- Toggle theme
- Search all process output

The palette is the power-user escape hatch. Everything in the UI is also
accessible through it.

### Editor integration

`z` (or configurable key) opens the selected project's directory in the
configured editor. Supported editors:
- Zed, VS Code, Cursor, Windsurf
- PhpStorm, WebStorm, IntelliJ IDEA
- Sublime Text, Fleet
- Custom command

Configured globally in settings, with per-project override support.

### Theme system

Dark and light themes built in, plus system-follow. Theme applies to the
sidebar, chrome, and terminal background. Terminal color schemes are
separate — users can set their preferred terminal theme (Catppuccin, Dracula,
Solarized, etc.) independently of the app theme.

### Git status

For each project, Helm reads git state on a 5-second interval:
- Current branch
- Dirty file count
- Unpushed commit count
- Behind remote count
- Last commit message (truncated)

Displayed inline in the sidebar below the process tree when a project is
expanded. Also available via MCP for agents.

### GitHub CI status

Optional. If `gh` CLI is available and authenticated, Helm polls for CI
check status on the current branch. Shows a pass/fail/pending indicator
next to the branch name in the sidebar. Poll interval: 60 seconds.

---

## Keybindings

### Sidebar focused
- `j/k` — navigate tree
- `Enter` — select process (show in terminal pane) or expand project
- `s` — start/stop selected process
- `r` — restart selected process
- `S` — start all auto_start processes for selected project
- `X` — stop all processes for selected project
- `z` — open project in editor
- `t` — open project in external terminal
- `c` — clear selected process output

### Global
- `Cmd+K` — command palette
- `Cmd+,` — settings
- `Cmd+1-9` — jump to project by position
- `Cmd+L` — toggle focus between sidebar and terminal pane
- `Cmd+F` — search terminal output (when terminal focused)

### Terminal pane focused
- All keystrokes pass through to the process
- `Cmd+L` — jump back to sidebar

---

## Process lifecycle

### Startup
1. User opens Helm (or `helm up` from CLI)
2. Load all configured projects from `~/.config/helm/config.toml`
3. For each project with a `helm.yml`, parse the manifest
4. Merge in any local-only processes
5. Start all processes where `auto_start: true` (respecting trust)
6. Sidebar shows status as processes come up

### Crash handling and auto-restart
- Backend monitors each child PID via async wait
- On exit: capture exit code, save crash log
- If `auto_restart: true` and exit code != 0:
  - Wait 1 second, restart
  - On second crash within 30s: wait 5s
  - On third: wait 15s
  - On fourth+: wait 30s (cap)
  - Reset backoff after 60s of stable running
- Update sidebar status, fire notification
- If `auto_restart: false`: mark as crashed, leave stopped

### File watching
- For processes with `restart_when_changed`, set up watchers on launch
- Use `notify` crate with debounce (500ms)
- On trigger: graceful stop (SIGTERM, 5s timeout, SIGKILL) then restart
- Only active while the process is running
- Glob patterns resolved relative to project root

### Shutdown
- Closing window or `helm down` CLI command
- SIGTERM to all children
- Wait up to 5 seconds for graceful exit
- SIGKILL any remaining
- Clear PID tracking file
- Save window position/size for next launch

---

## MCP server

HTTP server on localhost (default port 45678). Agents add it once:

```bash
claude mcp add helm --transport http --scope user http://localhost:45678/
```

### Tools

- `helm_list_projects()` — all projects with process health summary
- `helm_list_processes(project)` — processes with status, PID, uptime
- `helm_read_output(project, process, lines?)` — last N lines of output
  (default 100)
- `helm_start(project, process)` — start a stopped process
- `helm_stop(project, process)` — stop a running process
- `helm_restart(project, process)` — restart a process
- `helm_signals()` — everything that needs attention across all projects
- `helm_git_status(project?)` — git state for one or all projects

### Example interactions

Agent: "Is anything crashed right now?"
```json
{
  "signals": [
    {"type": "crashed", "project": "polymarket", "process": "queue",
     "exit_code": 1, "crashed_at": "2026-04-21T14:32:00Z"},
    {"type": "unpushed", "project": "scry", "commits": 3}
  ]
}
```

Agent: "Restart the queue worker and show me the last 20 lines"
```json
// helm_restart("polymarket", "queue") → { "ok": true }
// helm_read_output("polymarket", "queue", 20) → { "lines": [...] }
```

---

## Config

Global config at `~/.config/helm/config.toml`:

```toml
[general]
editor = "zed"
terminal = "ghostty"
theme = "dark"
scrollback = 10000

[mcp]
enabled = true
port = 45678

[github]
enabled = true
poll_interval = 60

[[projects]]
path = "~/workspace/polymarket"
label = "polymarket"

[[projects]]
path = "~/workspace/cockpit"
label = "cockpit"
editor = "code"  # per-project override

[[projects]]
path = "~/workspace/book-writing"
label = "book-writing"
```

---

## CLI

- `helm` — launch the GUI
- `helm up` — launch and start all auto_start processes
- `helm down` — stop all processes and quit
- `helm add [path]` — add a project to config (defaults to cwd)
- `helm init` — generate helm.yml in cwd via project auto-detection
- `helm mcp` — run MCP server standalone (headless, for SSH/CI use)
- `helm list` — print all projects and process status to stdout
- `helm start <project> [process]` — start process(es) without GUI
- `helm stop <project> [process]` — stop process(es) without GUI

---

## Implementation phases

### Phase 1: One terminal in a window

Goal: Tauri app that spawns a PTY and renders it in xterm.js. No sidebar,
no config, no helm.yml. Just prove the byte pipeline works.

**Backend:**
- Scaffold Tauri project (`cargo create-tauri-app`)
- Add `portable-pty` dependency
- Tauri command: `spawn_shell()` — creates a PTY running `$SHELL`
- Tauri command: `write_to_pty(data: Vec<u8>)` — sends keystrokes to PTY
- Tauri event: `pty-output` — streams PTY bytes to frontend
- Handle PTY resize via `resize_pty(cols, rows)` command

**Frontend:**
- Install xterm.js + `@xterm/addon-fit` + `@xterm/addon-web-links`
- Full-window xterm.js instance
- Wire `onData` → `write_to_pty` invoke
- Wire `pty-output` event listener → `terminal.write()`
- Wire `FitAddon` to window resize → `resize_pty` invoke

**Milestone:** Open Helm, get a working shell. Type commands, see output.
Colors, cursor movement, vim, htop all work correctly.

### Phase 2: Multiple processes + sidebar skeleton

Goal: Sidebar with a hardcoded process list. Click a process to switch
which terminal is visible. Start/stop from the sidebar.

**Backend:**
- `ProcessManager` struct: HashMap of process ID → PTY handle + state
- Commands: `start_process(id, command, cwd, env)`, `stop_process(id)`,
  `write_to_process(id, data)`, `resize_process(id, cols, rows)`
- Each PTY output streams on a per-process event channel: `pty-output-{id}`
- Process state tracking: Stopped, Running, Crashed (with exit code)
- Event: `process-state-changed` → `{ id, state, exit_code? }`

**Frontend:**
- Split layout: fixed-width sidebar (280px default) + terminal pane
- Sidebar: flat list of process names with status dot
- Clicking a process:
  - Creates an xterm.js instance if first time (lazy init)
  - Hides current terminal, shows selected terminal
  - Attaches data listener to correct event channel
- Start/stop buttons per process in sidebar
- Style: minimal, dark background, monospace process names

**Milestone:** Three hardcoded processes (shell, `echo "hello"`, `sleep 999`).
Switch between them. Start/stop works. Status dots update.

### Phase 3: helm.yml parsing

Goal: Replace hardcoded processes with helm.yml. One project at a time.

**Backend:**
- `serde_yaml` structs matching helm.yml schema
- `load_manifest(project_path)` → `HelmManifest`
- Validation: required fields, unknown keys warning
- Tauri command: `load_project(path)` — parse helm.yml, register processes
- Auto-start processes where `auto_start: true`
- Capture shell environment on startup (`$SHELL -ilc env`)
- Merge per-process `env` on top of shell env

**Frontend:**
- Sidebar tree with sections: Agents / Commands / Terminals
- Section headers with running count: `COMMANDS ── 3/4`
- Process type icons or visual distinction
- Keyboard nav: `j/k` to move, `Enter` to select, `s` to start/stop

**Milestone:** Create a `helm.yml` in a real project. Launch Helm pointed at
it. Processes appear grouped by type. Start them. Interact with them.

### Phase 4: Multi-project

Goal: Multiple projects in the sidebar. Expand/collapse. Global config.

**Backend:**
- Parse `~/.config/helm/config.toml` for project list
- Load each project's helm.yml on startup
- Support projects without helm.yml (show as empty, or just git info)
- `add_project(path, label)` command — appends to config

**Frontend:**
- Project rows in sidebar: collapsible, show health summary when collapsed
- Expand a project → show its process tree
- `Cmd+1-9` to jump to project by position
- Sidebar scrollable when projects exceed viewport
- Project context: right-click or key to open in editor / terminal

**Milestone:** Three real projects in sidebar. Expand/collapse. Jump between
them. Processes start independently per project.

### Phase 5: Process lifecycle hardening

Goal: Auto-restart, crash detection, file watching, orphan recovery.

**Backend:**
- Auto-restart with exponential backoff (1s → 5s → 15s → 30s cap)
- Backoff reset after 60s of stable running
- Crash log persistence: last 1,000 lines to disk on non-zero exit
- `notify` crate file watcher for `restart_when_changed` globs
- Debounce: 500ms after last file event
- PID tracking in `~/.config/helm/pids.json`
- Orphan detection on startup: check if tracked PIDs are still alive
- Graceful shutdown: SIGTERM → 5s wait → SIGKILL

**Frontend:**
- Crash indicator (red dot + exit code) in sidebar
- Notification toast for crashes (in-app)
- Native OS notification for crashes when app is backgrounded
- Orphan recovery dialog on startup
- Restart count / uptime shown on hover or in process detail

**Milestone:** Kill a process externally (`kill -9`). Helm detects it, shows
crash state, auto-restarts if configured. Change a watched file, process
restarts. Force-quit Helm, relaunch, orphan dialog appears.

### Phase 6: Trust, local processes, helm.yml watching

Goal: Security model and config flexibility.

**Backend:**
- Trust store at `~/.config/helm/trust.json`
- Hash each process command string, store trust per project+hash
- On helm.yml change: diff against previous, flag new/changed commands
- Local-only process storage: `~/.config/helm/local/{project_hash}.yml`
- Merge local processes into sidebar alongside yml processes
- Move-to-local / move-to-yml commands
- helm.yml file watcher: detect changes, prompt sync

**Frontend:**
- Trust confirmation banner when new commands detected
- "Untrusted" badge on processes pending approval
- Local vs. yml indicator (subtle, maybe an icon)
- Sync banner when helm.yml changes externally

**Milestone:** `git pull` brings in a new process in helm.yml. Helm shows
trust prompt. Approve it, process becomes startable. Add a local-only
process, confirm it doesn't appear in helm.yml.

### Phase 7: Command palette + settings

Goal: Power-user keyboard interface and settings UI.

**Frontend:**
- Command palette component: `Cmd+K` to open
- Fuzzy search over all actions: start/stop/restart processes, jump to
  project, add project, toggle theme, open in editor, refresh env
- Results ranked by relevance, `Enter` to execute, `Cmd+1-9` for quick pick
- Settings panel: `Cmd+,`
  - Editor picker
  - Terminal picker
  - Theme toggle (dark / light / system)
  - Scrollback size
  - MCP port
  - Sidebar width
  - Notification preferences

**Milestone:** Every action reachable through `Cmd+K`. Settings changes
persist and apply immediately.

### Phase 8: Git status + editor/terminal integration

Goal: Contextual project info and one-key editor launch.

**Backend:**
- `git2` crate: branch, dirty count, unpushed, behind, last commit
- Poll on 5-second interval per project
- `open_in_editor(project_path, editor)` command
- `open_in_terminal(project_path, terminal)` command
- GitHub CI status via `gh` CLI (optional, poll 60s)

**Frontend:**
- Git status line below process tree in expanded project
- Format: `main ×3 ↑1 "last commit msg..."`
- CI status indicator next to branch: ✓ / ✗ / ◐
- `z` key opens editor, `t` opens terminal

**Milestone:** Expand a project, see its git state update live. Press `z`,
Zed opens to that directory. CI badge shows green/red.

### Phase 9: MCP server

Goal: Agents can query and control Helm.

**Backend:**
- `axum` HTTP server on configurable port (default 45678)
- JSON-RPC or REST endpoints for all MCP tools
- `helm_list_projects`, `helm_list_processes`, `helm_read_output`,
  `helm_start`, `helm_stop`, `helm_restart`, `helm_signals`,
  `helm_git_status`
- Read process output from xterm.js scrollback buffer (forwarded from
  frontend) or from a backend ring buffer
- Server starts with app if MCP enabled in config
- `helm mcp` CLI: run server standalone without GUI

**Frontend:**
- MCP server status indicator in sidebar footer or settings

**Milestone:** Add Helm as MCP server in Claude Code settings. Ask Claude
"what's crashed?" — it calls `helm_signals()` and reports. Ask it to restart
a process — it does.

### Phase 10: Polish + CLI

Goal: Production-quality UX and headless operation.

**Backend:**
- `helm init` — project auto-detection, generates helm.yml
- `helm add` — add project to global config
- `helm list` — print status table to stdout
- `helm start/stop` — headless process control
- `helm up` / `helm down` — full lifecycle from CLI
- Window position/size persistence

**Frontend:**
- Sidebar drag-to-resize handle
- Project drag-to-reorder
- Terminal theme picker (Catppuccin, Dracula, Solarized, etc.)
- xterm.js search addon: `Cmd+F` in terminal
- Web links addon: clickable URLs in terminal output
- Smooth animations on process state transitions
- Empty state: "No projects yet. Add one with Cmd+K or helm add ."
- About / version info

**Milestone:** Full app. Daily-driver ready. Someone could clone the repo,
read the README, and understand what to do.
