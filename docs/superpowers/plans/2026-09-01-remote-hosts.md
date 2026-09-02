# Remote Hosts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Watch and drive tmux sessions, git repos, and processes on a second machine over SSH, with host-qualified tiles and a local view session for jumping.

**Architecture:** An `SSHRunner` that satisfies the existing `sources.Runner` seam by shelling out to the system `ssh` with ControlMaster, so every tmux-backed function runs remotely unchanged. Git gains the same seam. Identity gains a host dimension end to end. The jump creates a local per-host tmux session whose windows run `ssh -t ... tmux new -A`. Agent status needs no tunnel: the remote box runs its own daemon and its tmux is the store the session list already reads.

**Tech Stack:** Go 1.22+, system `ssh` (OpenSSH), tmux, bubbletea. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-01-remote-hosts-design.md`

## Global Constraints

- **No new dependencies.** No `golang.org/x/crypto/ssh`.
- **`ssh` argv always includes** `-o BatchMode=yes -o ControlMaster=auto -o ControlPath=<cfgdir>/ssh-%C -o ControlPersist=60s -o ConnectTimeout=5` and `--` before the host.
- **Every remote argument is `shellQuote`d.** No exceptions, no "safe-looking" strings.
- **Exit 255 from ssh is `ErrHostUnreachable`.** Any other exit is the remote command's own error.
- **Host names match `^[a-zA-Z0-9_-]+$`** (same grammar as `validLabel`). Grid key is `host/label`; local is bare.
- **Remote tmux path is absolute and required per host.** PATH is never trusted remotely.
- **A remote reconcile fails closed on `ErrHostUnreachable`.** Nothing is launched against a host whose state is unknown.
- **Staleness for remote status uses the remote clock.**
- Run `go build ./... && go vet ./... && go test ./...` before every commit.

---

### Task 1: Host config and validation

**Files:** Modify `config/config.go`, `config/config_test.go`

**Interfaces produced:**
- `type HostConfig struct { Name, Tmux, Cockpit string }`
- `Config.Hosts []HostConfig` (`toml:"hosts"`)
- `RepoConfig.Host string` (`toml:"host"`)
- `func (c *Config) Host(name string) (HostConfig, bool)`
- `func (r RepoConfig) Key() string` — `host/label` or `label`

- [ ] **Step 1: Failing tests**

```go
func TestHostConfigIsValidated(t *testing.T) {
	cases := map[string]string{
		"undeclared host": "[[repos]]\nhost = \"nope\"\npath = \"/r\"\nlabel = \"a\"\n",
		"relative tmux":   "[[hosts]]\nname = \"mini\"\ntmux = \"tmux\"\n",
		"bad host name":   "[[hosts]]\nname = \"mi ni\"\ntmux = \"/usr/bin/tmux\"\n",
		"missing tmux":    "[[hosts]]\nname = \"mini\"\n",
	}
	for name, body := range cases {
		cfg := loadFrom(t, "[obsidian]\nvault_path = \"/v\"\n"+body)
		if err := validate(cfg); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}

func TestRepoKeyQualifiesRemote(t *testing.T) {
	if got := (RepoConfig{Label: "docket"}).Key(); got != "docket" {
		t.Errorf("local key = %q", got)
	}
	if got := (RepoConfig{Host: "mini", Label: "docket"}).Key(); got != "mini/docket" {
		t.Errorf("remote key = %q", got)
	}
}
```

`loadFrom` writes the body to a temp file and decodes it with `toml.Decode` into a `Config`, applying defaults — mirror whatever helper `config_test.go` already uses for inline configs.

- [ ] **Step 2:** Run `go test ./config/ -run 'TestHostConfig|TestRepoKey'` — FAIL, undefined.
- [ ] **Step 3:** Add the types, `validHost` regexp, `validateHosts(cfg)` called from `validate`, `Host()`, and `Key()`.
- [ ] **Step 4:** Run `go test ./config/` — PASS. Commit: `Add host config`.

---

### Task 2: `shellQuote` and `SSHRunner`

**Files:** Create `sources/ssh.go`, `sources/ssh_test.go`

**Interfaces produced:**
- `var ErrHostUnreachable = errors.New("host unreachable")`
- `func shellQuote(s string) string`
- `type SSHRunner struct { Host, Tmux, ControlDir string; Timeout time.Duration }`
- `func (r SSHRunner) Run(ctx, args ...string) (string, error)` — `tmux <args>` remotely
- `func (r SSHRunner) RunShell(ctx, script string) (string, error)` — raw shell, for git and the clock
- `func (r SSHRunner) RemoteNow(ctx) (time.Time, error)`
- `func (r SSHRunner) argv(remote []string) []string` — pure, for tests
- `func classifySSHError(err error, stderr []byte) error`

- [ ] **Step 1: Failing tests**

```go
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"two words":   "'two words'",
		"it's":        `'it'\''s'`,
		"$HOME":       "'$HOME'",
		"":            "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSSHRunnerArgvIsMultiplexedAndQuoted(t *testing.T) {
	r := SSHRunner{Host: "mini", Tmux: "/opt/homebrew/bin/tmux", ControlDir: "/cfg"}
	got := r.argv([]string{"/opt/homebrew/bin/tmux", "new-window", "-n", "dev", "npm run dev"})

	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ControlMaster=auto",
		"-o", "ControlPath=/cfg/ssh-%C", "-o", "ControlPersist=60s", "-o", "ConnectTimeout=5",
		"--", "mini", "'/opt/homebrew/bin/tmux' 'new-window' '-n' 'dev' 'npm run dev'"}
	if !slices.Equal(got, want) {
		t.Errorf("argv =\n%q\nwant\n%q", got, want)
	}
}

