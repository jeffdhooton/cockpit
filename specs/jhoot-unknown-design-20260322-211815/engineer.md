# Engineer Spec: Cockpit — tmux-native Terminal Command Center

Build a Go + Bubbletea single-binary TUI dashboard (`cockpit`) that aggregates tmux sessions, git repo status, Obsidian tasks, and GitHub signals into a persistent tmux-native command center.

## Setup

Project root: `/Users/jhoot/workspace/cockpit`

```
cockpit/
├── go.mod
├── go.sum
├── main.go                    # Entry point, tmux bootstrap, CLI routing
├── cmd/
│   └── root.go                # CLI commands: default (TUI), init, cap
├── config/
│   └── config.go              # TOML config loader + validation
├── tui/
│   ├── app.go                 # Root Bubbletea model, layout engine, panel focus
│   ├── styles.go              # Lipgloss styles, Tokyo Night palette, shared components
│   ├── sessions.go            # Sessions panel (horizontal card flow)
│   ├── repos.go               # Repos panel (table with status indicators)
│   ├── tasks.go               # Today panel (interactive checkbox list)
│   ├── inbox.go               # Inbox panel (list + capture text input)
│   ├── signals.go             # Signals panel (aggregated attention items)
│   └── keyhints.go            # Context-sensitive bottom key bar
├── sources/
│   ├── source.go              # Source interface definition
│   ├── tmux.go                # tmux session data via CLI
│   ├── git.go                 # Git repo status via CLI
│   ├── obsidian.go            # Obsidian markdown file read/write
│   └── github.go              # GitHub PR/CI via gh CLI
├── config_template.go         # Embedded TOML template for `cockpit init`
└── testdata/
    ├── today.md               # Test fixture: sample today file
    ├── inbox.md               # Test fixture: sample inbox file
    └── config.toml            # Test fixture: sample config
```

## Dependencies

- **Go 1.22+** (installed: go1.22.2 darwin/arm64)
- `github.com/charmbracelet/bubbletea` v1 (latest stable, NOT v2 — v2 is not released yet)
- `github.com/charmbracelet/lipgloss` v1 (latest stable)
- `github.com/charmbracelet/bubbles` — for `textinput`, `spinner`, `list` components
- `github.com/BurntSushi/toml` — TOML config parsing
- `github.com/spf13/cobra` — CLI command routing (`cockpit`, `cockpit init`, `cockpit cap`)
- System tools assumed present: `tmux`, `git`, `gh` (GitHub CLI)
- **No CGO** — pure Go, cross-compilable

## Implementation Details

### Phase 1: Project Scaffold + Config + CLI Skeleton

**Files to create:** `go.mod`, `main.go`, `cmd/root.go`, `config/config.go`, `config_template.go`, `testdata/config.toml`

**Behavior:**

1. **`go mod init github.com/jhoot/cockpit`** — initialize Go module.

2. **`config/config.go`** — Define config struct and loader:
   ```go
   type Config struct {
       General  GeneralConfig
       Obsidian ObsidianConfig
       Repos    []RepoConfig
       GitHub   GitHubConfig
       Signals  SignalsConfig
   }
   type GeneralConfig struct {
       SessionName     string `toml:"session_name"`     // default: "cockpit"
       RefreshInterval int    `toml:"refresh_interval"` // seconds, default: 5
   }
   type ObsidianConfig struct {
       VaultPath string `toml:"vault_path"`
       TodayFile string `toml:"today_file"`
       InboxFile string `toml:"inbox_file"`
   }
   type RepoConfig struct {
       Path  string `toml:"path"`
       Label string `toml:"label"`
   }
   type GitHubConfig struct {
       Enabled         bool `toml:"enabled"`          // default: true
       RefreshInterval int  `toml:"refresh_interval"` // seconds, default: 60
   }
   type SignalsConfig struct {
       StaleSessionThreshold string `toml:"stale_session_threshold"` // default: "24h"
       ShowStaleSessions     bool   `toml:"show_stale_sessions"`     // default: true
       ShowUnpushed          bool   `toml:"show_unpushed"`           // default: true
       ShowFailingCI         bool   `toml:"show_failing_ci"`         // default: true
   }
   ```
   - `Load(path string) (*Config, error)` — reads TOML file, expands `~` via `os.UserHomeDir()`, validates required fields. Returns descriptive errors with context (e.g., `"config: vault_path is required"`).
   - `DefaultConfigPath() string` — returns `~/.config/cockpit/config.toml`.
   - Expand `~` in all path fields (`VaultPath`, `Repos[].Path`) after parsing.
   - Validate: `VaultPath` must be non-empty, `RefreshInterval` must be > 0, `StaleSessionThreshold` must parse with `time.ParseDuration`.

