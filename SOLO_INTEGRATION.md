# Solo Integration: Awareness Layer for Project Stacks

Three features inspired by SoloTerm, adapted for cockpit's role as an
observatory (not a process runner). Each builds on the previous.

## 1. solo.yml Discovery (start here)

**What:** New source (`sources/solo.go`) that discovers and parses `solo.yml`
files found in configured repo paths. Cockpit reads the manifest to understand
what each project *expects* to be running — without owning the lifecycle.

**Why this first:** It's the foundation. The other two features need to know
"what should be running" before they can report on health or expose it via MCP.
It's also the smallest surface area — purely additive, no existing code changes.

**What it gives the user:** A new "Stack" column or section in the Sessions/Repos
view showing the declared processes for each project. At a glance: "my-app wants
a dev server, queue worker, and Claude Code."

### Shape

```go
// sources/solo.go

type SoloProcess struct {
    Name       string
    Command    string
    AutoStart  bool
    AutoRestart bool
    Env        map[string]string
}

type SoloManifest struct {
    ProjectName string
    RepoPath    string            // which repo this was found in
    Processes   []SoloProcess
    Raw         map[string]any    // full parsed YAML for future use
}

func DiscoverSoloManifests(ctx context.Context, repoPaths []string) ([]SoloManifest, error)
```

- Walk each configured repo path, look for `solo.yml` at root
- Parse with `gopkg.in/yaml.v3` — only extract the fields cockpit cares about
- Return alongside existing repo data on the normal refresh interval

### Config addition

```toml
[solo]
enabled = true  # opt-in, off by default until stable
```

No other config needed — discovery is driven by the existing `[[repos]]` paths.

### TUI changes

Option A — extend the Repos panel: add a "stack" indicator per repo (e.g.
`⚙ 4 processes`). Selecting it could show the process list in a tooltip or
expand row.

Option B — new panel `PanelStack` that shows all solo.yml processes across
projects, grouped by project. This is more visible but costs screen real estate.

Leaning toward A to start — it's less invasive and the repos panel already has
room for another column.

---

## 2. Crash/Health Awareness in Signals

**What:** Cross-reference solo.yml declarations against actual running state.
If a project says it wants a queue worker but nothing matching is running,
surface that in Signals.

**Depends on:** solo.yml discovery (#1).

### Detection strategy

Cockpit already shells out to tmux. We can extend this:

1. **tmux pane health** — `tmux list-panes -t <session> -F '#{pane_pid} #{pane_dead} #{pane_dead_status}'`
   tells us if a pane's process exited and its exit code. Dead pane + non-zero
   exit = crashed.

2. **Process matching** — For each `SoloProcess.Command`, check if a matching
   process exists in the session's panes (by command prefix or PID). This is
   fuzzy but useful: `php artisan serve` in solo.yml matched against the
   actual pane command.

3. **Missing process detection** — If solo.yml declares 4 processes but the
   tmux session only has 2 panes, something's not running.

### Signals additions

```go
type ProcessSignal struct {
    Project     string
    ProcessName string
    Status      ProcessHealth  // Running | Crashed | Missing | Unknown
    ExitCode    int            // if crashed
    Since       time.Time      // when it was last seen healthy
}
```

New signal types for the Signals pane:
- `✗ my-app: queue worker crashed (exit 1)`
- `? my-app: dev server not found in session`
- `✓ my-app: 4/4 processes running`

### Stretch: non-solo.yml health

Even without a solo.yml, cockpit can detect dead panes in any tmux session.
This is cheap and useful independently. Could ship this as a quick win before
the full solo.yml cross-reference.

---

## 3. MCP Bridge

**What:** `cockpit mcp` subcommand that runs an MCP server (stdio transport),
exposing cockpit's aggregated state to CLI agents like Claude Code.

**Depends on:** More valuable with #1 and #2, but could ship with just the
existing data (sessions, repos, signals).

### Why this matters

Right now Claude Code is blind to what else is going on in your dev environment.
With an MCP bridge, an agent can ask cockpit "what's the state of my world?"
before making decisions. This is Solo's MCP pitch, but lighter — cockpit doesn't
run the processes, it just reports on them.

### Resources / Tools to expose

**Resources (read-only state):**
- `cockpit://sessions` — all tmux sessions with attached/detached/stale status
- `cockpit://repos` — git status across all configured repos
- `cockpit://signals` — current signal state (failing CI, unpushed, crashed processes)
- `cockpit://stack/{project}` — solo.yml processes + health for a specific project

**Tools (actions):**
- `cockpit_jump_session(name)` — switch tmux to a session (same as pressing Enter in the TUI)
- `cockpit_capture_pane(session, lines)` — grab recent output from a tmux pane
- `cockpit_signals()` — return current signals as structured JSON

### Implementation

Use `github.com/mark3labs/mcp-go` (the standard Go MCP SDK). The server reads
the same config file and calls the same `sources.*` functions the TUI uses.

```
cockpit mcp  # runs stdio MCP server, configure in claude code settings:
```

```json
// ~/.claude/settings.json
{
  "mcpServers": {
    "cockpit": {
      "command": "cockpit",
      "args": ["mcp"]
    }
  }
}
```

### Wire format example

Agent asks: "What needs my attention?"

```json
// cockpit_signals() response
{
  "signals": [
    {"type": "crashed_process", "project": "my-app", "process": "queue:work", "exit_code": 1},
    {"type": "unpushed", "project": "side-proj", "commits": 3},
    {"type": "failing_ci", "project": "my-app", "branch": "feat/auth"}
  ]
}
```

---

## Implementation order

```
1. solo.yml discovery       — new source, small TUI change, no deps
2. dead-pane detection      — quick win, works without solo.yml
3. solo.yml health signals  — cross-reference #1 + #2
4. MCP bridge (basic)       — expose sessions/repos/signals
5. MCP bridge (full)        — add stack health from #3
```

Steps 1 and 2 are independent and could be done in parallel.
Step 4 could also start early since it only needs the existing sources.