func TestClassifySSHError(t *testing.T) {
	unreachable := &exec.ExitError{ProcessState: exitState(255)}
	if err := classifySSHError(unreachable, []byte("ssh: connect to host mini: No route")); !errors.Is(err, ErrHostUnreachable) {
		t.Errorf("255 must be unreachable, got %v", err)
	}
	remote := &exec.ExitError{ProcessState: exitState(1)}
	if err := classifySSHError(remote, []byte("no server running")); errors.Is(err, ErrHostUnreachable) || !strings.Contains(err.Error(), "no server running") {
		t.Errorf("a remote exit must carry the remote message, got %v", err)
	}
	if err := classifySSHError(exec.ErrNotFound, nil); err == nil {
		t.Error("a missing local ssh must be an error")
	}
}
```

`exitState(code)` builds a `*os.ProcessState` by running `sh -c "exit N"` once; put it in the test file.

- [ ] **Step 2:** Run — FAIL.
- [ ] **Step 3:** Implement. `Run` builds `argv(append([]string{r.Tmux}, args...))`, executes with `exec.CommandContext`, captures stderr, classifies. `RunShell` sends the script unquoted as the single remote argument. `RemoteNow` is `RunShell(ctx, "date +%s")` parsed. `ControlDir` defaults to `config.Dir()`; `MkdirAll(0o700)` on first use.
- [ ] **Step 4:** PASS. Commit: `Add the ssh runner`.

---

### Task 3: Git behind a seam

**Files:** Modify `sources/git.go`, `sources/git_test.go`

**Interfaces produced:**
- `type CommandRunner interface { RunIn(ctx, dir, name string, args ...string) (string, error) }`
- `type LocalCommandRunner struct{}`
- `func (r SSHRunner) RunIn(ctx, dir, name string, args ...string)` — `cd -- '<dir>' && <name> <args>` via `RunShell`
- `func GetGitStatus(ctx, cr CommandRunner, repos []config.RepoConfig) []GitRepoStatus` (signature change; one caller in `tui/app.go`)
- `GitRepoStatus.Host string`

- [ ] **Step 1: Failing test** — a `fakeCommandRunner` recording `(dir, name, args)`; assert `fetchOneRepo` runs `rev-parse` in the repo's path through it and carries `Host` onto the result. Plus `TestSSHRunnerRunInChangesDirectoryFirst` asserting the remote script begins `cd -- '~/workspace/docket' && git`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement; update the `tui/app.go` caller to pass `sources.LocalCommandRunner{}` for now. **Step 4:** PASS. Commit: `Put git behind a command runner`.

---

### Task 4: Host on sessions and targets

**Files:** Modify `sources/source.go`, `sources/tmux.go` (`parseTmuxOutput` takes `host string, now time.Time`), `tui/grid.go`, `tui/grid_test.go`

**Interfaces produced:**
- `TmuxSession.Host string`; `func (s TmuxSession) Key() string`
- `Target.Host string`; `func (t Target) Key() string`
- `func ListSessionsOn(ctx, r Runner, host string, now time.Time) ([]TmuxSession, error)`
- `BuildTargets(sessions, repos, statuses map[string]AgentStatus /* keyed by Key() */, selfSession string)` — joins on key; skips sessions carrying `@cockpit_view_of`

The session format gains `#{@cockpit_view_of}` as an eighth field; a non-empty value marks a local view session that the grid must not render.