3. **`config_template.go`** — Embed the commented TOML template as a `const` or `embed.FS` string. The template should match the config spec from the plan, with every field commented and explained.

4. **`cmd/root.go`** — Cobra CLI with three commands:
   - `cockpit` (root) — will launch TUI (stub for now: print "cockpit TUI starting...")
   - `cockpit init` — creates `~/.config/cockpit/` dir and writes `config.toml` template. If file exists, print "Config already exists at <path>" and exit 0 (do not overwrite).
   - `cockpit cap <thought>` — stub for now: print "capture: <thought>"

5. **`main.go`** — just calls `cmd.Execute()`.

6. **`testdata/config.toml`** — a valid test config with 2 repos, obsidian paths pointing to `testdata/`.

**Tests:** `config/config_test.go`
- Test `Load` with valid config (from `testdata/config.toml`)
- Test `Load` with missing file → returns error containing "no config found"
- Test `Load` with malformed TOML → returns parse error
- Test `Load` validates required fields (empty vault_path → error)
- Test `~` expansion in paths
- Test default values applied for optional fields

**Gate check:**
```bash
go build ./...          # must compile with 0 errors
go test ./...           # must pass with 0 failures
go vet ./...            # must pass with 0 issues
```

---

### Phase 2: Sources — tmux + git

**Files to create:** `sources/source.go`, `sources/tmux.go`, `sources/git.go`

**Behavior:**

1. **`sources/source.go`** — Define the data types (no interface needed yet; each source is called directly):
   ```go
   type TmuxSession struct {
       Name       string
       Windows    int
       Attached   bool
       LastUsed   time.Time  // parsed from pane_last_used epoch
   }
   type GitRepoStatus struct {
       Label        string
       Path         string
       Branch       string
       Dirty        bool
       DirtyCount   int      // number of modified/untracked files
       Unpushed     int      // commits ahead of upstream
       LastCommit   string   // first line of last commit message
       Error        error    // non-nil if repo path invalid or git fails
   }
   ```

2. **`sources/tmux.go`** — `GetTmuxSessions(ctx context.Context) ([]TmuxSession, error)`:
   - Execute `tmux list-sessions -F '#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_last_attached}'` with `exec.CommandContext`.
   - Parse tab-delimited output. `session_attached` is "1" or "0". `session_last_attached` is Unix epoch (parse with `strconv.ParseInt` → `time.Unix`).
   - 10-second context timeout.
   - If tmux is not running, return empty slice (not an error) — tmux server may not be up.

3. **`sources/git.go`** — `GetGitStatus(ctx context.Context, repos []config.RepoConfig) ([]GitRepoStatus, error)`:
   - For each repo, run in parallel (use `sync.WaitGroup` or `errgroup`):
     - `git -C <path> status --porcelain` → count lines for `DirtyCount`, set `Dirty` if count > 0.
     - `git -C <path> rev-parse --abbrev-ref HEAD` → `Branch`.
     - `git -C <path> rev-list --count @{upstream}..HEAD 2>/dev/null` → `Unpushed`. If no upstream, set to 0.
     - `git -C <path> log -1 --pretty=format:%s` → `LastCommit`.
   - If repo path doesn't exist, set `Error` on that `GitRepoStatus` (don't fail the whole batch).
   - Each subprocess gets a 10-second context timeout.

