# Spine card acceptance checks

Run commands in `manifest.yaml` from the repository root. Each command exits
zero only on success. No checks require production; none use `after_release`.

The repository uses `go test`, with Bubble Tea keyboard messages and rendered
views for UI tests. It has no standalone UI drive tool. These checks use that
same UI surface through the public root model and a real Bubble Tea event loop,
and run the actual `cockpit doctor --json` executable. They never inspect model
fields, inject implementation-specific messages, inspect source files, or
require a new source API or test seam from the builders.

Fake executables on an isolated PATH emulate the subprocess boundary. They
serve the supplied `testdata/spine/bearings.json`, record commands, emulate
tmux's public format strings and session existence, and execute console shell
commands against fake spine and a fake final shell. The UI receives source
results through its ordinary startup, refresh timer and `r` key. Snapshot
timestamps shift together to preserve the sample ages on future runs. Tests
also vary asks, goals, spend, agents, empty sections and source failures.

The runner disables Go module downloads. Dependencies must already be available
in the repository's usual module cache. Temporary worlds, binaries and Go cache
are runtime artifacts; all authored files are here. The checks live in a
`testdata` directory so the ordinary `go test ./...` gate does not recursively
run these independently invoked acceptance checks.

The quality command runs exactly `go build ./...`, `go vet ./...`, and
`go test ./...`. Its PATH excludes spine, tmux and gh. Existing real-tmux
integration tests use their existing "not installed" skip, while the remaining
repository tests run normally, including their loopback HTTP servers. The
feature checks use only fakes for all three tools.

## Baseline on main

All six feature commands exit 1 on main. Every feature subcase fails: spine
is not polled, the fleet tile/preview/asks are absent, and doctor has no spine
diagnostics. The existing-session entry case finds main's ordinary tmux session
tile, but correctly fails because it has no fleet preview or console entry.

The harness control passes: it loads the existing session tile and opens the
existing attention view using keyboard input. The fake-console control checks
that the tmux boundary actually executes the fallback and final shell. Both
controls passed 50 repeated runs after fixing a temporary-directory cleanup race.

Outcome 7 is marked `already_true: true` because the returned check review
confirms that its gate already passes on main. Its requirement is the existing
build, vet and test gate, so it remains a regression check; it must not be
made to fail by adding an unrelated feature assertion.

On this revision, build and vet passed before the combined command reached
the test suite. Existing tests in cmd, daemon and sources failed because this
sandbox refuses loopback listeners (`bind: operation not permitted`). The
combined command therefore exited 1 here; its expected baseline pass could
not be independently verified in this sandbox. That environment failure is
not evidence that outcome 7 needs implementation. Run this gate where the
repository's existing tests can bind. No checks skip or suppress those
failures, and no outcome uses `after_release`.

No commits were made.
