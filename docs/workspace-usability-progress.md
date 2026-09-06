# Workspace usability: implementation progress

Tracks the V1 specs under `docs/superpowers/specs/2026-09-05-*.md`. Each
acceptance item maps to code, tests, recorded results, and remaining gaps.
The verification log at the end records commands, environments and results.

## Baseline preserved

The working tree on 2026-09-05 already carried uncommitted work (host boxes
on the grid, packed tiles, grid numbering): `README.md`, `tui/app.go`,
`tui/grid.go`, `tui/grid_test.go`, `tui/hosts.go`, `tui/hosts_test.go`,
`tui/keyhints.go`, plus the four spec files. That diff (597 insertions, 80
deletions over HEAD `5c75c89`) was recorded before any edit and is
preserved in place; this work builds on top of it and reverts no hunk.
`go build ./... && go test ./...` passed on that tree before any edit.

## Test-host convention

Live SSH acceptance ran against the tailnet host `mini` (the only configured
remote host) **without touching its own tmux server**: a test-owned wrapper
`/tmp/cockpit-ssh-test/tmux` bound tmux to a private socket
(`-L cockpit-ssh-test -f /dev/null`), and a throwaway checkout lived at
`/tmp/cockpit-ssh-test/site`. Both were removed afterwards. The installed
remote binary (`~/.local/bin/cockpit`, `vbcf2899`) was not replaced, so the
remote hook could not be upgraded; the pane-record shape it will write was
exercised directly (see gaps). No explicitly designated test host was named
in the task; `mini` was used under that isolation and is called out here.

## Checkpoint 1: foundations

| Item | Code | Tests | Result |
|---|---|---|---|
| Reliable read outcomes: "no server" and "no session" are answers; any other failure is unknown | `sources/tmux.go` (`ErrNoServer`, `ErrNoSession`, `IsVerifiedAbsence`), `sources/processes.go` (`ListSessionsOn`, `sessionExists`, `ObserveProcesses`), `sources/ssh.go` | `TestListSessionsDistinguishesMissingTmuxFromNoServer`, `TestObserveProcessesReportsAFailedReadAsAnError`, `TestUnreadableWindowsBlockEveryMutation` | pass |
| Stable identity: session/window/pane ids plus server generation (`pid-start_time`) | `sources/tmux_args.go` (`windowFormat`, `sessionFormat`, `serverGeneration`), `sources/panes.go` | `TestParseSessionsReadsIdentityAndKeepsLegacyLines`, `TestParseWindowsReadsIdentityAndUnknownExitCode` | pass |
| Managed-window mark (`@cockpit_managed`), name collisions, split windows, ambiguity | `NewWindowArgs` (chained mark on `{end}`), `classifyProcesses`, `ProcessInfo.Controllable` | `TestObserveProcessesClassifiesOwnershipAndIntent`, `TestReconcileLeavesAnUnmarkedDeadWindowAlone`, `TestReconcileSkipsASplitManagedWindow` | pass |
| Stop override on the owning tmux session (`@cockpit_stopped_<name> = v1:<epoch>`) | `sources/stopintent.go` | `TestStopOverridesRoundTrip`, `TestReconcileSkipsAStoppedProcess` | pass |
| Cross-process project lock: tmux-side `if-shell -F` compare-and-set with expiry | `sources/lock.go` | `TestLockArgsCompareAndSet`, `TestLockGivesUpWhenAnotherClientHoldsIt`, `TestIntegrationLockIsExclusiveAcrossClients` (8 clients × 3 rounds, max 1 holder), `TestIntegrationExpiredLockIsTakenOver` | pass |
| Host-aware process service; bare labels never resolve remotely | `process/service.go` (`Resolve`, `RunnerFor`) | `TestResolveNeverCrossesHosts`, `TestRemoteProjectUsesRemoteRunnerOnly`, `TestUnreachableHostLaunchesNothing` | pass |
| Delayed / lost responses: superseded replies dropped, lost mutation responses unknown | `process/service.go` (`dispatched`), `tui/processes.go` (`apply`, `applyOutput` sequence guards) | `TestLostResponseAfterDispatchIsUnknownNotRetried`, `TestLateOutputCannotReplaceTheSelectedPreview` | pass |
| Pane records use pane-only option names (tmux resolves unset pane options through the session) | `sources/panes.go` (`@cockpit_pane_*`) | `TestIntegrationPaneRecordsDoNotInheritSessionStatus` (regression found on the real server) | pass |