**Tests:** `sources/tmux_test.go`, `sources/git_test.go`
- tmux: Test parsing of sample `tmux list-sessions` output (use a helper that injects output instead of calling real tmux). Create a `parseTmuxOutput(output string) ([]TmuxSession, error)` function that's independently testable.
- git: Test parsing of `git status --porcelain` output, `rev-list` count output. Create parse helpers that are independently testable. For integration, test with a temp git repo (`git init` in `t.TempDir()`).

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
```

---

### Phase 3: Sources — Obsidian

**Files to create:** `sources/obsidian.go`, `testdata/today.md`, `testdata/inbox.md`

**Behavior:**

1. **`sources/obsidian.go`**:
   ```go
   type Task struct {
       Text    string
       Done    bool
       Line    int    // 1-indexed line number in source file
   }
   ```
   - `ReadTasks(filePath string) ([]Task, error)` — Read file, find all lines matching `^\s*- \[([ x])\] (.+)$`. Preserve line numbers. Non-task lines are ignored during read but preserved during write.
   - `ToggleTask(filePath string, lineNum int) error` — Read file into lines slice. At `lineNum` (1-indexed), swap `- [ ]` ↔ `- [x]`. Write back atomically: write to `.tmp` file in same dir, then `os.Rename`. This preserves all non-task content verbatim.
   - `AppendInbox(filePath string, text string) error` — Append `- [ ] <text> — YYYY-MM-DD HH:MM\n` to file. Create file if it doesn't exist. Use `os.OpenFile` with `O_APPEND|O_CREATE|O_WRONLY`.
   - Expand `~` in file paths before use.

2. **Test fixtures:**
   - `testdata/today.md`:
     ```markdown
     # Today

     Some prose here.

     - [x] Ship auth fix
     - [ ] Review epub export PR
     - [ ] Call Jake re: partnership

     ## Notes
     Random notes below tasks.
     ```
   - `testdata/inbox.md`:
     ```markdown
     - [ ] fix auth bug in book-suite
     - [ ] look into bubbletea v2
     ```

**Tests:** `sources/obsidian_test.go`
- `ReadTasks`: parses tasks from `testdata/today.md`, returns 3 tasks with correct Done/Text/Line values.
- `ReadTasks`: ignores non-task lines (headings, prose, blank lines).
- `ReadTasks`: returns empty slice for non-existent file (not error — file may not be created yet).
- `ToggleTask`: toggles `[ ]` → `[x]` at correct line, preserves all other content byte-for-byte.
- `ToggleTask`: toggles `[x]` → `[ ]` (reverse).
- `AppendInbox`: appends formatted line to existing file.
- `AppendInbox`: creates file if missing.
- All write tests use `t.TempDir()` copies, not the fixtures directly.

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
```

---

### Phase 4: Sources — GitHub

**Files to create:** `sources/github.go`

**Behavior:**

1. **`sources/github.go`**:
   ```go
   type GitHubStatus struct {
       PRsAwaitingReview int
       PRsDraft          int
       FailingChecks     int      // count of repos with failing CI
       RepoChecks        []RepoCheck
       Error             error
   }
   type RepoCheck struct {
       RepoLabel string
       PRCount   int
       CIStatus  string  // "passing", "failing", "pending", "none"
   }
   ```
   - `GetGitHubStatus(ctx context.Context, repos []config.RepoConfig) (*GitHubStatus, error)`:
     - For each repo, run in parallel:
       - `gh pr list --repo <owner/repo> --state open --json number,title,isDraft,reviewDecision --limit 10` — parse JSON output. Count PRs where `reviewDecision` is `"REVIEW_REQUIRED"` for `PRsAwaitingReview`.
       - `gh run list --repo <owner/repo> --branch <default-branch> --limit 1 --json status,conclusion` — if latest run `conclusion` is `"failure"`, increment `FailingChecks`.
     - Derive `owner/repo` from `git -C <path> remote get-url origin` → parse GitHub URL (handle both `https://github.com/owner/repo.git` and `git@github.com:owner/repo.git`).
     - If `gh` is not installed or not authenticated, set `Error` and return partial data.
   - 10-second context timeout per subprocess.

