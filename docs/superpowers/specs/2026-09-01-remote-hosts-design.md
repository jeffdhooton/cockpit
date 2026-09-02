# Remote Hosts — Design

Date: 2026-09-01
Status: **approved** — implement

Cockpit watches and drives a second machine over SSH. The first host is
`mini`, the jclaws Mac mini on the tailnet, where Codex runs and four repos
live. Adapted from `BUILD_APP_SUBSYSTEMS.md` §2 and §6, with one deliberate
departure from that document's transport recommendation (§3 below).

The Hermes gateway on the same box is a separate, smaller design:
`2026-09-01-hermes-tile-design.md`. The two share nothing but the grid.

## 1. What a remote host is

A remote host is an SSH alias plus the absolute path of its tmux. Cockpit
reads its tmux sessions and git status the way it reads local ones, renders
them as tiles, creates sessions and reconciles processes on it when you jump,
and reads agent status its own daemon has written into that machine's tmux.

Cockpit's role does not change: it drives tmux, never the agent. What it does
locally, it now does on `mini` through a different pipe.

### Facts about `mini` the design rests on

Probed 2026-09-01, all read-only:

- SSH works non-interactively (`BatchMode`), identity file only —
  `~/.ssh/config` sets `IdentitiesOnly yes` and `IdentityAgent none` for it.
- `~/.ssh/config` begins with `Include ~/.orbstack/ssh/config`.
- tmux 3.7c at `/opt/homebrew/bin/tmux`. **No tmux server was running.**
- Non-login PATH lacks `~/.local/bin` and Homebrew, so `codex`, `hermes`, and
  a future `cockpit` are invisible to a bare `ssh mini cmd`. Login PATH has
  them.
- Repos at `~/workspace/{docket,deepresearch,deepresearch-codernext,personal-site-2026}`.
- `claude` is not installed. `codex` is. Hermes runs as launchd services,
  not in tmux.

## 2. Identity: a host dimension that never flattens

Today `Target.Label` is the session name, the repo label, and the tmux target.
With two machines, `docket` here and `docket` on `mini` would share one tile,
one status option, and one jump destination. The source document's lesson
(§3.1: a native id is unique only within its profile) applies directly.

- `config.RepoConfig` and `sources.TmuxSession` gain `Host string`. Empty
  means local. No existing config changes shape.
- The grid key is `host/label` for remote (`mini/docket`) and the bare label
  for local. `validLabel` forbids `/`, so the qualified form cannot collide
  with a plain label, and `validHost` uses the same grammar.
- `BuildTargets` joins on `(host, name)`. The cockpit session is excluded per
  host: `mini` will hold a `cockpit` session too if cockpit ever runs there.
- `Target` gains `Host`. The tile renders the host as a muted prefix.

## 3. Transport: the system `ssh`, multiplexed

**Decision: shell out to `ssh` with ControlMaster. Do not use
`golang.org/x/crypto/ssh`.**

The source document recommends an in-process client with a hand-written
known_hosts parser, a generation-guarded reconnect, and `ssh -G` for config
resolution. Every one of those exists to reproduce what the real `ssh` binary
already does — and for `mini` specifically, the in-process client would have
to parse and honour `IdentityAgent none` and `Include` itself or fail to
connect at all. At a five-second poll the per-call subprocess cost is
irrelevant. The design is therefore:

```go
// sources/ssh.go
type SSHRunner struct {
	Host    string        // ssh alias
	Tmux    string        // absolute remote path
	Timeout time.Duration // per call; default 10s
}
func (r SSHRunner) Run(ctx context.Context, args ...string) (string, error)
```

`Run` executes:

```
ssh -o BatchMode=yes -o ControlMaster=auto -o ControlPath=<cfgdir>/ssh-%C
    -o ControlPersist=60s -o ConnectTimeout=5 -- <host> <tmux> <args, quoted>
```

- `BatchMode=yes` turns an unknown host key or a needed passphrase into a
  fast, named failure instead of a hung poll waiting on a prompt.
- `ControlMaster=auto` + `ControlPersist=60s` keeps one TCP connection per
  host across calls. The socket lives in cockpit's config directory (mode
  0700), not `/tmp`.
- Every argument after the host is passed through `shellQuote` (single-quote
  wrapping, `'` → `'\''`). Remote `sshd` runs the command through the login
  shell, so quoting is the entire correctness of process launch — `npm run
  dev`, paths with spaces, `$` in an env value.
- `--` before the host, so a host name can never be read as an option.

`SSHRunner` satisfies the existing `sources.Runner` interface. `ListSessions`,
`ListWindows`, `EnsureSession`, `InspectProcesses`, `ReconcileProcesses`,
`StartProcess`, `StopProcess`, `RestartProcess`, and `CapturePane` therefore
run remotely **with no changes**, including this morning's fail-closed
reconcile. That seam was built for exactly this.

### Git

`sources/git.go` calls `exec` directly. It gains the same seam:

```go
type CommandRunner interface {
	RunIn(ctx context.Context, dir string, name string, args ...string) (string, error)
}
```

with `LocalCommandRunner` and an SSH implementation that emits
`cd -- '<dir>' && git ...` over the same multiplexed connection. Remote paths
are expanded remotely: `~` is left for the remote shell, never resolved with
the Mac's home directory.

### Error classification

ssh exits 255 for its own failures — unreachable host, refused key, timeout,
`BatchMode` refusal — and passes the remote command's exit status through
otherwise. `SSHRunner` maps 255 to `ErrHostUnreachable` (wrapping stderr) and
lets everything else surface as the remote command's error. This is the
remote twin of `ErrTmuxNotFound`: it keeps "mini is down" from rendering as
"mini has no sessions."