## Checkpoint 2: process controls

Spec: `2026-09-05-process-controls-design.md`, acceptance 1–10.

| # | Item | Code | Tests / evidence | Result |
|---|---|---|---|---|
| 1 | Dormant project lists commands with zero launch/reconcile calls | `process.Service.Inspect`; TUI `openProcessesFor` | `TestIntegrationInspectDormantProjectLaunchesNothing`, `TestProcessesOpensFromGridWithoutLaunching`; MCP e2e `list_processes` on a dormant project (`session_exists False`) | pass |
| 2 | Start creates at most shell + command; repeated/concurrent TUI/MCP starts do not duplicate | `Service.Start` under `AcquireProjectLock` | `TestStartCreatesOnlyTheShellAndTheOneWindow`, `TestIntegrationStartCreatesShellAndOneWindow` (6 concurrent → 1), live SSH (4 concurrent → 1), MCP e2e (3 concurrent curl starts → 1 window) | pass |
| 3 | Stop survives re-entry and TUI/daemon restart; explicit Start resumes; siblings keep policy | `sources/stopintent.go`, `reconcileFrom`, `Service.Prepare` | `TestIntegrationStopSurvivesReentryAndRestart`, live SSH `TestLiveSSHLifecycleAndStopPersistence`, MCP e2e (TUI stop → MCP shows `desired_state: stopped` → daemon restarted → TUI project entry → still stopped, sibling started → MCP start resumes) | pass |
| 4 | Instant failure keeps output and exit code; exit 0 is Completed with no alert | `classifyProcesses`, `describeProcess`, `deriveHost` | `TestIntegrationExitCodesAndCompletion`, `TestProcessItemsSeparateCrashFromCompletionAndStop`; MCP e2e (`exit_code 7`, output `worker-crashed` readable, attention item present, cleared by restart) | pass |
| 5 | Stop/Restart by validated window id despite index/name changes; unmanaged and split windows protected | `Service.stop/restart` (`Controllable`) | `TestIntegrationStopTargetsIdentityDespiteRenamesAndMoves`, `TestIntegrationSplitAndLegacyWindowsAreProtected`, `TestStopRefusesSplitAndUnmanagedWindows`, `TestSplitAndUnmanagedRowsCannotBeStopped` (TUI) | pass |
| 6 | Legacy matching window needs explicit adoption; adoption launches nothing | `Service.Adopt`, `MarkManagedArgs`, TUI adopt dialog | `TestAdoptMarksWithoutLaunching`, integration (pane pid unchanged after adoption); MCP e2e stop of an unmanaged collision refused with the adoption message | pass |
| 7 | Remote project sharing a local label uses the remote runner; unreachable host never launches locally | `Service.Resolve` | `TestRemoteProjectUsesRemoteRunnerOnly`, `TestUnreachableHostLaunchesNothing`; live SSH: local private server stays empty during remote start; unreachable alias → every mutation `unavailable` | pass |
| 8 | Fault after stop-intent write / after remote dispatch → partial/unknown, no automatic retry | `Service.stop`, `dispatched` | `TestStopReportsPartialWhenKillFailsAfterIntent`, `TestStopDoesNotKillWhenIntentCannotBeRecorded`, `TestLostResponseAfterDispatchIsUnknownNotRetried`; live SSH reconnect does not duplicate | pass |
| 9 | Returning from attach/output restores project, selection and originating view; late output cannot replace the preview | `tui/nav.go` (return stack), `tui/processes.go` | `TestProcessesPanelAtFortyByTwelve` (Esc from output returns to the same row), `TestAttentionCrashItemOpensProcessesWithRowSelectedAndReturns`, `TestLateOutputCannotReplaceTheSelectedPreview`; manual TUI pass | pass |
| 10 | MCP and TUI share the service; legacy args valid; safety cases return clear errors | `daemon/agent.go` (`lifecycleResult`), `daemon/tools.go` | `daemon/agent_test.go`, `daemon/tools_test.go`; MCP e2e above | pass |

## Checkpoint 3: attention queue

Spec: `2026-09-05-attention-queue-design.md`, acceptance 1–10.

