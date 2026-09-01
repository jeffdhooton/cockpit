# Project Processes & Cockpit Daemon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a repo declare background processes that launch as tmux windows on jump, and ship a standalone `cockpit daemon` exposing a tool server with parity to Helm's.

**Architecture:** tmux is the process manager — processes are windows in the repo's session, launched with `remain-on-exit` so crashes stay readable. All tmux access goes through a `Runner` interface so argv construction is pure and testable. The daemon is stateless, deriving every answer live from tmux/git/markdown, and speaks JSON-RPC 2.0 over loopback HTTP using only the Go standard library.

**Tech Stack:** Go 1.24, BurntSushi/toml, Bubbletea/Lipgloss (existing), `net/http` + `encoding/json` (stdlib only — no new dependencies), tmux 3.0+.

**Spec:** `docs/superpowers/specs/2026-09-01-processes-and-daemon-design.md`

## Global Constraints

- **No new Go dependencies.** The daemon uses stdlib `net/http` and `encoding/json` only.
- **tmux 3.0+** required (`new-window -e` for env vars).
- **Module path:** `github.com/jhoot/cockpit`.
- **Daemon port default:** `45679`. Helm's was `45678` — do not reuse it.
- **Tool names:** `cockpit_<verb>`, mirroring Helm's `helm_<verb>` argument shapes exactly.
- **Session id format:** `cockpit-<hex nanos>`.
- **Process/window name rule:** `^[a-zA-Z0-9_-]+$` — the existing `validLabel` pattern.
- **Commit messages:** subject ≤ 50 chars, body sentences ≤ 20 words, spell out unfamiliar acronyms. A commit hook enforces this.
- **Verification per task:** `go build ./... && go vet ./... && go test ./...`

---

### Task 1: Process and daemon configuration

**Files:**
- Modify: `config/config.go`
- Test: `config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  type ProcessConfig struct {
      Name       string            `toml:"name"`
      Command    string            `toml:"command"`
      AutoStart  *bool             `toml:"auto_start"`
      WorkingDir string            `toml:"working_dir"`
      Env        map[string]string `toml:"env"`
      Status     *StatusPatterns   `toml:"status"`
  }
  type StatusPatterns struct {
      Ready, Compiling, Error, Restarting string
  }
  type DaemonConfig struct {
      Enabled bool `toml:"enabled"`
      Port    int  `toml:"port"`
  }
  func (p ProcessConfig) ShouldAutoStart() bool
  func (p ProcessConfig) ResolvedWorkingDir(repoPath string) string
  func (c *Config) Repo(label string) (RepoConfig, bool)
  func (r RepoConfig) Process(name string) (ProcessConfig, bool)
  ```
  `RepoConfig` gains `Processes []ProcessConfig`; `Config` gains `Daemon DaemonConfig`.

- [ ] **Step 1: Write failing tests**

In `config/config_test.go`:

```go
func TestLoadProcesses(t *testing.T) {
	cfg := writeAndLoad(t, `
[obsidian]
vault_path = "/tmp/vault"

[[repos]]
path = "/tmp/my-app"
label = "my-app"

  [[repos.processes]]
  name = "dev"
  command = "npm run dev"

  [[repos.processes]]
  name = "test"
  command = "npm test"
  auto_start = false
  working_dir = "packages/web"
  env = { PORT = "3000" }

    [repos.processes.status]
    ready = 'Local:\s+(\S+)'
`)
	procs := cfg.Repos[0].Processes
	if len(procs) != 2 {
		t.Fatalf("want 2 processes, got %d", len(procs))
	}
	if !procs[0].ShouldAutoStart() {
		t.Error("omitted auto_start should default to true")
	}
	if procs[1].ShouldAutoStart() {
		t.Error("auto_start = false should be false")
	}
	if got := procs[1].ResolvedWorkingDir("/tmp/my-app"); got != "/tmp/my-app/packages/web" {
		t.Errorf("working dir = %q", got)
	}
	if procs[1].Env["PORT"] != "3000" {
		t.Errorf("env not parsed: %v", procs[1].Env)
	}
	if procs[1].Status == nil || procs[1].Status.Ready != `Local:\s+(\S+)` {
		t.Errorf("status not parsed: %+v", procs[1].Status)
	}
}
```

Plus table-driven rejection tests (`TestValidateProcesses`) for: empty name, duplicate names in one repo, name with a space, empty command, and an unparseable status regex — each asserting `Load` returns an error mentioning the process name. Plus `TestDaemonDefaults` asserting `Enabled == true` and `Port == 45679` when `[daemon]` is absent, and `TestResolvedWorkingDirAbsolute` asserting an absolute `working_dir` is returned unchanged and an empty one returns the repo path.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'Process|Daemon|WorkingDir' -v`
Expected: FAIL — `cfg.Repos[0].Processes` undefined.

- [ ] **Step 3: Implement**

Add the types above to `config/config.go`. Then:

```go
func (p ProcessConfig) ShouldAutoStart() bool {
	return p.AutoStart == nil || *p.AutoStart
}

