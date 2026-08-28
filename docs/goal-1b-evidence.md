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
| `gofmt -l <diff-touched files>` | 0 | no output; 6 pre-existing visualizer files (boids/orbital/rain/repos/starfield/styles) were never gofmt-clean at the base commit and are untouched by this branch |
| `go vet ./...` | 0 | clean |
| `go test ./... -count=1` | 0 | all 6 packages pass; also passes under `-race` (verified independently by graders 5 and 6) |
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

- **House-rules check (explore sub-agent, commit be53526):** no violations on any of the six
  house rules. Six nits reported; all fixed in 1ccb780 (labelExists legacy-only compare,
  cursor clamp, over-bound output classified malformed + pinned by test, launch prompt input
  guard, doc comment placement).
- **Grader round 1 (fresh-context coder sub-agent, commits be53526+1ccb780):** DISPROOF
  SUCCEEDED with five defects. All fixed in the follow-up commit:
  1. Search results pinned list positions; a refresh re-sorted them under an open dialog and
     Enter could activate the wrong session (including a Build attach). Fixed: results store
     identity keys resolved at Enter time; vanished sessions yield a hint.
  2. `exec.Cmd.Wait` could block past the timeout when a grandchild held the pipes. Fixed:
     `WaitDelay = 3s`, explicit `exec.ErrWaitDelay` classification, regression tests
     (300s-grandchild case now returns in ~3.2s).
  3. Empty-string `run_id` passed validation and presented as attachable. Fixed: rejected as
     malformed in `validateSession`; `Attachable()` also requires a non-empty run id.
  4. Raw ANSI/control sequences in contract strings reached the terminal. Fixed:
     `SanitizeDisplay` strips control characters at every render boundary (titles, project
     labels, preview, launch dialog).
  5. Launch/resume response validation was laxer than list. Fixed: shared `validateSession`.
- **Grader round 2 (fresh-context coder sub-agent, commit 821b565):** DISPROOF SUCCEEDED,
  4 defects, all fixed in c099438: (1) `project_label`/`agent` unsanitized at card and
  compact render boundaries; (2) contract `error.message` rendered raw into transient and
  dialog errors; (3) stale Build preview survived record deletion; (4) contradictory
  `ok:true` + `error` envelope accepted.
- **Grader round 3 (fresh-context coder sub-agent, commit c099438):** DISPROOF SUCCEEDED,
  5 defects, all fixed in 3531af2: (1) C1 controls (8-bit CSI/OSC/DCS) and Unicode Cf
  format chars (bidi/zero-width) bypassed `SanitizeDisplay`; (2) late `buildProjectsMsg`
  error during an advanced launch dialog nil'd the slice and panicked the next render;
  (3) unbounded contract titles blew the fixed panel layout (200k chars → 5214 lines);
  (4) duplicate `conversation_id` produced non-unique identity keys shadowing records in
  search; (5) late project-list success silently re-pointed the launch cursor.
- **Grader round 4 (fresh-context coder sub-agent, commit 3531af2):** DISPROOF SUCCEEDED,
  3 defects, all fixed in 0c7fca3: (1) no fetch sequencing — a stale in-flight success
  undid a newer failure and resurrected actionable records (5s tick vs 10s timeout makes
  overlapping fetches routine); (2) resume response identity never checked against the
  request; (3) duplicate `(conversation_id, run_id)` pairs passed validation.
- **Grader round 5 (fresh-context coder sub-agent, commit 0c7fca3):** **DISPROOF FAILED —
  bar holds (pass 1 of 2).** 30 independent adversarial tests plus a 90s Go fuzz of the
  envelope decoder (21.7M execs, 0 failures); every prior fix re-verified; new attacks in
  gen-race, identity-bypass, hostile-namespace, and headless-program territory all held.
- **Grader round 6 (fresh-context coder sub-agent, commit 0c7fca3):** **DISPROOF FAILED —
  bar holds (pass 2 of 2, consecutive).** 22 independent tests incl. an end-to-end launch
  through a real fake executable and a live-binary PTY smoke (fake tmux + fake buildctl):
  attach suspended/repainted on child exit and child failure; full suite also passed
  under `-race`.

Two consecutive fresh-context graders failed to disprove the Goal 1B bar. Loop complete.

Residual observations reported by graders (all below the bar, deferred):
manual `r` refresh does not refetch Build (tick/actions do); `Version()`/`SupportsV1()`
unused (schema enforced per-envelope); legacy tmux names render unsanitized (pre-existing,
not contract data); preview truncation is byte-based on legacy pane content (pre-existing);
duplicate project ids not rejected (launch identity check bounds the risk).
