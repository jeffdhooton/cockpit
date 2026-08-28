# Goal 1B evidence — Cockpit Build client

Branch: `goal/cockpit-build-client`
Worktree: `/Users/jeff/workspace/cockpit-build-client`
Contract consumed: JBuild `docs/contracts/session-control-v1.md` (frozen v1, unmodified)

## Scope

Cockpit is a thin consumer of the frozen `buildctl` contract. It merges Build sessions with
legacy default-server tmux sessions into one navigator, with launch / resume / attach driven
only by contract data. Build absence, timeouts, malformed data, incompatible schema, stale
runs, and child-process failure all degrade visibly and nonfatally to legacy-only operation.

## Changed files by responsibility

- `buildctl/` (new package) — contract client:
  - `buildctl.go` — executable resolution (configured → PATH → `~/.build/bin/buildctl`),
    bounded subprocess runner (4 MiB stdout/stderr bound, 10s default timeout, ctx
    cancellation), strict one-object envelope decoding, `schema_version == 1` enforcement,
    forward-compatible unknown-field tolerance, exit-code and stable-error-code mapping,
    validated launch/resume argv construction (values as argv elements, never shell),
    interactive attach command (no `--json`, no timeout).
  - `buildctl_test.go` — hermetic tests against a fake `buildctl` shell executable:
    valid decodes, forward compatibility, 15 malformed/hostile response cases rejected as a
    whole, every stable v1 error code, exit codes 2/3/4/5/10/unknown, timeout, cancellation,
    missing executable, exact argv recording (shell-metacharacter prompt travels as one argv
    element), attach argv (no `--json`), resolution order, bounded output.
  - `testdata/*.json` — canonical contract fixtures for the Goal 1C integrator.
- `tui/merge.go` (new) — `MergedSession` with source-scoped identity keys
  (`build:<conversation_id>` / `legacy:<name>`); name collisions never merge; deterministic
  recency ordering; `Attachable()`/`Resumable()` read contract flags only.
- `tui/sessions.go` — merged rendering (cards + compact); Build rows show title, project,
  agent, and contract status; quiet Build indicator line.
- `tui/app.go` — `BuildClient` interface seam (`*buildctl.Client` in prod, fake in tests);
  build fetch on init/tick; failure drops stale Build records and sets a quiet actionable
  note; `activateSession` gates attach/resume on contract flags; interactive attach via
  `tea.ExecProcess` (Bubble Tea suspends/restores the terminal around the child on success,
  detach, and failure); attach failure yields a transient error with TUI state untouched;
  Build launch dialog (`L`): local non-archived projects → agent → permission (dangerous only
  when explicitly chosen) → optional prompt; legacy preview/status polling restricted to
  legacy sessions (Build status is contract data, never pane scraping); save-to-config
  refuses Build sessions.
- `tui/keyhints.go` — `L launch` hint when Build is available; launch-dialog hints.
- `tui/merge_test.go`, `tui/build_test.go` (new) — merge identity/collision/ordering/gating;
  unavailable-at-startup fallback; failure-drops-stale-records across all failure classes;
  attach uses contract `run_id` and the `tea.ExecProcess` suspend/restore path; resume gated
  by contract flag with exact conversation id; forbidden actions produce hints only; attach
  failure leaves TUI intact; Build preview is contract-only (no capture cmd); legacy
  switch/preview unchanged; full launch-dialog flow asserts exact `LaunchOptions`; project
  filtering (archived/remote excluded); project-fetch failure blocks submission;
  save-as-repo skips Build sessions.
- `config/config.go`, `config_template.go` — optional `[build].command`.

## Verification commands and results

| Command | Exit | Notes |
| --- | --- | --- |
| `gofmt -l .` | 0 | no output |
| `go vet ./...` | 0 | clean |
| `go test ./...` | 0 | all 6 packages pass, incl. 40+ new cases |
| `go build ./...` | 0 | clean |

Hermeticity: tests use a fake `buildctl` shell executable in `t.TempDir()`, a stubbed
`buildctlResolve` seam, and an injected `fakeBuildClient`. No test touches a real Build home,
the live/default tmux server, or the JBuild worktree. No test executes `tmuxSwitch` or a real
attach child.

## Deferred scope / contract questions

- Remote host launch/attach is Wave 2 per contract; launch dialog filters to
  `host_kind == "local"` non-archived projects.
- Build session preview shows contract fields only; rich Build previews would require a
  contract addition (not requested).
- No contract defects found. v1 was implementable as frozen.

## Grading record

(Filled in as grading rounds complete — see below.)