**Tests:** `sources/github_test.go`
- Test GitHub URL parsing (HTTPS and SSH formats).
- Test JSON output parsing for `gh pr list` and `gh run list` with sample JSON strings.
- Extract parsers as testable functions: `parsePRList(jsonBytes []byte)`, `parseRunList(jsonBytes []byte)`.

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
```

---

### Phase 5: TUI — Styles + Layout Engine + Static Panels

**Files to create:** `tui/styles.go`, `tui/app.go`, `tui/sessions.go`, `tui/repos.go`, `tui/tasks.go`, `tui/inbox.go`, `tui/signals.go`, `tui/keyhints.go`

**Behavior:**

1. **`tui/styles.go`** — Tokyo Night color palette as Lipgloss constants:
   ```go
   var (
       ColorBg      = lipgloss.Color("#1a1b26")
       ColorFg      = lipgloss.Color("#a9b1d6")
       ColorAccent  = lipgloss.Color("#7aa2f7")
       ColorSuccess = lipgloss.Color("#9ece6a")
       ColorWarning = lipgloss.Color("#e0af68")
       ColorError   = lipgloss.Color("#f7768e")
       ColorMuted   = lipgloss.Color("#565f89")
       ColorPurple  = lipgloss.Color("#bb9af7")
       ColorSelectedBg = lipgloss.Color("#292e42")
       ColorBorder     = lipgloss.Color("#3b4261")
   )
   ```
   - Define reusable styles: `PanelBorder` (muted border), `FocusedPanelBorder` (accent border), `MutedText`, `BoldText`, `StatusClean` (green ✓), `StatusDirty` (red ✗), `StatusUnpushed` (yellow ↑N).
   - Panel border function: `RenderPanel(title string, content string, width int, height int, focused bool) string` — renders bordered panel with title using Lipgloss `Border()` and `BorderTitle()`.

2. **`tui/app.go`** — Root Bubbletea model:
   ```go
   type PanelID int
   const (
       PanelSessions PanelID = iota
       PanelRepos
       PanelToday
       PanelInbox
       PanelSignals
   )
   type Mode int
   const (
       ModeNavigation Mode = iota
       ModeCapture
   )
   type Model struct {
       config       *config.Config
       width, height int
       focused      PanelID
       mode         Mode
       // Panel models
       sessions     SessionsModel
       repos        ReposModel
       tasks        TasksModel
       inbox        InboxModel
       signals      SignalsModel
       // Data
       err          error
   }
   ```
   - `Init() tea.Cmd` — return `tea.Batch` of all source fetch commands + tick commands.
   - `Update(msg tea.Msg) (tea.Model, tea.Cmd)` — handle:
     - `tea.WindowSizeMsg` → store width/height, recalculate layout.
     - `tea.KeyMsg` → route to navigation/capture mode handlers.
     - Source result messages → update panel data.
     - Tick messages → re-fetch sources.
   - `View() string` — calculate panel sizes per the sizing rules table, render each panel, compose with `lipgloss.JoinVertical` and `lipgloss.JoinHorizontal` (for bottom Inbox|Signals row).
   - **Layout calculation** function: takes `(width, height int, repoCount int)`, returns struct with each panel's allocated width/height. Follow the sizing rules table from the plan.
   - **Minimum terminal check**: if width < 60, render centered "Terminal too narrow" message instead of layout.

3. **Panel models** (`sessions.go`, `repos.go`, `tasks.go`, `inbox.go`, `signals.go`):
   Each panel is a sub-model with its own `Update` and `View` methods (not full `tea.Model` — they're called by the root model).

   - **`tui/sessions.go`** — `SessionsModel`:
     - Stores `[]sources.TmuxSession`, `cursor int`, `loading bool`.
     - `View(width, height int, focused bool) string` — render horizontal card flow. Each card: bordered box with name (bold, accent if current session), attached/detached text (green/muted), window count + idle time (muted). Cards flow horizontally with 1-char gap, wrap to next line.
     - Idle time: compute from `LastUsed` as relative duration ("4h", "2d"). Use human-friendly formatting.

   - **`tui/repos.go`** — `ReposModel`:
     - Stores `[]sources.GitRepoStatus`, `cursor int`, `loading bool`.
     - `View(width, height int, focused bool) string` — render table with columns: project (label), branch (purple), status (✓ green / ✗N red), ↑N (yellow, blank if 0), last commit (truncated with `…` at word boundary).
     - Header row in muted bold. Scrollable when repos exceed allocated height.
     - If a repo has `Error != nil`, show `⚠ path not found` in status column.

   - **`tui/tasks.go`** — `TasksModel`:
     - Stores `[]sources.Task`, `cursor int`, `loading bool`.
     - `View` — render `[x]` (green, muted text) or `[ ]` (default) per task. Selected item has `◂` cursor indicator in accent blue. Selected item background highlight (`ColorSelectedBg`).
     - Scrollable when tasks exceed allocated height.

   - **`tui/inbox.go`** — `InboxModel`:
     - Stores `[]sources.Task`, `cursor int`, `loading bool`, `textinput.Model` (from bubbles).
     - In navigation mode: show list of inbox items + `> _` prompt placeholder.
     - In capture mode: text input is focused and active. Shows typed text with blinking cursor.

   - **`tui/signals.go`** — `SignalsModel`:
     - Stores aggregated signals from all sources. Signal types:
       - Unpushed commits: from git source, if any repo has `Unpushed > 0`.
       - Failing CI: from GitHub source, if `FailingChecks > 0`.
       - Stale sessions: from tmux source, sessions with `LastUsed` older than `StaleSessionThreshold`.
       - Awaiting review PRs: from GitHub source, if `PRsAwaitingReview > 0`.
     - `View` — render signal lines with colored indicators. If no signals, show `✓ All clear` in green.

   - **`tui/keyhints.go`** — `KeyhintsView(mode Mode, width int) string`:
     - Navigation mode: `Tab panels  j/k nav  Enter jump  x toggle  c cap  r refresh  q quit`
     - Capture mode: `Enter save  Esc cancel`
     - Key names in accent blue, descriptions in muted. Separated by double space. Truncate from right if too wide.

4. **Wire up in `cmd/root.go`**: The root command loads config, then creates and runs the Bubbletea program with `tui.NewModel(cfg)`.

**Tests:** `tui/app_test.go`
- Test layout calculation function with various terminal sizes (40+ rows, 30-39, 24-29, <24).
- Test that minimum terminal width check returns the correct message.
- Test panel sizing: verify each panel gets correct height allocation.
- (Visual rendering tests are deferred — manual testing via running the TUI.)

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
# Manual: run `go run .` inside a tmux session and verify the layout renders
```

