# Cockpit — Terminal Command Center

## Problem

Too many Claude Code sessions, browser windows, Slack threads, projects, and contexts open simultaneously. No single place to see "what's hot right now" across everything. Context-switching costs are eating productive hours.

## Solution

A tmux-based TUI that acts as a persistent home screen. One command (`cockpit`) launches or reattaches to a multi-pane tmux session that shows live project status, provides quick-capture for fleeting thoughts, and serves as a jump-off point to any active context.

## Target User

Solo technical founder running multiple concurrent projects (3+), heavy CLI user, macOS (M4 Mac Mini), Obsidian for knowledge management.

## Layout

```
┌─────────────────────────────────────────────┐
│              STATUS BOARD (top)              │
│  Live-updating dashboard refreshing on loop  │
├──────────────────────┬──────────────────────┤
│   SCRATCHPAD / CAP   │   SHELL / LAUNCHER   │
│  Quick-capture to    │  Default terminal or  │
│  Obsidian vault      │  session picker       │
└──────────────────────┴──────────────────────┘
```

## Status Board — What It Shows

The top pane runs a bash script on a `watch` loop (configurable interval, default 5s). It renders a compact, glanceable dashboard:

**Active tmux sessions** — List all running tmux sessions with name, window count, and attached/detached status. This is the primary "what do I have going on" signal.

**Git status per project** — For each configured repo path: current branch, dirty/clean, unpushed commits, last commit message + relative timestamp. Visual indicator (✓ / ✗ / ↑) for quick scanning.

**Today's tasks** — Pulled from a markdown file in the Obsidian vault (`Cockpit/today.md`). Simple checkbox format. The board just cats and formats it — Obsidian is the editor.

**System context (optional/stretch)** — Open browser tab count (via AppleScript or browser CLI), Slack unread badge (if feasible without auth headaches), Spotify current track (via `osascript`). These are nice-to-haves, not blockers.

## Quick Capture (`cap`)

A standalone shell function/script that appends timestamped entries to `$VAULT_PATH/Cockpit/inbox.md`.

Usage: `cap "fix auth bug in hoopless"` or just `cap` for interactive mode.

Format in the file:
```
- [ ] fix auth bug in hoopless — 2026-03-22 14:32
```

This file is the inbox. User triages it into `today.md` or project-specific notes inside Obsidian whenever they want. Zero structure imposed.

## Configuration

Single config file (`cockpit.conf`) with:

- `VAULT_PATH` — Obsidian vault root
- `PROJECT_REPOS` — Array of absolute paths to git repos to track
- `PROJECT_LABELS` — Friendly names (optional, falls back to dir name)
- `REFRESH_INTERVAL` — Dashboard refresh rate in seconds
- `SESSION_NAME` — tmux session name for the cockpit

## Tech Stack

- **bash** — All of it. No frameworks, no package managers, no build step.
- **tmux** — Layout engine and session management.
- **watch** or bash `while` loop — Dashboard refresh.
- **git CLI** — Repo status.
- **osascript** (macOS) — Optional integrations (browser tabs, Spotify).
- **Obsidian vault** — Task storage (plain markdown, no plugins required).

## What This Is NOT

- Not a task manager. Obsidian is the task manager. This just surfaces what's already there.
- Not a process manager. It doesn't start/stop your projects. It shows you what's running.
- Not a replacement for tmux knowledge. You still switch sessions with `Ctrl-b s` or `Ctrl-b w`. The cockpit is one session among many — it's just your home base.

## Install / Setup

Should be: clone the repo, edit `cockpit.conf`, run `./install.sh` (which symlinks `cockpit` and `cap` into `~/.local/bin` or similar), done.

## Stretch Goals (V2, not V1)

- **Keybind integration** — `cockpit jump <session-name>` to switch to a project session from within the cockpit pane.
- **Pomodoro / time-boxing** — A timer pane or status line integration.
- **Project scaffolding** — `cockpit new <project-name>` creates a tmux session, a git repo, and an Obsidian project note in one shot.
- **Bubbletea rewrite** — If the bash version hits a wall on formatting or interactivity, port the status board to Go with Bubbletea for richer TUI rendering. Only if bash isn't cutting it.

---

## tmux Adoption — Workflow Migration

The cockpit assumes a tmux-native workflow where each project lives in a named session. This replaces the 80-open-iTerm2-tabs pattern with something listable, switchable, and persistent.

### Mental Model

Each project gets one tmux session. Each session can have multiple windows (like tabs) and panes (like splits). The cockpit itself is just another session — your home base. You jump between sessions with the session picker (`prefix + s`), and the cockpit's status board shows you what's running across all of them.

```
tmux sessions:
  cockpit     ← home base, status dashboard
  book-suite  ← Claude Code, test runs, etc.
  hoopless    ← CRM dev work
  sky-high    ← partner project
```

### Key Habits to Build

- `cockpit` to start your day (launches or reattaches the cockpit session)
- `prefix + s` to see all sessions and jump between them
- `prefix + d` to detach (session stays alive in background)
- `prefix + ,` to rename a window
- `tmux new -s <name>` to start a new project session (or let `cockpit new` handle it in V2)
- `tmux ls` from any terminal to see what's running

### Naming Convention

