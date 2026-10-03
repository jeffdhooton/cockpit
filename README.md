# Cockpit

A tmux-native terminal dashboard for developers juggling multiple projects. One command gives you a persistent home screen with live project status, quick-capture for fleeting thoughts, and one-keystroke jumping between contexts.

![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)

```
┌─ COCKPIT ────────────────────────────────────────┐
│ ╭────────────╮ ╭────────────╮ ╭────────────╮     │
│ │ 1 my-app   │ │ 2 scry     │ │ 3 side-proj│     │
│ │ ● working  │ │ ● idle 4m  │ │ ● attached │     │
│ │ feat/auth✗3│ │ main ✓ ↑2  │ │ main ✓     │     │
│ ╰────────────╯ ╰────────────╯ ╰────────────╯     │
│ ╭────────────╮ ╭────────────╮                    │
│ │   dotfiles │ │   notes    │                    │
│ │ ● no session │ ● no session                    │
│ │ main ✓     │ │ main ✗ 3   │                    │
│ ╰────────────╯ ╰────────────╯                    │
├─ my-app ─────────────────────────────────────────┤
│ $ go test ./...                                  │
│ ok  github.com/you/my-app  0.31s                 │
├──────────────────────────────────────────────────┤
│ HJKL nav · 1-0 open · ENTER jump · N new · D dash│
└──────────────────────────────────────────────────┘
```

## What it does

- **Sessions** — The default view: every host's tmux sessions on the left, sorted so the ones with an agent waiting on you come first; the selected session's windows and panes on the right, each labelled as an agent (with its hook-reported status), a configured process (with its exact state), or a shell, plus a live preview of the selected pane. Enter attaches to that exact pane and never starts anything; `o` opens a project with its processes.
- **Grid** — Press `g` for the tile wall: running tmux sessions and saved repos as one grid. `hjkl` to move, Enter to jump; a dormant repo gets a session created on the spot. The first ten running sessions carry a digit — press `1`-`9` or `0` to drop straight into one without moving the cursor there first. Each remote host is a single box here; Enter opens its own grid and Backspace comes back. The grid packs in as many tiles as fit — a dozen across on an ultrawide, down to one on a phone over SSH. Tiles stay a readable width; extra room buys more of them rather than wider ones.
- **On a phone** — Below 70 columns the tile collapses to a single line: the status marker and the name, nothing else. Branch, dirty counts and process counts are desktop detail. Three rows a tile instead of five means half again as many sessions on the screen, and the digits mean reaching one is a single keystroke.
- **Capture** — `c` in any view, or `cockpit cap "idea"` from any terminal, appends a line to your Obsidian Today file. Nothing is displayed; triage in Obsidian.
- **Attention** — Press `a` anywhere for the queue of things that need a person: an agent waiting on input, a crashed process, a failed CI run, a stopped Hermes gateway. Enter takes you to the exact pane or process; sources that could not be read are listed, not hidden.
- **Processes** — A project can declare background processes. Jumping to it brings them up as tmux windows beside your shell. Press `p` on a project tile to inspect, start, stop, restart or adopt them; a stop stays stopped until you start it again.
- **Doctor** — `cockpit doctor` explains what is working, what is unavailable, and the smallest next action, without changing anything.
- **Daemon** — A local tool server so agents like Claude Code and Codex can inspect and drive the workspace.

Everything refreshes automatically. Local sources (tmux, git) every 5 seconds, GitHub every 60 seconds. Set `default_view = "grid"` to start on the tile wall; `"sessions"` is the default and `"dashboard"` is accepted as an alias of it.

## Install

Requires tmux and git. Go is not needed to run a release. Optional: `gh`
(GitHub CLI) for PR/CI signals.