---

### Phase 6: TUI — Interactivity + Polling

**Files to modify:** `tui/app.go`, `tui/tasks.go`, `tui/inbox.go`, `tui/sessions.go`, `cmd/root.go`

**Behavior:**

1. **Keyboard navigation** in `tui/app.go`:
   - `Tab` → cycle `focused` through panels in order: Sessions → Repos → Today → Inbox → Signals → Sessions.
   - `j`/`k` → delegate to focused panel's cursor movement. Each panel tracks its own cursor and scrolls when cursor moves past visible area.
   - `q` → return `tea.Quit`.
   - `r` → return `tea.Batch` of all source fetch commands (force refresh).
   - `c` → set `mode = ModeCapture`, focus Inbox panel, focus the textinput.
   - `x` → if focused panel is Today, toggle task at cursor position. Call `sources.ToggleTask(filePath, task.Line)`. On success, update local state immediately (don't wait for poll). On error, show transient error in keyhint bar for 3 seconds (use `tea.Tick`).
   - `Enter` → if focused panel is Sessions, execute `tmux switch-client -t <sessionName>` and return `tea.Quit` (the TUI exits, user lands in target session; cockpit tmux session stays alive). If focused panel is Repos, check if a tmux session with matching label exists — if yes, switch to it; if no, create one with `tmux new-session -d -s <label> -c <repoPath>` then switch. On error, show transient error for 3 seconds.
   - `Esc` (in capture mode) → set `mode = ModeNavigation`, unfocus textinput.
   - `Enter` (in capture mode) → call `sources.AppendInbox(inboxPath, inputText)`, clear textinput, stay in capture mode.

2. **Polling** in `tui/app.go`:
   - Define tick messages: `localTickMsg struct{}`, `remoteTickMsg struct{}`.
   - `localTick() tea.Cmd` — returns `tea.Tick(time.Duration(config.General.RefreshInterval) * time.Second, func(time.Time) tea.Msg { return localTickMsg{} })`.
   - `remoteTick() tea.Cmd` — same pattern with `config.GitHub.RefreshInterval`.
   - On `localTickMsg` → `tea.Batch(fetchTmuxSessions(), fetchGitStatus(), fetchTasks(), fetchInbox(), localTick())`.
   - On `remoteTickMsg` → `tea.Batch(fetchGitHubStatus(), remoteTick())`.
   - Each fetch function returns a `tea.Cmd` that runs the source in a goroutine and returns the appropriate message type.

3. **Loading states**: Each panel starts with `loading: true`. When data arrives, set `loading: false`. `View()` renders spinner (`⠋ Loading...`) when `loading` is true.

4. **Error/stale handling**: Define `SourceErrorMsg{Source string, Err error}`. When received, the affected panel keeps its last data but shows stale indicator with timestamp.

5. **`cockpit cap` subcommand** in `cmd/root.go`:
   - `cockpit cap "thought text"` → load config, call `sources.AppendInbox(inboxPath, args[0])`, print confirmation, exit.
   - `cockpit cap` (no args) → interactive mode: loop with `bufio.NewReader(os.Stdin)`, prompt `> `, read lines, append each to inbox. Exit on empty line or Ctrl-C.

6. **tmux bootstrap** in `cmd/root.go` (root command `Run` function):
   - Check `tmux` is installed (`exec.LookPath("tmux")`). If not, print error and exit.
   - Load config. If missing, print "Run `cockpit init`" and exit.
   - Check `$TMUX` env var:
     - If set AND current session name (via `tmux display-message -p '#{session_name}'`) matches `config.General.SessionName` → run TUI directly.
     - If set but different session → `tmux switch-client -t <sessionName>` (if session exists) or create + switch.
     - If not set → `tmux new-session -s <sessionName> "cockpit"` then attach. The re-invoked `cockpit` inside the session will hit the first branch and run the TUI.

**Tests:** `tui/app_test.go` (additions)
- Test that `Tab` key cycles focus correctly.
- Test that `q` returns `tea.Quit`.
- Test tick message handling triggers source fetches.

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
# Manual: run the binary in tmux. Verify:
#   - Panels render with real data
#   - Tab cycles focus (border color changes)
#   - j/k moves cursor within panels
#   - x toggles a task (check the markdown file)
#   - c enters capture mode, Enter saves, Esc exits
#   - Enter on a session switches tmux sessions
#   - Data refreshes automatically (modify a git repo, see it update)
```

---

### Phase 7: Responsive Layout + Polish

**Files to modify:** `tui/app.go`, `tui/styles.go`, `tui/repos.go`, `tui/sessions.go`, `tui/inbox.go`, `tui/signals.go`

**Behavior:**

1. **Responsive width tiers** in layout calculation:
   - **Wide (100+ cols):** Full layout as designed.
   - **Standard (80-99 cols):** Last commit column in repos truncates aggressively. Session card idle time hidden.
   - **Narrow (60-79 cols):** Bottom row stacks vertically (Inbox above Signals) instead of side-by-side. Repos table hides last commit column. Session cards render as single line each (no box border, just `name [attached] 3w`).
   - **< 60 cols:** Centered message: "Terminal too narrow (need 60+ cols). Resize or press q to quit."

2. **Responsive height tiers** in layout calculation — implement the panel sizing rules table:
   | Terminal height | Sessions | Repos | Today | Bottom row | Keyhints |
   |---|---|---|---|---|---|
   | 40+ | 3 | min(repos+1, 6) | flex | 6 | 1 |
   | 30-39 | 2 | min(repos+1, 4) | flex | 4 | 1 |
   | 24-29 | 2 | min(repos+1, 3) | flex | 3 | 1 |
   | < 24 | 1 | 1 (summary) | flex | 3 | 1 |

3. **Truncation** in repos table: Truncate at last word boundary before column width limit, append `…`. Status indicators and unpushed count columns never truncate.

4. **Default focus on launch:** Today panel, cursor on first unchecked task. If Today is empty, focus Sessions panel.

5. **Transient error messages:** Show in keyhint bar area for 3 seconds, then auto-clear via `tea.Tick`. Style: `⚠` in warning yellow + error message in muted text.

6. **Scrolling:** For any panel with more items than visible rows, render a visual scroll indicator (e.g., muted `▼` at bottom or `▲` at top when there's hidden content).

**Tests:** `tui/app_test.go` (additions)
- Test layout at each width tier boundary (59, 60, 79, 80, 99, 100).
- Test layout at each height tier boundary (23, 24, 29, 30, 39, 40).
- Test truncation function: truncates at word boundary, appends `…`.

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
# Manual: resize terminal to various sizes and verify layout adapts correctly
```

---

### Phase 8: `cockpit init` + Config Validation + Empty States

**Files to modify:** `cmd/root.go`, `config/config.go`, `config_template.go`, `tui/sessions.go`, `tui/repos.go`, `tui/tasks.go`, `tui/inbox.go`, `tui/signals.go`

**Behavior:**

1. **`cockpit init`** command:
   - Create `~/.config/cockpit/` directory if it doesn't exist (`os.MkdirAll`).
   - Write the embedded TOML template to `~/.config/cockpit/config.toml`.
   - Print: "Config created at ~/.config/cockpit/config.toml — edit it to add your repos and vault path."
   - If file already exists, print: "Config already exists at <path>. Remove it first to regenerate." and exit 0.

2. **Empty states** for each panel (render when data slice is empty and not loading):
   - Sessions: "No tmux sessions running. Start one: tmux new -s <name>" — muted text, command in accent.
   - Repos: "No repos configured. Add repos in ~/.config/cockpit/config.toml" — with config path in accent.
   - Today: "No tasks for today. Add some in your vault: <today_file>" — with file path in accent.
   - Inbox: "Inbox empty — press c to capture a thought" — with `c` in accent.
   - Signals: "✓ All clear — nothing needs attention" — green checkmark, calm tone.

3. **Error states** for each panel (render when source returned error):
   - Show `⚠` in warning yellow + specific error message (path, source name).
   - Keep last-known data visible below the error if available.

**Tests:** `config/config_test.go` (additions)
- Test init creates config file in temp dir.
- Test init doesn't overwrite existing file.

**Gate check:**
```bash
go build ./...
go test ./...
go vet ./...
# Manual: delete config, run `cockpit init`, verify template is created and valid TOML
# Manual: run `cockpit` with empty/missing config sections, verify empty states render correctly
```

---

### Phase 9: Final Integration + Build

**Files to modify:** `main.go`, `cmd/root.go`

**Behavior:**

1. Verify all source → panel data flows work end-to-end.
2. Add `--version` flag to root command. Set version via `ldflags` at build time: `-ldflags "-X main.version=0.1.0"`.
3. Add `--config` flag to root command to override config path.
4. Ensure `cockpit cap` works from outside the cockpit tmux session (independent config load).
5. Build: `go build -o cockpit .`

**Gate check:**
```bash
go build -o cockpit -ldflags "-X main.version=0.1.0" .
./cockpit --version       # should print "cockpit v0.1.0"
./cockpit init            # should create config (or say it exists)
go test ./...             # all tests pass
go vet ./...              # no issues
```

## Testing

- **Framework:** Go's built-in `testing` package. No external test framework.
- **Test file locations:** Same directory as source, `*_test.go` suffix.
- **Patterns:**
  - Table-driven tests for parsing functions (tmux output, git output, GitHub JSON, TOML config).
  - `t.TempDir()` for all file write tests (never write to real paths).
  - Extract parsing logic into pure functions that accept strings/bytes (not `exec.Command`) so they're testable without real CLI tools.
  - Integration tests that call real `git` commands use `t.TempDir()` with `git init`.
  - TUI tests use `tea.NewProgram` test helpers or test the model's `Update` method directly with synthetic messages.
- **Minimum coverage targets:**
  - `config/` — all Load paths (valid, missing, malformed, defaults).
  - `sources/` — all parse functions, edge cases (empty output, malformed output).
  - `tui/` — layout calculation, focus cycling, key routing.

## Workflow

1. **Phase 1:** Scaffold project, implement config loader, CLI skeleton. Write config tests. Run gate check. Commit: "scaffold project with config loader and CLI commands".

2. **Phase 2:** Implement tmux and git sources with parse helpers. Write source tests. Run gate check. Commit: "add tmux and git data sources".

3. **Phase 3:** Implement Obsidian source with read/write/toggle. Write tests. Run gate check. Commit: "add Obsidian markdown source with task toggle".

4. **Phase 4:** Implement GitHub source with gh CLI integration. Write tests. Run gate check. Commit: "add GitHub PR and CI status source".

5. **Phase 5:** Build TUI: styles, layout engine, all panel views (static rendering). Wire up to real sources. Write layout tests. Run gate check. Commit: "implement TUI layout with all panels".

6. **Phase 6:** Add keyboard interactivity, polling, capture mode, tmux bootstrap, `cap` subcommand. Write interaction tests. Run gate check. Commit: "add keyboard navigation, polling, and capture mode".

7. **Phase 7:** Implement responsive layout tiers, truncation, scroll indicators, polish. Write responsive tests. Run gate check. Commit: "add responsive layout and visual polish".

8. **Phase 8:** Implement `cockpit init`, empty states, error states. Write tests. Run gate check. Commit: "add init command, empty states, and error handling".

9. **Phase 9:** Final integration, version flag, build verification. Run full gate check. Commit: "finalize build with version flag".

10. **Post-implementation:** Run `/review` to review the full diff before shipping. Run `/qa` if a web component is added later. Use `/investigate` if any integration issues arise during manual testing.

**Rules for every phase:**
- Run `go vet ./...` before every commit.
- Run `go test ./...` before every commit — all must pass.
- Run `go build ./...` before every commit — must compile.
- Do not proceed to the next phase until the current phase's gate check passes.
- If a gate check fails, diagnose and fix before moving on.
- Commit after each phase with a descriptive message.