Session names should be short, lowercase, hyphenated. They show up in the status board and session picker, so brevity matters: `book-suite`, `hoopless`, `sky-high`, not `Book Suite Development Environment`.

---

## tmux Starter Config — Neo Ergo Optimized

Designed for a split ergonomic keyboard (QwertyKeys Neo Ergo) where thumb clusters handle modifiers. Drop this at `~/.tmux.conf`.

### Prefix: `Ctrl+Space`

`Ctrl+Space` is the recommended prefix for the Neo Ergo. On a split ergo with thumb clusters, Ctrl lives under one thumb and Space under the other — it's a natural two-thumb chord with zero hand contortion. It avoids every common conflict: `Ctrl+a` clobbers shell beginning-of-line, `Ctrl+b` is a hand cramp on any board, and `Ctrl+s` conflicts with terminal flow control.

Alternative worth considering: dedicate a key on the Neo Ergo's thumb cluster to send a custom keycode via VIA (e.g., `F13`) and bind that as the prefix. Single-key prefix, zero conflicts, zero chords. This is a VIA/QMK rabbit hole but it's the endgame if you want maximum ergonomics.

### Config

```bash
# =============================================================
# ~/.tmux.conf — Neo Ergo optimized
# =============================================================

# ── Prefix ───────────────────────────────────────────────────
# Ctrl+Space as prefix (ergonomic on split thumb clusters)
unbind C-b
set -g prefix C-Space
bind C-Space send-prefix

# ── Splits (memorable keys) ──────────────────────────────────
# | for vertical split, - for horizontal (visual mnemonics)
unbind '"'
unbind %
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"

# New windows also inherit current path
bind c new-window -c "#{pane_current_path}"

# ── Pane Navigation (vim-style) ──────────────────────────────
bind h select-pane -L
bind j select-pane -D
bind k select-pane -U
bind l select-pane -R

# ── Pane Resizing ────────────────────────────────────────────
# Hold prefix, then H/J/K/L to resize in 5-cell increments
bind -r H resize-pane -L 5
bind -r J resize-pane -D 5
bind -r K resize-pane -U 5
bind -r L resize-pane -R 5

# ── Mouse Mode ───────────────────────────────────────────────
# Click to select pane, drag to resize, scroll to scroll.
# Training wheels while building muscle memory. Turn off later
# if it starts conflicting with text selection.
set -g mouse on

# ── Session Switching ────────────────────────────────────────
# prefix + s  → session picker (already default, listed for reference)
# prefix + S  → quick switch to cockpit session
bind S switch-client -t cockpit

# ── Window Navigation ────────────────────────────────────────
# prefix + n/p for next/prev (already default)
# prefix + <number> for direct jump (already default)
# Alt+h / Alt+l for prev/next without prefix
bind -n M-h previous-window
bind -n M-l next-window

# ── Reload Config ────────────────────────────────────────────
bind r source-file ~/.tmux.conf \; display "Config reloaded"

# ── Copy Mode (vi keys) ──────────────────────────────────────
setw -g mode-keys vi
bind -T copy-mode-vi v send -X begin-selection
bind -T copy-mode-vi y send -X copy-pipe-and-cancel "pbcopy"

# ── Quality of Life ──────────────────────────────────────────
set -g base-index 1              # Windows start at 1, not 0
setw -g pane-base-index 1        # Panes too
set -g renumber-windows on       # Renumber on close (no gaps)
set -g escape-time 0             # No delay after Escape
set -g history-limit 50000       # Generous scrollback
set -g display-time 2000         # Status messages stay 2s
set -g status-interval 5         # Refresh status bar every 5s
set -g focus-events on           # Pass focus events to apps

# ── Appearance (minimal) ─────────────────────────────────────
set -g status-style "bg=default,fg=white"
set -g status-left "#[bold]#S #[default]│ "
set -g status-left-length 20
set -g status-right "%H:%M"
set -g status-right-length 10
set -g window-status-current-style "fg=cyan,bold"
set -g window-status-style "fg=colour244"
set -g pane-border-style "fg=colour238"
set -g pane-active-border-style "fg=cyan"

# ── Terminal ─────────────────────────────────────────────────
set -g default-terminal "tmux-256color"
set -ag terminal-overrides ",xterm-256color:RGB"
```

### Cheat Sheet (print this or throw it in Obsidian)

```
PREFIX = Ctrl+Space

SESSIONS
  prefix + s       Session picker (tree view, arrow keys + enter)
  prefix + S       Jump to cockpit
  prefix + d       Detach (session stays alive)
  prefix + $       Rename session
  tmux new -s X    New session named X
  tmux ls          List all sessions

WINDOWS (tabs)
  prefix + c       New window
  prefix + ,       Rename window
  prefix + n/p     Next / previous window
  prefix + 1-9     Jump to window by number
  Alt+h / Alt+l    Prev / next (no prefix needed)

PANES (splits)
  prefix + |       Split vertical
  prefix + -       Split horizontal
  prefix + h/j/k/l Navigate panes (vim-style)
  prefix + H/J/K/L Resize panes
  prefix + z       Toggle pane zoom (fullscreen one pane)
  prefix + x       Close pane

MISC
  prefix + r       Reload config
  prefix + [       Enter scroll/copy mode (q to exit)
```