Download a release binary (macOS or Linux, amd64 or arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/jeffdhooton/cockpit/main/scripts/install.sh | sh
```

That verifies the archive's checksum and installs `cockpit` into
`~/.local/bin` (override with `INSTALL_DIR`; pin a tag with
`COCKPIT_VERSION=v0.3.0`). Or grab an archive from the
[releases page](https://github.com/jeffdhooton/cockpit/releases) and put
the `cockpit` binary on your PATH.

From source (Go 1.24+):

```bash
go install github.com/jeffdhooton/cockpit@latest
```

or

```bash
git clone https://github.com/jeffdhooton/cockpit.git
cd cockpit
go build -o cockpit .
cp cockpit ~/.local/bin/  # or anywhere in your PATH
```

`cockpit version` prints the release tag; a source build prints `dev`.

## Setup

```bash
# Pick projects from your running tmux sessions or a directory scan, review
# the exact change, then save. Needs a terminal; writes nothing on cancel.
cockpit init --interactive

# Or write the minimal template and edit it by hand
cockpit init
$EDITOR ~/.config/cockpit/config.toml

# Then check the installation
cockpit doctor
```

The guided setup scans at most two directory levels and 200 candidates,
skips dependency directories, follows a symlink you name explicitly but
never traverses symlinked directories, and keeps distinct git worktrees
apart. It merges into an existing config as text, preserving your comments,
and backs the old file up first. If the file changed while you were
reviewing, it shows the new diff instead of writing. It installs no hooks,
registers no MCP servers and starts nothing.

A new config disables GitHub and the daemon (`enabled = false`) so a fresh
install has no misleading warnings; turn them on when you want them.

The config looks like this:

```toml
[general]
session_name = "cockpit"
refresh_interval = 5

[obsidian]                          # optional: omit on a machine with no vault
today_file = "~/Documents/Vault/Cockpit/today.md"
inbox_file = "~/Documents/Vault/Cockpit/inbox.md"

[[repos]]
path = "~/workspace/my-app"
label = "my-app"

[[repos]]
path = "~/workspace/side-project"
label = "side-proj"

[github]
enabled = true                      # off in a fresh config; needs gh
refresh_interval = 60

[signals]
stale_session_threshold = "24h"
show_stale_sessions = true
show_unpushed = true
show_failing_ci = true

[daemon]
enabled = true                      # off in a fresh config
port = 45679
```

The `[obsidian]` section is optional. Without it, `c` and `cockpit cap` say
there is nowhere to capture to, which is the right shape for a machine that
only runs the daemon. Create the task
files if they don't exist:

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

## Session view

```text
╭─ SESSIONS ───────────────────╮╭─ LOCAL / CPIT ─────────────────────────────────╮
│ LOCAL                        ││ main ✗85  $3 · server 4060 · 5 windows          │
│▎1 cpit      ● needs you 2 ag ││  @0 zsh       shell                             │
│ 2 scry      ● working  1 ag  ││ !@2 reviewer  ● needs you  claude · 4m          │
│ 3 docket    ○ unknown        ││  @3 dev       Running      process              │
│ MINI ● up                    ││ ─── reviewer ── %557 · ~/workspace/cpit ────── │
│ 4 api       ● working  1 ag  ││   Allow Bash(rm -rf node_modules)?              │
│ HALO2 ⚠ unavailable          ││   ❯ 1. Yes  2. Yes, and don't ask again  3. No  │
│  dormant (12): JRP advocates ││                                                 │
╰──────────────────────────────╯╰─────────────────────────────────────────────────╯
```

Sessions are the unit. Each host lists its live sessions with a status
rolled up from its agent panes (needs you, working, idle, unknown), the
number of agents, and running/total processes; configured repos with no
session sit under `dormant`. Sessions that need you sort to the top of
their host; the cursor follows the session, not the row. A host that stops
answering keeps its sessions marked stale with attach and preview disabled.

A pane is an **agent** when a hook has reported for it or its foreground
command is `claude` or `codex`; without a report its status is `unknown`,
never guessed. A pane is a **process** when it is one of the project's
configured processes, shown with the process panel's exact wording. The
rest are shells or named by their command. `!` marks a pane that has an
attention item. The preview is the pane's visible screen, read only for the
selected pane and only from hosts whose last read succeeded.

| Key | Action |
|-----|--------|
| `j` / `k` | Move between sessions, or between panes when the pane list has focus |
| `Tab` / `Shift-Tab` | Move focus between the session list and the pane list |
| `1`–`9`, `0` | Attach to the numbered session |
| `Enter` | Attach to the selected pane. Validated first; never creates a session or runs a command |
| `o` | Open project: create the session if missing and start its auto-start processes |
| `p` | Process panel for the selected session |
| `a` | Attention queue |
| `l` | Full-screen preview of the selected pane |
| `n` | New session |
| `/` | Filter sessions by host or name; `Esc` clears |
| `c` | Capture a line to the Today file |
| `g` | Grid view (`d` or `g` in the grid comes back) |
| `r` | Refresh all sources |
| `q` | Quit (the tmux session stays alive; run `cockpit` to return) |

Below 90 columns the session list stacks above the detail; at 40 columns
the detail shows the pane list and `l` opens the preview full screen.

Agents can read the same tree through the read-only `cockpit_workspaces`
MCP tool (`schema_version: 1`) and read a pane it names with
`cockpit_read_output` and `pane_id`.

## Attention

`a` opens the queue from the grid or the dashboard, always across every
host. The hint bar carries a persistent `Attention N` count of fresh,
actionable items, with a separate count of sources that could not be read:
unknown coverage is not zero work.

| Row | Evidence | Enter |
|---|---|---|
| Needs input | A hook reported that this pane's agent is waiting | Attach to that pane |
| Exited (code N) | A configured process died and was not stopped by you | Open the process panel with it selected, output visible |
| CI failed | The latest run on `main` failed (feature branches are not checked) | Details with repository, branch, run id and URL; `o` opens it |
| Gateway stopped | The Hermes dashboard answered and said so | Gateway details |

`Tab` switches to **Housekeeping** — unpushed commits and stale sessions —
which never inflates the count. `/` filters by host, project or kind, `r`
refreshes, and `Esc` returns to exactly the view, host and tile you came
from. Selection follows the item, not the row: if the item you selected
resolves while you are looking, a placeholder holds its place until you
move, so a queued Enter cannot act on whatever slid in underneath.

Opening a row never starts, restarts or reconciles anything: attaching goes
through an attach-only path that revalidates the server, session, window
and pane identities first. A target that was renamed is still reached; one
that was removed or replaced says "This target is no longer available".

A host or source that fails to read keeps its last-known items, marked
stale with actions disabled, and appears in a coverage section that filters
never hide. Only a successful read clears an item. The empty state is "No
attention items observed", never a claim that everything is healthy.

Agents report per pane. Two blocked agents in one session are two rows, and
one finishing does not clear the other; a late event from an exited run
cannot recolour its replacement. A machine still running an older hook
reports at session level, which the queue shows as limited coverage and
`cockpit doctor` flags for update.

The same records are available to agents as the read-only `cockpit_attention`
MCP tool (`schema_version: 1`), and `cockpit_signals` keeps its envelope,
adapted from the same facts.

## Process panel

`p` on a project tile (`P` from the dashboard's Sessions or Repos panel)
opens the panel. Opening only reads: a dormant project lists its declared
commands as **Not started** without creating a session or starting anything.
A host box is not a project; an unmanaged session shows its windows for
inspection only.

```text
Processes · mini/api                         Connected

  dev       Running          starts with project
> worker    Stopped by you   starts with project
  tests     Not started      manual

Other windows
  shell     Unmanaged

Enter attach · l output · s start · x stop · R restart · Esc back
```

| Key | Action |
|---|---|
| `Enter` | Attach to the process's window and pane (validated first) |
| `l` | Read the last 200 lines; `L` loads up to 2,000; follows the tail while you are at the bottom |
| `s` | Start the configured command; no-op with feedback if already running |
| `x` | Stop, after a confirmation naming host, project and process. Defaults to Cancel and says plainly that the window's work is terminated, not gracefully shut down |
| `R` | Restart in place, with the same confirmation; a dead row offers Start instead |
| `A` | Adopt a window cockpit did not start that matches a configured process, after reviewing which window. Adoption launches nothing |
| `c` | Show the configured command |
| `r` | Refresh; `Esc` returns to where you came from |

States: **Running**, **Exited (code N)**, **Completed** (exit 0, no
alert), **Not started**, **Stopped by you**, **Unknown**. Ready/error
pattern matches from old scrollback are never treated as health.

**Stop stays stopped.** A stop records an override on the tmux session
(`@cockpit_stopped_<name>`) before terminating the window. Entering the
project again, restarting the TUI or the daemon, or another client over ssh
all honour it: the process stays down until you press `s` (or call
`cockpit_start`). The override lives as long as the tmux session; a server
restart or reboot clears it, and `auto_start` in config remains the
permanent policy. Every client that reconciles a session must be on a
version that understands overrides; doctor reports mismatches it can see.

**Ownership.** Cockpit marks the windows it creates. A pre-existing window
that merely shares a process's name is inspection-only until you adopt it,
and Start refuses to launch a duplicate beside it. A managed window that has
been split cannot be stopped or restarted from here, because that would
kill the other panes. Stop and Restart address windows by tmux id, so a
rename or reorder cannot redirect them.

**One writer at a time.** Lifecycle operations and project-entry
reconciliation take a per-project lock held on the tmux server itself, so
the TUI, the daemon and a client on another machine cannot double-launch;
the lock expires if its holder crashes. A lost ssh response after a
mutation is reported as an unknown outcome and never retried automatically.

## Doctor

```bash
cockpit doctor              # local core and enabled integrations
cockpit doctor --host mini  # plus one configured ssh host
cockpit doctor --all-hosts  # plus every host, three at a time
cockpit doctor --json       # one versioned document on stdout
```

Doctor reads and reports. It never installs, starts, edits, trusts, creates
sessions or runs project commands, and it has no `--fix`. Each check is
`PASS`, `WARN`, `FAIL` or `SKIP` with a bounded reason and a next step shown
as text; nothing is executed by selecting a result.

- **Fail** means core functionality cannot work in the requested scope:
  missing tmux, invalid config, an unreadable configured repository, a host
  you asked for that cannot be reached.
- **Warn** means an enabled optional feature is degraded: the daemon is not
  answering, `gh` is not logged in, a hook is untrusted or stale.
- **Skip** means disabled, not installed, not probed, or blocked by an
  earlier failure, and says which.

No tmux server and no configured repos are both valid. Exit 0 means no
failures (warnings allowed), 1 means diagnostic failures, 2 means bad usage.
Hosts use your `ssh` in batch mode over a transient connection: no host key
is accepted and no prompt can appear. A remote cockpit that supports doctor
contributes its own report; an older one is reported as unverified. Checks
are bounded (2s local, 5s to connect, 10s per host, 30s in all) and a report
that ran out of time says which checks did not complete.

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

Twelve of the tools mirror Helm's suite one for one; `cockpit_capture`,
`cockpit_tasks`, `cockpit_attention` and `cockpit_workspaces` are cockpit's own. `cockpit_start`,
`cockpit_stop` and `cockpit_restart` go through the same process service as
the TUI: the same lock, the same window identity check, the same stop
override. A remote project is addressed as `host/label`; a bare label is
always local. Results carry `desired_state`, `outcome`, `exit_code` and the
window id beside the legacy `state`, and a read that fails returns
`outcome: unavailable` rather than an empty list. One difference is worth knowing:
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
| `○ unknown` | No hook has reported for this session's agents; nothing is guessed |
| `○ no session` | Nothing to attach to |

Status is reported or unknown: Cockpit does not poll pane contents to guess
whether an agent is busy. A status older than ten minutes is treated as
stale and shows as unknown, so a crashed agent cannot stay "working"
forever.

**Codex trust is handled for you.** Codex leaves a newly installed hook
untrusted and does not run it until approved, and trust is pinned to a hash
of the hook's configuration — editing the command untrusts it again. `hook
install` finishes the job the way Codex's own approve action would, by
recording that hash through `codex app-server`, and reports `trusted N
hooks; they are live`. Only cockpit's own hooks are ever trusted. If Codex
cannot be started the install says so and the hooks stay inferred until you
approve them inside Codex.

A waiting agent also appears first in the `cockpit_signals` tool, above a
dead process.

## Remote hosts

Cockpit can watch and drive a second machine over SSH. Each host is one tile
at the top level — the machine, not its contents. Enter opens that machine's
own grid, which reads exactly like the root one, and Backspace comes back.
Inside it, Enter on a tile brings the remote session and its processes up,
then drops you into it.

A box says only whether the link is up. Three machines with a dozen sessions
between them are three tiles at the root, not thirty-odd, and the digits stay
worth pressing: they renumber per grid, so `1` is the first session on the
machine you are looking at.

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

**Unreachable.** When a host stops answering, its box and its tiles keep their
last-known state under `⚠ unreachable` and the poll backs off to a minute. Nothing is
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

A Hermes gateway on the tailnet gets one tile — gateway running or stopped,
and which platforms are connected — from its dashboard's status endpoint,
which needs no token. A stopped gateway also appears in Signals. The tile
lives on the level its machine is on: with a `host` it sits inside that
host's box, and without one it stays at the top level.

```toml
[[hermes]]
label = "hermes"
url = "http://100.96.45.73:9119"
host = "mini"   # optional: a [[hosts]] entry
```

With `host` set, Enter opens a shell on that machine: a remote tmux session
named for the tile, starting in the remote home directory, reached through
the same view window a remote project uses. Hermes itself runs under
launchd rather than tmux, so this is the box, not the process. Without
`host` the tile is read-only.

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