func (p ProcessConfig) ResolvedWorkingDir(repoPath string) string {
	if p.WorkingDir == "" {
		return repoPath
	}
	dir := ExpandTilde(p.WorkingDir)
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(repoPath, dir)
}
```

In `applyDefaults`, default `Daemon.Port` to `45679`. Because Go zero-values
bools, `[daemon] enabled` needs the same treatment the signals booleans get —
use `*bool` on `DaemonConfig.Enabled` internally or track section presence. Use
`Enabled *bool` with an `IsEnabled()` helper defaulting to true, mirroring
`ShouldAutoStart`.

In `validate`, loop repos and processes:

```go
for _, repo := range cfg.Repos {
	seen := map[string]bool{}
	for _, p := range repo.Processes {
		if p.Name == "" {
			return fmt.Errorf("config: repo %q has a process with no name", repo.Label)
		}
		if !validProcessName.MatchString(p.Name) {
			return fmt.Errorf("config: repo %q process %q: name must be alphanumeric, hyphens, or underscores", repo.Label, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("config: repo %q has duplicate process %q", repo.Label, p.Name)
		}
		seen[p.Name] = true
		if strings.TrimSpace(p.Command) == "" {
			return fmt.Errorf("config: repo %q process %q: command is required", repo.Label, p.Name)
		}
		if p.Status != nil {
			for label, expr := range p.Status.All() {
				if expr == "" {
					continue
				}
				if _, err := regexp.Compile(expr); err != nil {
					return fmt.Errorf("config: repo %q process %q: status.%s is not a valid regexp: %w", repo.Label, p.Name, label, err)
				}
			}
		}
	}
}
```

`StatusPatterns.All()` returns `map[string]string{"ready": s.Ready, "compiling": s.Compiling, "error": s.Error, "restarting": s.Restarting}`.

In `expandPaths`, expand each process `WorkingDir` tilde. Add lookup helpers
`Config.Repo(label)` and `RepoConfig.Process(name)` returning `(value, bool)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./config/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add config/
git commit -m "Add process and daemon config"
```

---

### Task 2: tmux Runner seam and argv builders

**Files:**
- Modify: `sources/tmux.go`
- Create: `sources/tmux_args.go`
- Test: `sources/tmux_args_test.go`

**Interfaces:**
- Consumes: `config.ProcessConfig`.
- Produces:
  ```go
  type Runner interface {
      Run(ctx context.Context, args ...string) (string, error)
  }
  type ExecRunner struct{ Timeout time.Duration }
  func DefaultRunner() Runner

  func NewSessionArgs(session, dir string) []string
  func NewWindowArgs(session string, p config.ProcessConfig, repoPath string) []string
  func RespawnWindowArgs(session, window string, p config.ProcessConfig, repoPath string) []string
  func KillWindowArgs(session, window string) []string
  func RemainOnExitArgs(session, window string) []string
  func SelectWindowArgs(session string, index int) []string
  func ListWindowsArgs(session string) []string
  func CapturePaneArgs(target string, lines int) []string
  func SendKeysLiteralArgs(target, text string) []string
  func SendKeysEnterArgs(target string) []string
  func Target(session, window string) string

  type Window struct {
      Index  int
      Name   string
      Dead   bool
      PanePID int
      Active bool
  }
  func ParseWindows(out string) []Window
  ```

- [ ] **Step 1: Write failing tests**

In `sources/tmux_args_test.go`:

```go
func TestNewWindowArgs(t *testing.T) {
	p := config.ProcessConfig{
		Name:       "dev",
		Command:    "npm run dev",
		WorkingDir: "web",
		Env:        map[string]string{"PORT": "3000"},
	}
	got := NewWindowArgs("my-app", p, "/tmp/my-app")
	want := []string{
		"new-window", "-d", "-t", "my-app:", "-n", "dev",
		"-c", "/tmp/my-app/web", "-e", "PORT=3000", "npm run dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestNewWindowArgsEnvSorted(t *testing.T) {
	p := config.ProcessConfig{Name: "dev", Command: "x", Env: map[string]string{"B": "2", "A": "1"}}
	got := NewWindowArgs("s", p, "/r")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-e A=1 -e B=2") {
		t.Errorf("env vars must be sorted for determinism: %q", joined)
	}
}

func TestParseWindows(t *testing.T) {
	out := "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n"
	got := ParseWindows(out)
	if len(got) != 2 {
		t.Fatalf("want 2 windows, got %d", len(got))
	}
	if got[1].Name != "dev" || !got[1].Dead || got[1].PanePID != 222 || got[1].Active {
		t.Errorf("bad parse: %+v", got[1])
	}
}
```

Plus cases for: `CapturePaneArgs("s:dev", 200)` → `["capture-pane","-p","-t","s:dev","-S","-200"]`; `SendKeysLiteralArgs` → `["send-keys","-t","s:dev","-l","text"]`; `KillWindowArgs`; `RespawnWindowArgs` including `-k`; `RemainOnExitArgs` → `["set-window-option","-t","s:dev","remain-on-exit","on"]`; `NewWindowArgs` with no env and no working_dir falling back to the repo path; `ParseWindows` on empty input returning nil and skipping malformed lines.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./sources/ -run 'Args|ParseWindows' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement**

`sources/tmux_args.go` holds pure argv builders. Env keys are sorted with
`sort.Strings` so output is deterministic. `Target(session, window)` returns
`session + ":" + window`.

```go
func NewWindowArgs(session string, p config.ProcessConfig, repoPath string) []string {
	args := []string{"new-window", "-d", "-t", session + ":", "-n", p.Name,
		"-c", p.ResolvedWorkingDir(repoPath)}
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+p.Env[k])
	}
	return append(args, p.Command)
}
```

`ListWindowsArgs` uses the format string
`#{window_index}\t#{window_name}\t#{pane_dead}\t#{pane_pid}\t#{window_active}`.
`ParseWindows` splits on tabs, skips lines with fewer than 5 fields, and
tolerates unparseable integers by leaving them zero.