| # | Item | Code | Tests / evidence | Result |
|---|---|---|---|---|
| 1 | Two blocked panes → two items; a third pane clears neither; a late event cannot apply to a replacement | `sources/panes.go`, `daemon/hooks.go` (generation/session/window binding, `seq` ordering), `cmd/hook.go` (`TMUX_PANE` resolution, invocation = engine `session_id`) | `TestTwoBlockedPanesAreTwoItemsAndAThirdClearsNeither`, `TestStatusEndpointBindsAPaneReportToTheLivePane`, `TestStatusEndpointRefusesAReplacedOrMissingPane`, `TestStatusEndpointDropsALateReport`, `TestStatusEndpointStillAcceptsLegacyTargets` | pass |
| 2 | Same label on two hosts → distinct items and routes | `DeriveAttention` | `TestSameLabelOnTwoHostsIsTwoItems` | pass |
| 3 | Enter reaches the reporting pane despite an external active-pane switch | `sources/attach.go` (`SelectTargetArgs`) | `TestIntegrationAttachSurvivesRenameAndRefusesReplacement`; live SSH `TestLiveSSHBlockedRemotePaneIsQueuedAndAttachValidates` (another window made active remotely; the target pane is selected) | pass |
| 4 | Rename/remove/replace between poll and action → same identity or visible failure without creation | `ValidateTarget`, `AttachRemote` (attach-session, never `new-session -A`) | same tests; `TestAttachRemoteNeverCreatesTheRemoteSession`; live SSH (killed pane → `ErrTargetGone`, remote session count unchanged) | pass |
| 5 | Crash item preserves the dead pane; no start/restart during inspection or refresh | `Service.Inspect`, `attentionAction` | manual TUI pass (dead window `@3 crash dead=1` survives open/inspect/refresh); `TestAttentionToolReturnsRecordsAndCoverage` (no mutating verbs) | pass |
| 6 | No duplicates across polls; selection stable as urgent rows arrive; resolved row cannot redirect a pending action | `AttentionTracker`, `attentionModel` (selection by id, placeholder) | `TestTrackerKeepsFirstObservedAndSortsStably`, `TestAttentionSelectionSurvivesRefreshAndNewUrgentRows`, `TestResolvedRowBlocksAQueuedEnter` | pass |
| 7 | SSH/tmux/GitHub read failures cannot clear items or show passing; reconnect reconciles against fresh evidence | `DeriveAttention` (stale retention, coverage), `sources/github.go` (`Coverage`, `Err`) | `TestUnavailableHostKeepsLastKnownItemsAsStale`, `TestGitHubReadFailureIsUnavailableNotPassing`, `TestAttentionShowsCoverageAndNeverClaimsHealthy`; live SSH unreachable → stale retention, reconnect → `already_running` | pass |
| 8 | CI details identify the checked branch and exact run; remote unsupported and legacy hook coverage visible | `github.go` (`RunID`, `RunURL`, `Branch`), `deriveGitHub`, `deriveHost` | `TestCIItemsCarryRunIdentityAndBranchCoverage`, `TestLegacySessionReportIsLimitedCoverage`; `o` opener restricted by `TestRunURLOpenerOnlyAcceptsGitHubHTTPS` | pass |
| 9 | TUI and MCP derive equivalent records | both call `sources.DeriveAttention` over the same `HostReport` shape (`sources/collect.go`, `tui/panels.go`, `daemon/tools.go`) | `TestTUIAndMCPDeriveEquivalentRecords`, `TestAttentionToolReturnsRecordsAndCoverage`, `TestSignalsAdaptTheSameFacts` | pass |
| 10 | 40×12 usability; control sequences in names/details cannot affect the TUI | `sources.StripControl`, narrow two-line rows, detail view | `TestAttentionNarrowLayoutKeepsActionVisible`, `TestProcessesPanelAtFortyByTwelve`, `TestControlSequencesInNamesCannotReachTheTerminal`; manual 40×12 pass | pass |

`cockpit_attention` returns `schema_version: 1`, items, coverage, and
counts; `cockpit_signals` keeps its envelope (`TestAttentionToolReturnsRecordsAndCoverage`).

## Checkpoint 4: doctor and guided setup

Spec: `2026-09-05-doctor-setup-design.md`, acceptance 1–10.