- [ ] **Step 1: Failing tests**

```go
func TestBuildTargetsKeepsSameLabelOnTwoHostsApart(t *testing.T) {
	local := sess("docket")
	remote := sess("docket"); remote.Host = "mini"
	targets := BuildTargets([]sources.TmuxSession{local, remote}, nil, nil, "cockpit")
	if len(targets) != 2 || targets[0].Key() == targets[1].Key() {
		t.Fatalf("want two distinct tiles, got %v", labels(targets))
	}
}

func TestBuildTargetsExcludesTheCockpitSessionPerHost(t *testing.T) {
	remoteCockpit := sess("cockpit"); remoteCockpit.Host = "mini"
	if got := BuildTargets([]sources.TmuxSession{sess("cockpit"), remoteCockpit}, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("cockpit's own session is excluded on every host, got %v", labels(got))
	}
}

func TestBuildTargetsExcludesViewSessions(t *testing.T) {
	view := sess("mini"); view.ViewOf = "mini"
	if got := BuildTargets([]sources.TmuxSession{view}, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("a local view of a remote host is not a project, got %v", labels(got))
	}
}

func TestRenderTileShowsHostPrefix(t *testing.T) {
	s := sess("docket"); s.Host = "mini"
	out := renderTile(Target{Label: "docket", Host: "mini", Session: &s}, 22, false)
	if !strings.Contains(out, "mini/") {
		t.Errorf("remote tile must name its host:\n%s", out)
	}
}
```

- [ ] **Step 2:** FAIL. **Step 3:** Implement; widen fixtures to eight fields (`|||` → `||||`) across `sources` and `daemon` tests, as Task 3 of the hook plan did. **Step 4:** PASS. Commit: `Give sessions and targets a host`.

---

### Task 5: Poll each host

**Files:** Modify `tui/app.go`, create `tui/hosts.go`, `tui/hosts_test.go`

**Interfaces produced:**
- `type hostPoll struct { Host string; Sessions []TmuxSession; Repos []GitRepoStatus; Processes map[string][]ProcessInfo; Err error }`
- `hostDataMsg{ hostPoll }`
- `type backoff struct{ failures int }` with `func (b backoff) next() time.Duration` — 5s, 30s, 60s, 60s…
- `func (m Model) fetchHost(h config.HostConfig) tea.Cmd`
- `Model.hostState map[string]hostView` holding last data, backoff, and `Unreachable bool`

- [ ] **Step 1: Failing tests** — backoff steps 5→30→60→60 and resets on success; `mergeHost` keeps last-known sessions and marks unreachable on `ErrHostUnreachable`; a successful poll replaces them and clears the flag.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. `fetchHost` builds one `SSHRunner`, calls `RemoteNow`, `ListSessionsOn`, `GetGitStatus` for that host's repos, `InspectProcesses` for each, returns one message. On `hostDataMsg`, merge into `hostState`, then rebuild the combined session/repo/process lists the grid reads. Schedule the next poll with the backoff delay. **Step 4:** PASS. Commit: `Poll remote hosts with backoff`.

---

### Task 6: Unreachable on the tile

**Files:** Modify `tui/grid.go`, `tui/grid_test.go`