In `sources/tmux.go`, add:

```go
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type ExecRunner struct{ Timeout time.Duration }

func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("tmux %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return string(out), nil
}

func DefaultRunner() Runner { return ExecRunner{} }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./sources/ -v`
Expected: PASS (existing tmux tests still pass — nothing was removed).

- [ ] **Step 5: Commit**

```bash
git add sources/
git commit -m "Add tmux runner seam and argv builders"
```

---

### Task 3: Process reconciliation

**Files:**
- Create: `sources/processes.go`
- Test: `sources/processes_test.go`

**Interfaces:**
- Consumes: `Runner`, argv builders, `Window`, `config.RepoConfig`.
- Produces:
  ```go
  type ProcessState string
  const (
      ProcessRunning    ProcessState = "running"
      ProcessDead       ProcessState = "dead"
      ProcessNotStarted ProcessState = "not_started"
  )
  type ProcessInfo struct {
      Name        string
      Command     string
      State       ProcessState
      WindowIndex int
      PanePID     int
      AutoStart   bool
      Configured  bool
  }
  func ListWindows(ctx context.Context, r Runner, session string) ([]Window, error)
  func SessionExists(ctx context.Context, r Runner, session string) bool
  func InspectProcesses(ctx context.Context, r Runner, repo config.RepoConfig) ([]ProcessInfo, error)
  func StartProcess(ctx context.Context, r Runner, repo config.RepoConfig, p config.ProcessConfig) error
  func StopProcess(ctx context.Context, r Runner, session, name string) error
  func RestartProcess(ctx context.Context, r Runner, repo config.RepoConfig, p config.ProcessConfig) error
  func EnsureSession(ctx context.Context, r Runner, repo config.RepoConfig) (created bool, err error)
  func ReconcileProcesses(ctx context.Context, r Runner, repo config.RepoConfig) []error
  ```
- A `fakeRunner` recording calls and returning scripted output lives in
  `sources/processes_test.go` and is reused by later tasks' tests.

- [ ] **Step 1: Write failing tests**

In `sources/processes_test.go`, first the fake:

```go
type fakeRunner struct {
	calls   [][]string
	outputs map[string]string // keyed by args[0]
	errs    map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if err, ok := f.errs[args[0]]; ok {
		return "", err
	}
	return f.outputs[args[0]], nil
}

func (f *fakeRunner) called(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}
```

Then the behaviors:

```go
func TestReconcileStartsMissingAutoStartProcesses(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t0\t222\t0\n",
	}}
	repo := config.RepoConfig{Label: "app", Path: "/r", Processes: []config.ProcessConfig{
		{Name: "dev", Command: "npm run dev"},
		{Name: "test", Command: "npm test"},
	}}
	if errs := ReconcileProcesses(context.Background(), f, repo); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	created := f.called("new-window")
	if len(created) != 1 {
		t.Fatalf("want 1 new-window, got %d: %v", len(created), created)
	}
	if !slices.Contains(created[0], "test") {
		t.Errorf("wrong window created: %v", created[0])
	}
}

func TestReconcileRespawnsDeadWindow(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n",
	}}
	repo := config.RepoConfig{Label: "app", Path: "/r", Processes: []config.ProcessConfig{
		{Name: "dev", Command: "npm run dev"},
	}}
	ReconcileProcesses(context.Background(), f, repo)
	if len(f.called("respawn-window")) != 1 {
		t.Errorf("dead window should be respawned, calls: %v", f.calls)
	}
	if len(f.called("new-window")) != 0 {
		t.Errorf("dead window must not be duplicated, calls: %v", f.calls)
	}
}
```

Plus: `TestReconcileSkipsNonAutoStart` (a process with `auto_start = false` and
no window produces no `new-window`); `TestReconcileIdempotent` (all windows
present and alive → zero mutating calls); `TestStartProcessSetsRemainOnExit`
(`StartProcess` issues `new-window` then `set-window-option`);
`TestInspectProcessesClassifies` (configured+alive → running, configured+dead →
dead, configured+absent → not_started, unconfigured window → `Configured:false`,
and window 0 `shell` is reported but not treated as a configured process);
`TestReconcileCollectsErrors` (a `new-window` error is returned, and later
processes are still attempted).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./sources/ -run 'Reconcile|Inspect|StartProcess' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`ReconcileProcesses` lists windows once, builds a `map[string]Window`, then for
each configured auto-start process: running → skip; dead → `RestartProcess`;
absent → `StartProcess`. Errors are accumulated, never fatal — one bad process
must not prevent the others or the jump.

`StartProcess` runs `NewWindowArgs` then `RemainOnExitArgs` (a failure to set
remain-on-exit is ignored — it is a nicety, not a requirement).
`RestartProcess` runs `RespawnWindowArgs` then `RemainOnExitArgs`.