| # | Item | Code | Tests / evidence | Result |
|---|---|---|---|---|
| 1 | Fresh install: core ready, useful next steps, no files or processes created | `doctor/doctor.go` | `TestFreshInstallIsCoreReadyAndTouchesNothing` (spy forbids every mutating verb); Linux and macOS fresh-install runs (exit 0, "Core ready") | pass |
| 2 | Missing/malformed config → complete partial report, exit 1; JSON parses; usage → 2; warnings alone → 0 | `doctor.Run`, `cmd/doctor.go` (`exitError`) | `TestMissingConfigStillChecksTmuxAndSkipsDependents`, `TestMalformedConfigReportsParseErrorAndUnknownKeysWarn`; CLI runs: missing repo → 1, `--host x --all-hosts` → 2, unknown host → 2, warnings only → 0, `--json` parsed by Python | pass |
| 3 | Thin PATH, missing remote binary, unreadable socket, unknown host key are distinct with correct next actions and no wait | `checkBinary`, `checkTmux`, `remote.go` (batch mode, `ControlMaster=no`) | `TestUnreadableSocketAndProtocolErrorsAreDistinct`, `TestUnknownHostKeyIsADistinctFailure`, `TestMissingRemoteBinaryIsNamed`, `TestMissingConfigStillChecksTmuxAndSkipsDependents` | pass |
| 4 | A port serving something else, or Cockpit on another config, does not pass | `checkDaemon` (JSON-RPC `cockpit_whoami`) | `TestDaemonIdentityIsVerifiedNotJustThePort` | pass |
| 5 | Hooks present but untrusted, old, unverified or never observed are distinct; no call grants trust | `checkClaudeHooks`, `checkCodexHooks` (per-entry `hooks.state` keys), `checkHookFreshness` | `TestHookStatesAreDistinct` (no `codex` spawn); real run showed session-level delivery as limited precision | pass |
| 6 | Symlinked repos and worktrees pass; broken links and loops bounded | `checkRepo` (`EvalSymlinks`, `rev-parse --git-dir`) | `TestReposFollowSymlinksAndNameBrokenOnes` | pass |
| 7 | One slow host cannot block local results; deadlines and cancellation give explicit incomplete coverage | `timed`, `incomplete`, `checkHosts` (3 parallel) | `TestSlowHostCannotBlockLocalResults` (`Incomplete` lists the host, `ControlMaster=no` on every ssh) | pass |
| 8 | Doctor attempts no application-state writes, session creation, launch, command execution or repair | spy `Deps` | `TestFreshInstallIsCoreReadyAndTouchesNothing`; real `doctor --host mini` created no control socket in `~/.config/cockpit` | pass |
| 9 | Wizard cancel, repeated init, concurrent edits and safe-merge failure preserve config; saved minimal config loads | `setup/`, `cmd/init.go` | `TestWizardCancelWritesNothing`, `TestWizardMergesIntoExistingConfigAndKeepsComments`, `TestWizardDetectsAConcurrentEditAndReviewsAgain`, `TestWizardRefusesUnsafeMergeAndLeavesFileIntact`, `TestPlainInitOnExistingFilePointsToInteractiveReview`, `TestMinimalConfigLoadsAndDisablesOptionalIntegrations`, `TestWriteAtomicRefusesConcurrentEditsAndBacksUp`; real TTY run in a private tmux session (scan, symlink add, toggle, label edit, diff preview, save, doctor) | pass |
| 10 | Clean macOS/Linux install from the documented release path reaches a dashboard without Go; README/module/version reconciled | `scripts/install.sh` (`COCKPIT_RELEASE_URL` for candidate artifacts), `go.mod` module renamed to `github.com/jeffdhooton/cockpit`, README install section | Linux (alpine 3.20 container, no Go) and macOS (PATH without Go) installs from candidate `cockpit_0.3.0-rc1_*` archives: checksum verified, `cockpit version` → `v0.3.0-rc1`, `init`, `doctor` exit 0, dashboard and attention view rendered inside tmux | pass |

Discovery bounds (`MaxDepth 2`, `MaxCandidates 200`, skipped dependency
directories, no traversal of symlinked directories, distinct worktrees kept)
are covered by `TestScanIsBoundedAndSkipsSymlinkedDirs`,
`TestScanStopsAtTheCandidateCap`, `TestDedupeKeepsDistinctWorktrees`.

## Package acceptance (workspace-usability.md)

