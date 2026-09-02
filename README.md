# Cockpit

A tmux-native terminal dashboard for developers juggling multiple projects. One command gives you a persistent home screen with live project status, quick-capture for fleeting thoughts, and one-keystroke jumping between contexts.

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)

```
┌─ COCKPIT ────────────────────────────────────────┐
│ ╭────────────╮ ╭────────────╮ ╭────────────╮     │
│ │ my-app     │ │ scry       │ │ side-proj  │     │
│ │ ● working  │ │ ● idle 4m  │ │ ● attached │     │
│ │ feat/auth✗3│ │ main ✓ ↑2  │ │ main ✓     │     │
│ ╰────────────╯ ╰────────────╯ ╰────────────╯     │
│ ╭────────────╮ ╭────────────╮                    │
│ │ dotfiles   │ │ notes      │                    │
│ │ ● no session │ ● no session                    │
│ │ main ✓     │ │ main ✗ 3   │                    │
│ ╰────────────╯ ╰────────────╯                    │
├─ my-app ─────────────────────────────────────────┤
│ $ go test ./...                                  │
│ ok  github.com/you/my-app  0.31s                 │
├──────────────────────────────────────────────────┤
│ HJKL nav · ENTER jump · N new · / find · D dash  │
└──────────────────────────────────────────────────┘
```

## What it does

- **Grid** — Running tmux sessions and saved repos as one grid. `hjkl` to move, Enter to jump; a dormant repo gets a session created on the spot. The grid reflows from 4 columns down to 1, so it stays usable over SSH from a phone.
- **Dashboard** — Press `d` for the full five-panel view below. Set `default_view = "dashboard"` in config.toml to make it the startup view.
- **Sessions** — See all running tmux sessions. Press Enter to jump to one, or auto-create a new session from a repo.
- **Repos** — Git status across all your projects: branch, dirty/clean, unpushed commits, last commit message.
- **Today** — Tasks pulled from a markdown file in your Obsidian vault. Toggle them with `x`.
- **Inbox** — Quick-capture thoughts with `c` or `cockpit cap "idea"` from any terminal. Triage later in Obsidian.
- **Signals** — What needs attention: dead processes, failing CI, unpushed commits, stale sessions.
- **Processes** — A project can declare background processes. Jumping to it brings them up as tmux windows beside your shell.
- **Daemon** — A local tool server so agents like Claude Code and Codex can inspect and drive the workspace.

Everything refreshes automatically. Local sources (tmux, git, Obsidian) every 5 seconds, GitHub every 60 seconds.

## Install

Requires Go 1.22+, tmux, git, and optionally `gh` (GitHub CLI) for PR/CI signals.

```bash
go install github.com/jeffdhooton/cockpit@latest
```

Or build from source:

```bash
git clone https://github.com/jeffdhooton/cockpit.git
cd cockpit
go build -o cockpit .
cp cockpit ~/.local/bin/  # or anywhere in your PATH
```

## Setup

```bash
# Generate a config file
cockpit init

# Edit it — add your repos and Obsidian vault path
$EDITOR ~/.config/cockpit/config.toml
```

The config looks like this:

```toml
[general]
session_name = "cockpit"
refresh_interval = 5

[obsidian]
vault_path = "~/Documents/Vault"
today_file = "Cockpit/today.md"
inbox_file = "Cockpit/inbox.md"

[[repos]]
path = "~/workspace/my-app"
label = "my-app"

[[repos]]
path = "~/workspace/side-project"
label = "side-proj"

[github]
enabled = true
refresh_interval = 60

[signals]
stale_session_threshold = "24h"
show_stale_sessions = true
show_unpushed = true
show_failing_ci = true

[daemon]
enabled = true
port = 45679
```

Create the Obsidian task files if they don't exist:

```bash
mkdir -p ~/Documents/Vault/Cockpit
touch ~/Documents/Vault/Cockpit/today.md ~/Documents/Vault/Cockpit/inbox.md
```

If you use Obsidian, enable **"Detect all file changes"** in Settings → Files & Links so task toggles from Cockpit sync seamlessly.

## Usage

```bash
# Launch (or reattach to) the cockpit dashboard
cockpit

# Capture a thought from anywhere
cockpit cap "fix the auth bug"

# Interactive capture mode
cockpit cap

# Serve the tool server for agents
cockpit daemon start
```