`SessionExists` runs `has-session -t <name>` and reports `err == nil`.
`EnsureSession` returns early with `created=false` when the session exists,
otherwise runs `NewSessionArgs` and returns `created=true`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./sources/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add sources/
git commit -m "Add tmux process reconciliation"
```

---

### Task 4: Launch processes on jump

**Files:**
- Modify: `tui/app.go` (`tmuxJump` around line 1250, and its call sites)
- Modify: `tui/grid.go` (tile process indicator)
- Test: `tui/app_test.go`

**Interfaces:**
- Consumes: `sources.EnsureSession`, `sources.ReconcileProcesses`, `sources.InspectProcesses`.
- Produces: `func tmuxJumpRepo(repo config.RepoConfig) error`, and
  `func processIndicator(infos []sources.ProcessInfo) string`.

- [ ] **Step 1: Write failing tests**

```go
func TestProcessIndicator(t *testing.T) {
	cases := []struct {
		name  string
		infos []sources.ProcessInfo
		want  string
	}{
		{"none", nil, ""},
		{"all running", []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessRunning, Configured: true},
		}, "⚙ 1/1"},
		{"one dead", []sources.ProcessInfo{
			{Name: "dev", State: sources.ProcessRunning, Configured: true},
			{Name: "test", State: sources.ProcessDead, Configured: true},
		}, "⚙ 1/2"},
		{"unconfigured ignored", []sources.ProcessInfo{
			{Name: "shell", State: sources.ProcessRunning, Configured: false},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processIndicator(tc.infos); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestProcessIndicatorDegraded(t *testing.T) {
	infos := []sources.ProcessInfo{{Name: "dev", State: sources.ProcessDead, Configured: true}}
	if !processIndicatorDegraded(infos) {
		t.Error("a dead process must mark the tile degraded")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tui/ -run ProcessIndicator -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`processIndicator` counts only `Configured` infos, returning `""` when there are
none and `fmt.Sprintf("⚙ %d/%d", running, total)` otherwise.
`processIndicatorDegraded` reports whether any configured info is
`ProcessDead`.

Replace `tmuxJump(label, path string)` with `tmuxJumpRepo(repo config.RepoConfig)`:

```go
func tmuxJumpRepo(repo config.RepoConfig) error {
	if !validLabel.MatchString(repo.Label) {
		return fmt.Errorf("invalid session label %q: must be alphanumeric, hyphens, or underscores", repo.Label)
	}
	ctx := context.Background()
	r := sources.DefaultRunner()

	created, err := sources.EnsureSession(ctx, r, repo)
	if err != nil {
		return err
	}
	// Process failures must never block the jump.
	_ = sources.ReconcileProcesses(ctx, r, repo)
	if created {
		_, _ = r.Run(ctx, sources.SelectWindowArgs(repo.Label, 0)...)
	}
	return exec.Command("tmux", "switch-client", "-t", repo.Label).Run()
}
```

Update call sites in `tui/app.go` (repos panel Enter, grid Enter, and the
new-session path) to build a `config.RepoConfig` — they already have the repo in
hand from `m.cfg.Repos`. Where a call site only has a label, look the repo up
with `m.cfg.Repo(label)` and fall back to a bare `config.RepoConfig{Label: label, Path: path}`
when it is not configured. Keep `tmuxSwitch` unchanged for plain session jumps.

Render the indicator in the grid tile beneath the git line, using `MutedText`
normally and the error style when degraded. The tile already truncates to width;
skip the indicator when the tile is too narrow to fit it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./tui/ -v && go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add tui/
git commit -m "Launch repo processes on tmux jump"
```

---

### Task 5: Signal computation

**Files:**
- Create: `sources/signals.go`
- Test: `sources/signals_test.go`

**Interfaces:**
- Consumes: `TmuxSession`, `GitRepoStatus`, `GitHubStatus`, `ProcessInfo`, `config.SignalsConfig`.
- Produces:
  ```go
  type SignalKind string
  const (
      SignalStaleSession SignalKind = "stale_session"
      SignalUnpushed     SignalKind = "unpushed"
      SignalFailingCI    SignalKind = "failing_ci"
      SignalDeadProcess  SignalKind = "dead_process"
  )
  type Signal struct {
      Kind    SignalKind
      Subject string // repo label or session name
      Detail  string
  }
  type SignalInput struct {
      Config    config.SignalsConfig
      Sessions  []TmuxSession
      Git       []GitRepoStatus
      GitHub    *GitHubStatus
      Processes map[string][]ProcessInfo // repo label -> processes
      Now       time.Time
  }
  func ComputeSignals(in SignalInput) []Signal
  ```

- [ ] **Step 1: Write failing tests**

```go
func TestComputeSignalsStaleSession(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	in := SignalInput{
		Config:   config.SignalsConfig{StaleSessionThreshold: "24h", ShowStaleSessions: true},
		Sessions: []TmuxSession{
			{Name: "old", LastUsed: now.Add(-48 * time.Hour)},
			{Name: "fresh", LastUsed: now.Add(-1 * time.Hour)},
			{Name: "attached-old", LastUsed: now.Add(-48 * time.Hour), Attached: true},
		},
		Now: now,
	}
	got := ComputeSignals(in)
	if len(got) != 1 || got[0].Subject != "old" {
		t.Fatalf("want only the stale detached session, got %+v", got)
	}
}

func TestComputeSignalsDeadProcess(t *testing.T) {
	in := SignalInput{
		Processes: map[string][]ProcessInfo{
			"app": {
				{Name: "dev", State: ProcessDead, Configured: true},
				{Name: "test", State: ProcessRunning, Configured: true},
			},
		},
		Now: time.Now(),
	}
	got := ComputeSignals(in)
	if len(got) != 1 || got[0].Kind != SignalDeadProcess || got[0].Subject != "app/dev" {
		t.Fatalf("want one dead-process signal for app/dev, got %+v", got)
	}
}
```

Plus: unpushed suppressed when `ShowUnpushed` is false; failing CI emitted only
for `CIStatus == "failing"` and only when `ShowFailingCI`; an unparseable
threshold falls back to 24h rather than erroring; results are ordered
deterministically (dead processes first, then failing CI, unpushed, stale).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./sources/ -run ComputeSignals -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`ComputeSignals` builds the slice in the fixed order above. Attached sessions
are never stale. A repo's git error is skipped rather than reported as unpushed.
Dead-process subjects are `"<repo>/<process>"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./sources/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add sources/
git commit -m "Add signal computation"
```

---

### Task 6: JSON-RPC transport

**Files:**
- Create: `daemon/mcp.go`
- Test: `daemon/mcp_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  ```go
  type ToolHandler interface {
      Definitions() []ToolDefinition
      Call(ctx context.Context, name string, args map[string]any) (any, error)
  }
  type ToolDefinition struct {
      Name        string         `json:"name"`
      Description string         `json:"description"`
      InputSchema map[string]any `json:"inputSchema"`
  }
  type Server struct {
      Tools     ToolHandler
      Version   string
      SessionID string
  }
  func NewServer(tools ToolHandler, version string) *Server
  func (s *Server) Handler() http.Handler
  ```

- [ ] **Step 1: Write failing tests**

```go
type stubTools struct{ lastName string; lastArgs map[string]any; err error }

func (s *stubTools) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "cockpit_whoami", Description: "d",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}}
}
func (s *stubTools) Call(_ context.Context, name string, args map[string]any) (any, error) {
	s.lastName, s.lastArgs = name, args
	if s.err != nil {
		return nil, s.err
	}
	return map[string]any{"ok": true}, nil
}

func post(t *testing.T, h http.Handler, body string) (*http.Response, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	var decoded map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decoded)
	return res, decoded
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	h := NewServer(&stubTools{}, "1.2.3").Handler()
	_, got := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	result := got["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("version = %v", result["protocolVersion"])
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "cockpit" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", info)
	}
}

func TestInitializeFallsBackForUnknownVersion(t *testing.T) {
	h := NewServer(&stubTools{}, "1.2.3").Handler()
	_, got := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	result := got["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("version = %v", result["protocolVersion"])
	}
}

func TestToolErrorIsSuccessEnvelopeWithIsError(t *testing.T) {
	h := NewServer(&stubTools{err: errors.New("boom")}, "1").Handler()
	_, got := post(t, h, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{}}}`)
	if _, isErr := got["error"]; isErr {
		t.Fatal("tool failures must not be JSON-RPC errors")
	}
	result := got["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("want isError true, got %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "boom") {
		t.Errorf("error text = %q", text)
	}
}
```

Plus: wrong `jsonrpc` version → error `-32600`; unknown method with an id →
`-32601`; a notification (no `id`) → `202` with an empty body; `tools/list`
returns the stub's definition; `tools/call` forwards name and arguments to the
handler; the `mcp-session-id` header is present on responses; `GET` → `405`;
`DELETE` → `202`; both `/` and `/mcp` are routed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./daemon/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`daemon/mcp.go` defines `jsonRPCRequest` (with `ID json.RawMessage` so a null vs
absent id is distinguishable — use `*json.RawMessage`) and `jsonRPCResponse`
with `omitempty` on `result` and `error`. `Handler()` returns a `*http.ServeMux`
registering `/` and `/mcp` on the same `http.HandlerFunc`, which switches on
method: `GET` → 405, `DELETE` → 202 + session header, `POST` → decode and
dispatch, anything else → 405.

Tool results are wrapped as
`{"content":[{"type":"text","text":<pretty JSON>}]}`, and tool errors as the
same shape with `"isError": true` and the text `"Error: " + err.Error()`.

`NewServer` generates `SessionID` as `fmt.Sprintf("cockpit-%x", time.Now().UnixNano())`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./daemon/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/
git commit -m "Add JSON-RPC transport for the daemon"
```

---

### Task 7: Read-only tools

**Files:**
- Create: `daemon/tools.go`
- Test: `daemon/tools_test.go`

**Interfaces:**
- Consumes: `ToolHandler`, `sources.Runner`, `config.Config`.
- Produces:
  ```go
  type Tools struct {
      Cfg        *config.Config
      ConfigPath string
      Runner     sources.Runner
      Version    string
      Port       int
      Now        func() time.Time
  }
  func NewTools(cfg *config.Config, configPath string, r sources.Runner, version string, port int) *Tools
  ```
  Implements `Definitions()` and `Call()`. This task delivers
  `cockpit_list_projects`, `cockpit_list_processes`, `cockpit_read_output`,
  `cockpit_git_status`, `cockpit_signals`, `cockpit_whoami`, `cockpit_status`.

- [ ] **Step 1: Write failing tests**

```go
func testTools(t *testing.T, f sources.Runner, repos ...config.RepoConfig) *Tools {
	t.Helper()
	cfg := &config.Config{Repos: repos}
	cfg.General.SessionName = "cockpit"
	return NewTools(cfg, "/tmp/config.toml", f, "1.0.0", 45679)
}

func TestListProcessesReportsState(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n",
		"has-session":  "",
	}}
	tools := testTools(t, f, config.RepoConfig{Label: "app", Path: "/r",
		Processes: []config.ProcessConfig{{Name: "dev", Command: "npm run dev"}, {Name: "test", Command: "npm test"}}})

	got, err := tools.Call(context.Background(), "cockpit_list_processes",
		map[string]any{"project": "app"})
	if err != nil {
		t.Fatal(err)
	}
	byName := indexProcesses(t, got)
	if byName["dev"].State != sources.ProcessDead {
		t.Errorf("dev should be dead, got %v", byName["dev"].State)
	}
	if byName["test"].State != sources.ProcessNotStarted {
		t.Errorf("test should be not_started, got %v", byName["test"].State)
	}
}

func TestReadOutputRequestsRequestedLines(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"capture-pane": "line one\nline two\n"}}
	tools := testTools(t, f, config.RepoConfig{Label: "app", Path: "/r",
		Processes: []config.ProcessConfig{{Name: "dev", Command: "x"}}})
	if _, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "dev", "lines": float64(50)}); err != nil {
		t.Fatal(err)
	}
	call := f.called("capture-pane")[0]
	if !slices.Contains(call, "-50") {
		t.Errorf("want -S -50, got %v", call)
	}
}

func TestUnknownToolIsAnError(t *testing.T) {
	tools := testTools(t, &fakeRunner{})
	if _, err := tools.Call(context.Background(), "cockpit_nope", nil); err == nil {
		t.Fatal("unknown tool must error")
	}
}

func TestUnknownProjectIsAnError(t *testing.T) {
	tools := testTools(t, &fakeRunner{})
	if _, err := tools.Call(context.Background(), "cockpit_list_processes",
		map[string]any{"project": "ghost"}); err == nil {
		t.Fatal("unknown project must error")
	}
}
```

Plus: `Definitions()` returns 14 tools, every name is unique and starts with
`cockpit_`, and each has an object `inputSchema`; `cockpit_whoami` reports the
port, version and config path; `cockpit_status` returns matched events for a
process with `status` patterns and an empty list for one without;
`cockpit_read_output` defaults to 100 lines when `lines` is omitted and clamps
absurd values; `cockpit_status` respects `limit` (default 16, max 64).

`indexProcesses` is a test helper that marshals the tool result to JSON and back
into `[]sources.ProcessInfo`, keyed by name. Copy `fakeRunner` from
`sources/processes_test.go` into `daemon/tools_test.go` — it cannot be imported
across packages.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./daemon/ -run 'ListProcesses|ReadOutput|Unknown' -v`
Expected: FAIL — `NewTools` undefined.

- [ ] **Step 3: Implement**

`Call` switches on the tool name and returns `fmt.Errorf("unknown tool: %s", name)`
by default. Argument access goes through small helpers — `argString(args, "project")`,
`argInt(args, "lines", 100)`, `argBool(args, "submit", true)` — because
`encoding/json` decodes numbers as `float64`.

A shared `resolveProcess(args) (config.RepoConfig, config.ProcessConfig, error)`
looks up the project then the process, producing
`fmt.Errorf("project %q has no process %q", ...)` when missing. Tools that
accept an arbitrary window (`read_output` on a spawned agent) fall back to
treating `process` as a bare window name when it is not configured.

`cockpit_status` captures the pane, then for each configured pattern scans lines
newest-first, emitting `{"type": "ready", "line": "...", "match": "...",
"source": "scrollback"}` up to `limit`.

`cockpit_signals` gathers sessions, git, and per-repo process info, then calls
`sources.ComputeSignals`. GitHub data is included only when `cfg.GitHub.Enabled`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./daemon/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/
git commit -m "Add read-only daemon tools"
```

---

### Task 8: Mutating tools and agent spawning

**Files:**
- Modify: `daemon/tools.go`
- Create: `daemon/agent.go`
- Test: `daemon/tools_test.go`, `daemon/agent_test.go`

**Interfaces:**
- Consumes: everything from Task 7.
- Produces: `cockpit_start`, `cockpit_stop`, `cockpit_restart`,
  `cockpit_write_input`, `cockpit_spawn_agent`, `cockpit_capture`,
  `cockpit_tasks`, plus:
  ```go
  type settleOptions struct {
      Poll     time.Duration
      Quiet    time.Duration
      Deadline time.Duration
  }
  func waitForSettle(ctx context.Context, r sources.Runner, target string, o settleOptions) bool
  ```

- [ ] **Step 1: Write failing tests**

```go
func TestWriteInputSendsLiteralThenEnter(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t2\t1\n"}}
	tools := testTools(t, f, config.RepoConfig{Label: "app", Path: "/r",
		Processes: []config.ProcessConfig{{Name: "dev", Command: "x"}}})
	if _, err := tools.Call(context.Background(), "cockpit_write_input",
		map[string]any{"project": "app", "process": "dev", "input": "hello"}); err != nil {
		t.Fatal(err)
	}
	calls := f.called("send-keys")
	if len(calls) != 2 {
		t.Fatalf("want literal + Enter, got %v", calls)
	}
	if !slices.Contains(calls[0], "-l") || !slices.Contains(calls[0], "hello") {
		t.Errorf("first call = %v", calls[0])
	}
	if !slices.Contains(calls[1], "Enter") {
		t.Errorf("second call = %v", calls[1])
	}
}

func TestWriteInputSubmitFalseSkipsEnter(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t2\t1\n"}}
	tools := testTools(t, f, config.RepoConfig{Label: "app", Path: "/r",
		Processes: []config.ProcessConfig{{Name: "dev", Command: "x"}}})
	tools.Call(context.Background(), "cockpit_write_input",
		map[string]any{"project": "app", "process": "dev", "input": "x", "submit": false})
	if len(f.called("send-keys")) != 1 {
		t.Errorf("submit=false must not send Enter: %v", f.calls)
	}
}

func TestWaitForSettleReturnsWhenOutputStops(t *testing.T) {
	f := &changingRunner{outputs: []string{"", "boot", "boot ready", "boot ready", "boot ready"}}
	ok := waitForSettle(context.Background(), f, "s:w", settleOptions{
		Poll: time.Millisecond, Quiet: 3 * time.Millisecond, Deadline: time.Second})
	if !ok {
		t.Error("should settle once output stops changing")
	}
}

func TestWaitForSettleGivesUpAtDeadline(t *testing.T) {
	f := &alwaysChangingRunner{}
	start := time.Now()
	ok := waitForSettle(context.Background(), f, "s:w", settleOptions{
		Poll: time.Millisecond, Quiet: time.Second, Deadline: 30 * time.Millisecond})
	if ok {
		t.Error("constantly-changing output must not report settled")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("must respect the deadline")
	}
}
```

`changingRunner` returns successive `outputs` entries then repeats the last;
`alwaysChangingRunner` returns a new counter value every call. Both live in
`daemon/agent_test.go`.

Plus: `cockpit_start` on an already-running process is a no-op reporting
`"already running"`; `cockpit_stop` issues `kill-window`; `cockpit_restart`
issues `respawn-window -k`; `cockpit_spawn_agent` creates a window with a
generated name when `name` is omitted, sanitizes an invalid supplied name, and
skips prompt delivery when `prompt` is absent; `cockpit_capture` appends to a
temp inbox file and the text lands in it; `cockpit_tasks` reads tasks and
toggles one by line number.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./daemon/ -run 'WriteInput|Settle|Spawn|Capture|Tasks' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`waitForSettle` sleeps 150ms, then polls `capture-pane` every `Poll`, hashing
the content. When the content is non-empty and its hash has been unchanged for
`Quiet`, it returns true. At `Deadline` it returns false. The caller writes the
prompt either way — a slightly early prompt beats no prompt.

`cockpit_spawn_agent` resolves the target project (defaulting to the first
configured repo), ensures its session exists, generates the window name
(`agent-<4 hex>` when omitted), runs `new-window` with `remain-on-exit`, and —
when `prompt` is set — runs settle-then-send in a goroutine so the tool returns
promptly with the window target.

`cockpit_capture` calls `sources.AppendInbox(cfg.Obsidian.InboxFile, text)`.
`cockpit_tasks` calls `sources.ReadTasks(cfg.Obsidian.TodayFile)`, or
`sources.ToggleTask` when a `toggle_line` argument is present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./daemon/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/
git commit -m "Add mutating tools and agent spawning"
```

---

### Task 9: Daemon lifecycle and CLI

**Files:**
- Create: `daemon/lifecycle.go`
- Create: `cmd/daemon.go`
- Modify: `cmd/root.go` (register the command)
- Test: `daemon/lifecycle_test.go`

**Interfaces:**
- Consumes: `Server`, `Tools`.
- Produces:
  ```go
  func StateDir() string
  func PidFilePath() string
  func LogFilePath() string
  func WritePidFile(path string, pid int) error
  func ReadPidFile(path string) (int, error)
  func IsRunning(pid int) bool
  func Run(ctx context.Context, cfg *config.Config, configPath, version string) error
  func LaunchAgentPlist(binPath, configPath string) string
  ```
  CLI: `cockpit daemon`, `daemon start|stop|status|install|uninstall`.

- [ ] **Step 1: Write failing tests**

```go
func TestPidFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := WritePidFile(path, 4242); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPidFile(path)
	if err != nil || got != 4242 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestReadPidFileMissing(t *testing.T) {
	if _, err := ReadPidFile(filepath.Join(t.TempDir(), "absent.pid")); err == nil {
		t.Fatal("missing pidfile must error")
	}
}

func TestReadPidFileGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	os.WriteFile(path, []byte("not-a-pid"), 0644)
	if _, err := ReadPidFile(path); err == nil {
		t.Fatal("garbage pidfile must error")
	}
}

func TestIsRunningForSelf(t *testing.T) {
	if !IsRunning(os.Getpid()) {
		t.Error("the current process is running")
	}
	if IsRunning(999999) {
		t.Error("pid 999999 should not be running")
	}
}

func TestLaunchAgentPlistContainsPaths(t *testing.T) {
	p := LaunchAgentPlist("/usr/local/bin/cockpit", "/home/j/.config/cockpit/config.toml")
	for _, want := range []string{
		"com.jeffdhooton.cockpit.daemon",
		"/usr/local/bin/cockpit",
		"/home/j/.config/cockpit/config.toml",
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}
```

Plus an end-to-end test that starts `Run` on port 0 via an injected listener,
issues a `tools/list` request, and asserts the tool count — or, if a listener
cannot be injected cleanly, a test that constructs the server with real `Tools`
and asserts `Definitions()` has 14 entries with unique names.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./daemon/ -run 'PidFile|IsRunning|LaunchAgent' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

`IsRunning` uses `syscall.Kill(pid, 0)` via `os.FindProcess` + `Signal(syscall.Signal(0))`.
`StateDir` is `filepath.Dir(config.DefaultConfigPath())`.

`Run` builds `Tools` and `Server`, binds `127.0.0.1:<port>`, logs
`cockpit daemon listening on http://127.0.0.1:<port>/mcp`, and shuts down
gracefully on context cancel (SIGINT/SIGTERM via `signal.NotifyContext`).

`cmd/daemon.go` adds the cobra command tree. `daemon start` re-execs the current
binary with `daemon --foreground` using `exec.Command` with `Setsid`, redirects
stdout/stderr to the log file, writes the child pid, and prints the port.
A pidfile whose pid is not running is treated as stale and overwritten.
`daemon stop` sends SIGTERM and removes the pidfile. `daemon status` prints
running/stopped, pid, and port. `daemon install` writes
`~/Library/LaunchAgents/com.jeffdhooton.cockpit.daemon.plist` and runs
`launchctl load -w`; `uninstall` reverses it. On non-darwin, install/uninstall
report that they are macOS-only.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build -o /tmp/cockpit . && /tmp/cockpit daemon --help`
Expected: PASS and the help text lists the subcommands.

- [ ] **Step 5: Commit**

```bash
git add daemon/ cmd/
git commit -m "Add daemon lifecycle and CLI"
```

---

### Task 10: Registration cutover, template, and docs

**Files:**
- Create: `scripts/register-mcp.sh`
- Modify: `config_template.go`
- Modify: `README.md`
- Modify: `~/.claude.json`, `~/.codex/config.toml` (user machine, via the script)

**Interfaces:**
- Consumes: the running daemon on port 45679.
- Produces: no Go interfaces.

- [ ] **Step 1: Update the config template**

Add a commented process example under the repos section and a `[daemon]`
section to `configTemplate` in `config_template.go`:

```
# [[repos]]
# path = "~/workspace/my-project"
# label = "my-project"
#
#   Background processes launch as tmux windows when you jump to the project.
#   Window 0 is always your shell; each process gets its own numbered window.
#   [[repos.processes]]
#   name = "dev"
#   command = "npm run dev"
#   auto_start = true

[daemon]
# Serve the tool server for agents (Claude Code, Codex)
enabled = true
# Port for the local tool server
port = 45679
```

- [ ] **Step 2: Write the registration script**

`scripts/register-mcp.sh` must: back up `~/.claude.json` and
`~/.codex/config.toml` to `<file>.bak-$(date +%Y%m%d-%H%M%S)`; use `python3` to
load the JSON, delete `mcpServers.helm`, set
`mcpServers.cockpit = {"url": "http://127.0.0.1:45679/mcp"}`, and write it back
with `indent=2`; rewrite the `[mcp_servers.helm]` block in the codex TOML to
`[mcp_servers.cockpit]` with the new url; print what changed. It must be
idempotent — running it twice leaves the same result.

- [ ] **Step 3: Run the script and verify**

```bash
bash scripts/register-mcp.sh
grep -A2 '"cockpit"' ~/.claude.json
grep -A2 'mcp_servers.cockpit' ~/.codex/config.toml
```
Expected: both point at `http://127.0.0.1:45679/mcp`, no `helm` entry remains,
and `python3 -c 'import json;json.load(open("...."))'` still parses
`~/.claude.json`.

- [ ] **Step 4: Verify the live server**

```bash
cockpit daemon start
curl -s -X POST http://127.0.0.1:45679/mcp \
  -H 'content-type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | python3 -m json.tool | head -30
```
Expected: 14 tools, all prefixed `cockpit_`.

- [ ] **Step 5: Update the README**

Add a "Project processes" section documenting the config shape and the tmux
window-navigation keybind table from the spec, including the optional
`bind -n M-1` / `M-h` / `M-l` additions. Add a "Daemon" section covering
`cockpit daemon start|stop|status|install`, the port, and the agent registration
snippet for both Claude Code and Codex. Add the new keys to the keybindings
table where relevant.

- [ ] **Step 6: Commit**

```bash
git add config_template.go README.md scripts/
git commit -m "Document processes and register the daemon"
```

---

## Self-Review

**Spec coverage:** Config shape → Task 1. Validation → Task 1. Launch/reconcile
behavior → Tasks 2–4. `remain-on-exit` → Task 3. TUI indicator → Task 4. Dead
process signals → Task 5. Daemon shape and lifecycle → Task 9. Transport → Task
6. Twelve parity tools + two native → Tasks 7–8. Settle heuristic → Task 8.
Degradations (`status` from scrollback) → Task 7. Registration cutover → Task
10. tmux keybind docs → Task 10. Runner seam → Task 2.

**Type consistency:** `ProcessInfo`, `ProcessState`, `Window`, `Signal`,
`ToolDefinition`, `Tools`, and `Server` are each defined once and referenced
with the same field names throughout. `ShouldAutoStart`/`ResolvedWorkingDir` are
used in Tasks 2–4 exactly as defined in Task 1.

**Known ordering constraint:** Task 4 changes `tmuxJump`'s signature, so its
call sites must be updated in the same commit or the build breaks. Task 7's
`fakeRunner` is a copy, not an import — Go test helpers do not cross packages.