1. Blocked agent on a remote project in the global queue, opened to the
   correct existing pane without launching: live SSH
   `TestLiveSSHBlockedRemotePaneIsQueuedAndAttachValidates` (remote pane
   record → one actionable item with host `mini` → remote window/pane
   selected, local view window attaches with `attach-session`, no remote
   session created). The final `switch-client` cannot complete on a headless
   private server; everything before it is asserted.
2. Crashed configured process opens in the process panel with readable
   output: manual TUI pass (desktop and 40×12) and MCP e2e.
3. Manually stopped auto-start process stays stopped across navigation and
   TUI/daemon restart while tmux survives: MCP/TUI e2e and live SSH.
4. Dropped SSH → unavailable, mutations refused; reconnect does not
   duplicate or erase: live SSH
   `TestLiveSSHUnreachableHostPreventsMutationAndReconnectDoesNotDuplicate`.
5. New user: minimal config, session discovery, actionable diagnostics
   without optional integrations: wizard TTY run, fresh installs.

## Gaps and notes

- **Remote hook upgrade not exercised end to end.** The remote binary on
  `mini` was not replaced (installed binaries are out of bounds), so its hook
  still reports at session level; the queue shows that as limited coverage
  and doctor flags it. The pane-record shape was written directly on the
  remote private server to exercise derivation and attach.
- **Attach-existing `switch-client` on a headless private server** cannot
  succeed (no attached client); the manual TUI pass and the live SSH test
  verify every step before it. The same `switch-client` call is the one the
  existing jump path has always used.
- **Codex trust currency** is reported as recorded-but-unverifiable without
  spawning Codex, as the spec allows.
- **Session `LastUsed` for never-attached sessions** still renders a very
  large idle age on the grid (pre-existing behaviour, untouched).
- **Diff size.** This is a large change by design of the task; the
  pre-existing uncommitted work is preserved and nothing is committed.

## Verification log

Environment: macOS 25.4 (darwin/arm64), Go 1.26.2, tmux 3.6a locally;
`mini` (darwin/arm64, tmux 3.7c, cockpit `vbcf2899`) over the system ssh;
Docker (OrbStack) `alpine:3.20` for the Linux install.

- Baseline, before any edit: `go build ./... && go test ./...` → pass.
- Unit and private-server integration: `go vet ./... && go build ./... &&
  go test ./...` → all packages pass (`cmd`, `config`, `daemon`, `doctor`,
  `process`, `setup`, `sources`, `tui`). Integration tests use
  `tmux -L cockpit-*-<pid> -f /dev/null`, killed on cleanup; stale socket
  files were removed afterwards.
- Live SSH: `COCKPIT_SSH_TEST_HOST=mini COCKPIT_SSH_TEST_TMUX=/tmp/cockpit-ssh-test/tmux
  COCKPIT_SSH_TEST_REPO=/tmp/cockpit-ssh-test/site go test ./process/ -run
  LiveSSH -v -count=1` → 3/3 pass (9.3s). Test resources on `mini` removed;
  its own server still showed its 10 sessions afterwards.
- Doctor CLI (isolated configs in the scratchpad): plain → exit 1 with a
  missing repo named; `--host a --all-hosts` → 2; `--host nosuch` → 2;
  missing config → partial report, exit 1; warnings only → 0; `--json` →
  `schema_version 1`, parsed; `--host mini` → ssh/tmux/repo checks plus
  "remote cockpit predates doctor", 1.2s, no control socket created.
- Manual TUI: `scratchpad/tui.sh 120 40` and `tui-narrow.sh 40 12` on a
  private server with an isolated config; screens captured for grid,
  attention, second-row selection, process panel via a crash item, full
  output, stop confirmation, and the return path.
- MCP/TUI e2e on a private server (daemon on port 45999, TUI in the same
  server): dormant inspection, MCP start, 3 concurrent starts → 1 window,
  TUI stop confirmed → MCP `desired_state: stopped`, daemon restart + TUI
  project entry → still stopped, MCP start resumes, crash → `cockpit_attention`
  item with `exit_code 7` and readable output, restart clears it, MCP stop
  honoured by TUI entry (`@cockpit_stopped_worker` present, no worker window).
- Wizard on a real TTY inside a private tmux session: scan, symlinked add,
  toggle, label edit, diff preview, save, doctor and next steps; saved
  config reloaded by doctor. The symlink-add path revealed a dedupe bug
  (selection dropped), fixed with `TestDedupeKeepsDistinctWorktrees`.