### Config

```toml
[[hosts]]
name = "mini"                      # an ssh config alias; ssh resolves it
tmux = "/opt/homebrew/bin/tmux"    # required, absolute
cockpit = "~/.local/bin/cockpit"   # optional; enables remote hook install

[[repos]]
host = "mini"
path = "~/workspace/docket"
label = "docket"
  [[repos.processes]]
  name = "dev"
  command = "npm run dev"
```

Validation at load: every `repos.host` names a declared host; every host has
an absolute `tmux`; names match `validHost`. A bad host fails startup with a
message, not the first poll with an empty grid.

## 4. Polling

Each host polls on the local interval, independently, in its own goroutine
via `tea.Cmd`. A host's result carries its sessions, its git statuses, and its
process states together, so a tile never shows fresh git beside stale
sessions from a different pass.

A host that returns `ErrHostUnreachable` enters backoff: 5s, 30s, 60s, capped.
Its tiles keep their last-known data and show `⚠ unreachable` in place of
status. A successful poll resets the backoff. The grid never goes blank for a
host that was there a moment ago.

## 5. The jump

Enter on `mini/docket`:

1. **Remote, over SSH:** `EnsureSession` then `ReconcileProcesses` on `mini`,
   the same code and the same order as a local jump. The remote session and
   its process windows exist before any local window is created.
2. **Local view:** ensure a local tmux session named `mini` exists (created
   detached, with `@cockpit_view_of = mini` set so the grid skips it), ensure
   it has a window named `docket` whose command is
   `ssh -t -- mini <tmux> new-session -A -s docket`, then `switch-client -t
   mini` and `select-window -t mini:docket`.
3. `prefix S` returns to cockpit as from any session.

The view window is disposable. Killing it kills nothing remote; `-A`
reattaches next time. A dropped link leaves ssh's own exit message in the
window, and Enter on the tile reconciles the window and reconnects. There is
no automatic reconnect loop: the source document's warning about stale
clients being adopted (§2.4) is avoided by not having a client to go stale.

A view window whose ssh has exited is respawned on the next jump, not on a
timer.

## 6. Processes and status on the remote side

**Processes** need nothing new. The process indicator, dead-process signals,
`cockpit_status` pattern matching, start, stop, and restart already go
through `Runner`. The fail-closed reconcile applies unchanged, and on a remote
host it is additionally gated on the transport: `has-session` failing during
a link drop is `ErrHostUnreachable`, which reconcile treats as *unknown* and
refuses to launch against. Double-starting a dev server on a machine you are
not looking at is the one failure this design must not have.

**Agent status** needs no tunnel. The source document tunnels hook events
back to the Mac because Build's status server lives there. Cockpit's daemon
is stateless and its store is tmux, so the daemon runs on `mini` too:

- `cockpit` is installed on `mini` (`go install`, or the binary copied), its
  daemon runs on `mini`'s loopback, and `cockpit hook install` runs there.
- Codex on `mini` fires a hook → `mini`'s daemon → `@cockpit_status` in
  `mini`'s tmux → read by the SSH `list-sessions` this design already makes.
  The format string does not change.
- `cockpit hook install --host mini` runs the remote binary's installer over
  SSH, so one command covers both machines.

**Clock skew.** `@cockpit_status_at` is `mini`'s clock; staleness is judged
against the remote clock, fetched once per poll by a separate `date +%s`
over the same multiplexed connection (`SSHRunner.RemoteNow`). `SSHRunner`
therefore also exposes a raw-command path, `RunShell`, used only for this and
for git. A ten-minute skew would otherwise blank every remote status
silently. `parseTmuxOutput` takes the clock as a parameter rather than
calling `time.Now()`.

## 7. Degradation, named

| Situation | Behaviour |
|---|---|
| Host unreachable | Tiles keep last data, show `⚠ unreachable`, poll backs off |
| Host key unknown / refused | Same as unreachable; stderr from ssh shown in the error line |
| Remote tmux server not running | Repos render dormant (`○ no session`), as locally |
| Remote tmux binary missing | `ErrTmuxNotFound` from the remote command, tile shows `tmux missing` |
| Remote git error | `git err` on the tile, as locally |
| Remote cockpit not installed | Status stays inferred; `hook install --host` says so |
| Link drops mid-reconcile | Reconcile fails closed; nothing launched |
| View window's ssh exited | Window shows ssh's message; next Enter respawns |

## 8. Testing

- `shellQuote` and `SSHRunner` argv construction: pure, table-tested, with
  a space, a `'`, a `$`, and an empty argument.
- Exit-code classification: 255 → `ErrHostUnreachable`; 1 with stderr →
  the remote error; `exec.ErrNotFound` for a missing local `ssh`.
- `BuildTargets`: two hosts with the same label produce two tiles; the
  cockpit session is excluded per host; a view session is excluded.
- Jump argv: the exact local `new-session`/`new-window`/`switch-client`
  sequence, and that the remote `EnsureSession` runs first.
- Backoff: three failures step 5→30→60; a success resets.
- Config validation: undeclared host, relative tmux path, bad host name.
- Remote staleness uses the remote clock.
- Integration, gated on `COCKPIT_TEST_HOST`: list sessions on the real host,
  create and kill a throwaway session, read status written by the remote
  daemon.

## 9. What this does not do

- No auto-discovery of remote repos. Declared in config, as locally.
- No reverse tunnel, no in-process SSH client, no known_hosts parser.
- No remote Claude Code hooks: `claude` is not installed on `mini`. The
  installer handles it if that changes.
- No Hermes. See the companion design.
