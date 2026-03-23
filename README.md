# Cockpit

A tmux-native terminal dashboard for developers juggling multiple projects. One command gives you a persistent home screen with live project status, quick-capture for fleeting thoughts, and one-keystroke jumping between contexts.

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)

```
┌─ Sessions ───────────────────────────────────────┐
│ ┌───────────┐ ┌───────────┐ ┌───────────┐       │
│ │ cockpit*  │ │ my-app    │ │ side-proj  │       │
│ │ attached  │ │ detached  │ │ attached   │       │
│ └───────────┘ └───────────┘ └───────────┘       │
├─ Repos ──────────────────────────────────────────┤
│ my-app      feat/auth     ✗ 3    ↑2  add auth…   │
│ side-proj   main          ✓          fix typo…    │
├─ Today ──────────────────────────────────────────┤
│ [x] Ship auth fix                                │
│ [ ] Review PR for side-proj  ◂                   │
├─ Inbox ──────────────┬─ Signals ─────────────────┤
│ [ ] look into caching │ ✗ 1 repo with failing CI  │
│ > _                   │ ✓ All sessions active      │
├──────────────────────┴───────────────────────────┤
│ Tab panels  j/k nav  Enter jump  x toggle  c cap │
└──────────────────────────────────────────────────┘
```

## What it does

- **Sessions** — See all running tmux sessions. Press Enter to jump to one, or auto-create a new session from a repo.
- **Repos** — Git status across all your projects: branch, dirty/clean, unpushed commits, last commit message.
- **Today** — Tasks pulled from a markdown file in your Obsidian vault. Toggle them with `x`.
- **Inbox** — Quick-capture thoughts with `c` or `cockpit cap "idea"` from any terminal. Triage later in Obsidian.
- **Signals** — What needs attention: failing CI, unpushed commits, stale sessions.

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