- Fresh installs from candidate artifacts (`CGO_ENABLED=0 go build -trimpath
  -ldflags "-s -w -X main.version=0.3.0-rc1"`, GoReleaser archive naming and
  checksum file, served locally with `COCKPIT_RELEASE_URL`): Linux alpine
  container and macOS with Go absent from PATH — checksum verified,
  `cockpit v0.3.0-rc1`, `init`, `doctor` exit 0, dashboard and attention
  view rendered inside tmux. GoReleaser itself is not installed here; the
  archives follow `.goreleaser.yaml` naming and contents.

## Session view (spec `2026-09-05-session-view-design.md`)

Implemented per `docs/superpowers/plans/2026-09-05-session-view.md`. The
dashboard (Sessions strip, Projects table, Today, Notes, visualizers) and
the pane-hash status guess are removed; `c` capture stays as a one-line
prompt in every view.

| # | Item | Code | Tests / evidence | Result |
|---|---|---|---|---|
| 1 | Same-named sessions on two hosts are distinct with their own attach targets | `sources/workspace.go` (`SessionView.Key`), `tui/sessionsview.go` (`attachSession`) | `TestWorkspaceSortsByRollupAndKeepsHostsApart` | pass |
| 2 | Agent by record with reported status; `claude` without a record is an agent with unknown status; `zsh` is a shell; needs-input never inferred | `buildPane`, `agentCommands`, `shellCommands` | `TestWorkspaceClassifiesPanes`, `TestWorkspaceNeverInfersNeedsInput`, `TestIntegrationWorkspaceClassifiesARealAgentCommand` (real binary named `claude` on a private server) | pass |
| 3 | Managed process pane shows the process panel's text and exit code; unmanaged marked | `buildHost` (`procByWindow`) | `TestWorkspaceClassifiesPanes`, manual pass (`crash  Exited (code 1)  process`) | pass |
| 4 | A session that starts needing input rises; the cursor stays on its session | sort in `buildHost`; `sessionsModel.resolve` by key | `TestSessionsSelectionSurvivesReorder` | pass |
| 5 | Late preview for another pane or an older sequence is dropped | `sessionsModel.applyPreview` | `TestSessionsLatePreviewIsDropped` | pass |
| 6 | A host that stops answering keeps its sessions as stale with previews and attach disabled; coverage names it | `buildHost` (stale), `fetchPanePreview` (skips non-fresh hosts) | `TestWorkspaceKeepsStaleHostAndListsDormant`, manual pass (`UNREACHABLE-T… ⚠ unavailable`) | pass |
| 7 | Enter never creates or runs; `o` does, for the selected project only | `handleSessionsKey`, `openProjectCmd` | `TestSessionsEnterNeverLaunches`, `TestSessionsOpenProjectIsExplicit` | pass |
| 8 | `cockpit_workspaces` equals the TUI's tree, schema 1, read-only | `daemon/tools.go` (`workspaces`, `collectInput`) | `TestWorkspacesToolReturnsTheTreeReadOnly`, `TestReadOutputAcceptsAPaneID` | pass |
| 9 | 40×12 and 90-column layouts; control sequences stripped | `sessionsModel.view`, ANSI-aware `clip` | `TestSessionsViewBreakpoints`, `TestSessionsViewStripsControlSequences`; manual passes at 120×36 and 40×12 | pass |
| 10 | No dead keys; `default_view = "dashboard"` still starts | `config.applyDefaults` alias; `tui/keyhints.go` | `TestDefaultViewAcceptsSessionsAndAliasesDashboard`, `TestSessionsIsTheDefaultViewAndGridIsG`, `TestCaptureModeEnterExit` | pass |

Verification: `go vet ./... && go build ./... && go test -count=1 ./...`
pass. Manual passes ran the built binary inside private tmux servers
(`cockpit-sess-wide` at 120×36, `cockpit-sess-narrow` at 40×12) with a
fake agent binary named `claude`, a pane record marked `needs_input`, and a
crashed managed window; screens captured for the default view, Tab+j pane
selection with preview, `a` and Esc back to the same pane, `p`, `g`, `d`,
`/site`, and the capture prompt. A rendering bug found on the real server
(non-ANSI-aware truncation mangling styled rows) was fixed by truncating
with `charmbracelet/x/ansi`, now a direct dependency.