## Keybindings

| Key | Action |
|-----|--------|
| `Tab` | Cycle focus between panels |
| `j` / `k` | Navigate items within a panel |
| `Enter` | Jump to tmux session (Sessions/Repos panels) |
| `x` | Toggle task checkbox |
| `c` | Enter capture mode |
| `Esc` | Exit capture mode |
| `r` | Force refresh all sources |
| `q` | Quit (session stays alive — run `cockpit` to return) |

## Project processes

A project can declare background processes. When you jump to it, cockpit creates
the session and launches each one in its own tmux window. Window 0 stays a plain
shell and is where you land — a noisy dev server never drops you into a log.

```toml
[[repos]]
path = "~/workspace/my-app"
label = "my-app"

  [[repos.processes]]
  name = "dev"                  # becomes the tmux window name
  command = "npm run dev"
  auto_start = true             # default; false means "declared, start on demand"
  working_dir = "packages/web"  # optional, relative to the repo or absolute
  env = { PORT = "3000" }       # optional

    [repos.processes.status]    # optional, read by the cockpit_status tool
    ready = 'Local:\s+(\S+)'
    error = 'error|failed'
```

Jumping to a project that is already open reconciles rather than duplicates: a
process that is missing gets started, one that died gets respawned in place, and
anything already running is left alone. Process windows are set to
`remain-on-exit`, so a crash leaves a readable dead pane instead of a window
that vanishes. Dead processes show up in Signals, and grid tiles carry a `⚙ 1/2`
live-count badge.

### Navigating windows

With `Ctrl+Space` as your tmux prefix:

| Keys | Action |
|------|--------|
| `prefix` `1`–`9` | Jump to a window by number — the first is your shell, the rest are processes |
| `prefix` `n` / `p` | Next / previous window |
| `prefix` `l` | Toggle back to the last window |
| `prefix` `w` | Interactive window picker across sessions |
| `prefix` `,` | Rename the current window |
| `prefix` `&` | Kill the current window |
| `prefix` `S` | Back to cockpit (see the tmux config below) |

For prefix-free flipping between a server and its logs, add these to `~/.tmux.conf`:

```bash
bind -n M-1 select-window -t 1   # Alt+1..9 straight to a window
bind -n M-2 select-window -t 2
bind -n M-3 select-window -t 3
bind -n M-h previous-window
bind -n M-l next-window
```

## Daemon

Cockpit ships a local tool server so agents can see and drive your workspace:
list projects, read a process's output, start and stop processes, check signals
and git status, spawn a parallel agent in its own window, type into a running
process, and capture a thought to today's list.

```bash
cockpit daemon start      # background, logs to ~/.config/cockpit/daemon.log
cockpit daemon status
cockpit daemon stop
cockpit daemon install    # start at login (macOS launch agent)
cockpit daemon uninstall
cockpit daemon            # foreground, for debugging
```

It binds `127.0.0.1:45679` only, and holds no state of its own — every answer is
read live from tmux, git, and your markdown files, so restarting it loses
nothing.

Loopback keeps out other machines, not other programs on yours: a web page you
visit can post to a local port, and a `tools/call` that reaches the daemon has
already typed into your pane by the time the browser hides the reply. So the
daemon also refuses any request carrying an `Origin` header — browsers send one,
MCP clients do not — and requires `application/json`, which denies the
content types a browser can send without a preflight. Bodies are capped at 1MB.

Register it once with your agent tooling:

```bash
bash scripts/register-mcp.sh
```

That points Claude Code (`~/.claude.json`) and Codex (`~/.codex/config.toml`) at
the daemon, backing both up first. It is safe to run twice. To wire it up by
hand instead, add a server with the URL `http://127.0.0.1:45679/mcp`.

Twelve of the tools mirror Helm's suite one for one; `cockpit_capture` and
`cockpit_tasks` are cockpit's own. One difference is worth knowing:
`cockpit_status` matches your `[repos.processes.status]` patterns against the
pane's scrollback when you call it, rather than tailing a live stream, so it
reaches back only as far as tmux's history.

## Agent status

A tile's status — working, idle, or **needs you** — comes from the agent
itself when it can. Claude Code and Codex both fire hooks at real lifecycle
boundaries; cockpit installs a tiny one that posts the event name to the
daemon, which records it as a tmux session option. Nothing else from the
event leaves the agent: not the prompt, not the tool arguments, not the reply.