- [ ] **Step 1: Failing test** — `Target{Host:"mini", Unreachable:true, Session:&s}` renders `unreachable` in place of status and still renders the branch.
- [ ] **Step 2–4:** `Target.Unreachable bool`, set from `hostState` in `BuildTargets`' caller; render `WarningText.Render("⚠ unreachable")`. Commit: `Show an unreachable host`.

---

### Task 7: The remote jump

**Files:** Modify `tui/app.go`, create `sources/view.go`, `sources/view_test.go`

**Interfaces produced:**
- `func ViewSessionArgs(host string) []string` — `new-session -d -s <host> ; set-option -t <host> @cockpit_view_of <host>`
- `func ViewWindowArgs(host, label, tmux string) []string` — `new-window -d -t <host>: -n <label> ssh -t -- <host> <tmux> new-session -A -s <label>`
- `func JumpRemote(ctx, local Runner, remote SSHRunner, host config.HostConfig, repo config.RepoConfig) error`

- [ ] **Step 1: Failing tests** — argv for both builders (exact); `JumpRemote` calls `has-session`/`new-session` on the **remote** runner before any local call; a local view window that exists but whose pane is dead is respawned; `switch-client -t mini` then `select-window -t mini:docket` are the last two local calls.
- [ ] **Step 2:** FAIL. **Step 3:** Implement; route `tmuxJumpRepo` to `JumpRemote` when `repo.Host != ""`. **Step 4:** PASS. Commit: `Jump to a remote project through a local view`.

---

### Task 8: Remote reconcile fails closed on the transport

**Files:** Modify `sources/processes.go`, `sources/processes_test.go`

- [ ] **Step 1: Failing test** — `ReconcileProcesses` with a runner whose `has-session` and `list-windows` both return `ErrHostUnreachable` launches nothing and returns one error wrapping it.
- [ ] **Step 2–4:** In the existing error branch, `errors.Is(err, ErrHostUnreachable)` returns before the `SessionExists` check. Commit: `Never launch against an unreachable host`.

---

### Task 9: Remote hook install

**Files:** Modify `cmd/hook_install.go`, `cmd/hook_install_test.go`

- [ ] **Step 1: Failing test** — `--host mini` with a host whose `Cockpit` is set builds `ssh ... -- mini '<cockpit>' 'hook' 'install'`; a host with no `Cockpit` returns a named error saying so.
- [ ] **Step 2–4:** Implement via `SSHRunner.RunShell`; print the remote output verbatim. Commit: `Install hooks on a remote host`.

---

### Task 10: Remote staleness uses the remote clock

**Files:** `sources/tmux.go`, `sources/tmux_test.go`

- [ ] **Step 1: Failing test** — a status stamped 5 minutes ago on the remote clock, with the local clock 20 minutes ahead, is still reported when parsed with the remote `now`.
- [ ] **Step 2–4:** Already parameterised in Task 4; this task is the test plus wiring `RemoteNow` through `fetchHost`. Commit: `Judge remote staleness by the remote clock`.

---

### Task 11: Integration against `mini`, docs

- [ ] Gated tests under `COCKPIT_TEST_HOST`: list sessions; create `cockpit-it-<pid>`, see it in the list, kill it; `RemoteNow` within 60s of local.
- [ ] Add `[[hosts]]` and `host = ` to `config_template.go` as commented examples.
- [ ] README: a `## Remote hosts` section — config, the jump, what unreachable means, installing cockpit and hooks on the remote box.
- [ ] Add `mini` and its four repos to the live config; confirm tiles appear; jump to `mini/docket`; `prefix S` back.
- [ ] Commit: `Document remote hosts`.

## Self-Review

Spec §2 → Task 4. §3 transport → Task 2; git → Task 3; config → Task 1; classification → Task 2. §4 polling and backoff → Task 5. §5 jump → Task 7. §6 processes → Task 8; status/clock → Task 10; `--host` install → Task 9. §7 → Tasks 5, 6, 8. §8 tests distributed; integration → Task 11. Names used later match names defined earlier: `SSHRunner`, `ErrHostUnreachable`, `RunShell`, `RemoteNow`, `CommandRunner`, `Key()`, `ViewOf`, `hostPoll`.