```bash
cockpit hook install
```

That merges the hook into `~/.claude/settings.json` and `~/.codex/config.toml`,
backing each up first. It is safe to run twice. The daemon must be running for
status to land; the hook exits 0 no matter what, so a stopped daemon costs you
the status and nothing else.

| Tile shows | Meaning |
|---|---|
| `● working` | A prompt landed or a tool started |
| `● idle 4m` | The turn ended |
| `● needs you` | Blocked on a permission prompt — the one worth walking over for |
| dimmed label | No hook has reported; this is a guess from pane activity |
| `○ no session` | Nothing to attach to |

A session that reports is left out of the per-session `capture-pane` poll, so
the more sessions report, the cheaper the refresh gets. A status older than ten
minutes is treated as stale and falls back to the guess, so a crashed agent
cannot stay "working" forever.

**Codex needs one more step.** Codex leaves a newly installed hook untrusted
and does not run it until you approve it, and trust is pinned to a hash of the
hook's configuration — editing the command untrusts it again. `hook install`
reads back what it wrote and tells you which it found. Until you approve the
hook inside Codex, its sessions stay on the guess.

A waiting agent also appears first in the `cockpit_signals` tool, above a
dead process.

## Remote hosts

Cockpit can watch and drive a second machine over SSH. Its tmux sessions and
repos appear as tiles prefixed with the host name, and Enter on one brings the
remote session and its processes up, then drops you into it.

```toml
[[hosts]]
name = "mini"                      # an alias from ~/.ssh/config
tmux = "/opt/homebrew/bin/tmux"    # absolute: a bare ssh gets no Homebrew PATH
cockpit = "~/.local/bin/cockpit"   # optional, see below

[[repos]]
host = "mini"
path = "~/workspace/docket"        # ~ is the remote user's, not yours
label = "docket"
```

There is no SSH client inside cockpit. It runs your `ssh`, with ControlMaster
so one connection per host carries every query, which means your config —
`Include`, `IdentitiesOnly`, `ProxyJump`, host key checks — is honoured
exactly as it would be at a prompt. A host that needs a passphrase or an
unknown key fails fast rather than hanging the poll.

**Jumping.** Enter on `mini/docket` creates the session on `mini`, starts its
processes there, and switches you to a local tmux session named `mini` whose
`docket` window is an `ssh -t … tmux new -A -s docket`. `prefix S` brings you
back. The local session is a view: killing a window kills nothing remote, and
the next Enter reattaches.

**Unreachable.** When a host stops answering, its tiles keep their last-known
state under `⚠ unreachable` and the poll backs off to a minute. Nothing is
ever launched against a host in that state — a dropped link during a jump
fails closed rather than starting a second dev server on a machine you cannot
see.

**Agent status on the remote box.** Install cockpit there too, run its
daemon, and:

```bash
cockpit hook install --host mini
```

The remote hooks post to the remote daemon, the status lands in the remote
tmux, and the same `list-sessions` that draws the tile reads it. No tunnel.

### Hermes

A Hermes gateway on the tailnet gets one read-only tile — gateway running or
stopped, and which platforms are connected — from its dashboard's status
endpoint, which needs no token. A stopped gateway also appears in Signals.

```toml
[[hermes]]
label = "hermes"
url = "http://100.96.45.73:9119"
```

## How it works

Cockpit is a single Go binary built with [Bubbletea](https://github.com/charmbracelet/bubbletea). It creates a tmux session and runs the TUI inside it. When you jump to another session, cockpit stays alive in the background. Run `cockpit` again to reattach.

Data sources are polled on independent intervals using `tea.Tick`:
- **tmux** — `tmux list-panes` for session data
- **git** — `git status`, `git log`, `git rev-list` per configured repo
- **Obsidian** — reads/writes plain markdown files (checkbox lines)
- **GitHub** — `gh pr list`, `gh run list` via the GitHub CLI

## Recommended tmux config

Cockpit works best with `Ctrl+Space` as your tmux prefix and a binding to jump back:

```bash
# In ~/.tmux.conf
unbind C-b
set -g prefix C-Space
bind C-Space send-prefix

# Jump back to cockpit from any session
bind S switch-client -t cockpit
```

See [starting-spec.md](starting-spec.md) for a full recommended tmux config optimized for split ergonomic keyboards.

## License

MIT
