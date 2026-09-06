# Doctor and guided setup

Date: 2026-09-05  
Status: proposed  
Parent: [Workspace usability](2026-09-05-workspace-usability.md)

## Problem and success

A blank dashboard, silent hook, or missing remote binary can look like "nothing
is running." New users also need a useful starting config without knowing the
author's directory layout or optional integrations. Doctor explains what is
working, what is unavailable, and the smallest next action. Guided setup helps
create a config; it is a separate, explicitly chosen operation.

## Command contract

| Command | Behavior |
|---|---|
| `cockpit doctor` | Diagnose local core and enabled local integrations. Report configured remote hosts as not probed and show how to check them. |
| `cockpit doctor --host NAME` | Diagnose local prerequisites and one configured SSH host, including its configured repositories. |
| `cockpit doctor --all-hosts` | Diagnose local and all configured hosts with bounded concurrency. |
| `cockpit doctor --json` | Same checks with one versioned JSON document on stdout; combinable with host flags. |
| `cockpit init` | Keep noninteractive config creation, using a minimal useful template. |
| `cockpit init --interactive` | Guide selection of local projects and review/write a minimal config. |

All commands honor global `--config`. `--host` and `--all-hosts` are mutually
exclusive; unknown host names are usage errors. Doctor works outside tmux and
with missing or malformed config, reporting checks that remain possible and
skipping dependent checks. It never bootstraps the dashboard.

Doctor does not install binaries, start/restart services, edit files, change
hook trust, launch agents, create tmux sessions, run configured project commands,
or authenticate interactively. V1 has no `--fix`. Checks can make bounded
read-only requests to enabled integrations; unrelated product/network probes
are out of scope.

## Output and severity

```text
Cockpit doctor

PASS  Config       /home/alex/.config/cockpit/config.toml
PASS  tmux         Found; 4 sessions visible
PASS  Projects     3 readable repositories
WARN  Agent status Daemon is not responding; hook status unavailable
      Next: cockpit daemon start
SKIP  GitHub       Disabled
SKIP  Obsidian     Not configured
SKIP  Remote hosts 1 configured; not checked
      Next: cockpit doctor --all-hosts

Core ready · 1 optional feature needs attention · 1 host unchecked
```

Each check has a stable ID, scope, `pass|warn|fail|skip` status, concise summary,
bounded evidence, duration, and remediation text/optional suggested argv.
Commands are displayed only, never executed by selecting a result.

- **Fail:** core functionality cannot work in the requested scope: missing
  tmux, invalid config, unreadable configured repository, failed explicitly
  requested host connection, or remote tmux unavailable.
- **Warn:** an enabled optional capability is degraded, or a check cannot
  establish optional readiness. Examples: inactive daemon, missing GitHub auth,
  stale hook integration, unavailable optional vault file.
- **Skip:** disabled/not installed optional integration, deliberately unprobed
  host, or dependency failure. State which dependency prevented the check.
- No tmux server/sessions is a valid empty state, not a failure.
- No configured repos is valid: existing tmux sessions still make Cockpit useful.

Exit 0 means no failures (warnings are allowed), 1 means diagnostic failures,
and 2 means invalid CLI usage. Implement this explicitly rather than inheriting
the root command's blanket exit-1 behavior. JSON includes `schema_version: 1`,
Cockpit version, config path, selected scopes, checks, counts, and overall core
readiness. JSON goes to stdout without ANSI or progress noise; unexpected
internal failures produce a structured failed check when possible and exit 1.

## Checks

| Area | Check and interpretation |
|---|---|
| Config | Existence, TOML syntax, schema validation, unknown keys, positive intervals, valid port and host references, duplicate labels/commands, valid regexes. Unknown keys warn with their paths; existing accepted configs are not rewritten. |
| Binary context | Running Cockpit version/path and local tmux resolution; detect a launcher pointing at a missing/different binary when its metadata is readable. Report only the relevant path, not the entire environment. |
| tmux visibility | Can the selected server be queried? Distinguish no server from permission/protocol/binary errors. Compare TUI/daemon server scope when both are discoverable. V1 diagnoses mismatches; it does not add general multi-server management. |
| Repositories | Follow symlinks, confirm the resulting directory is readable and Git recognizes the checkout/worktree. Accept worktree `.git` files. Broken symlinks and missing configured directories name the affected project. Never fetch or modify Git data. |
| Process configuration | Validate names, commands, working directories and regexes. Detect managed-window/name conflicts when a server is available. Do not execute scripts or pretend to fully validate shell commands by parsing their first word. |
| Daemon | Request identity with a bounded read-only protocol call; verify Cockpit, version, effective config path, and tmux scope. An open TCP port alone is not success. A different service/config is reported explicitly. |
| Hooks | Check only installed engines' Cockpit hooks: command path, enabled expected entries, target format/version, effective config binding and trust evidence where determinable. Never approve hooks or spawn an agent to test them. |
| Hook freshness | A recent real report establishes delivery for its scope. Missing reports mean "delivery not yet observed," not "broken" or "live." Legacy session reporting is labeled limited precision. |
| GitHub | If enabled, check CLI presence, current authentication and bounded access to configured local GitHub repos. Do not display auth tokens or raw auth output. Remote CI remains unsupported and is reported as such. |
| Obsidian | Only configured paths: existence/readability and permission metadata for expected writes. No temporary write probe; say actual write success is untested where relevant. Absence of this integration is healthy. |
| Hermes | Only configured endpoints: bounded existing status request and parse. No credentials or automatic gateway restart. |
| Selected SSH hosts | Existing noninteractive OpenSSH access, remote tmux path/query, remote configured paths, and optional remote Cockpit diagnostic capability. No host-key acceptance, installation or login prompt. |

Config parse failure must not hide a missing local tmux binary: run independent
checks and explicitly skip those needing parsed config. Likewise one failed
repository/host must not abort all remaining independent checks.

Read hook trust only if exact current configuration can be matched to trust
evidence. The existing helper that recognizes any trusted hash is insufficient
to prove all Cockpit hooks are trusted. If an engine offers no reliable
side-effect-free check in the supported version, report "Trust unverified" and
give the existing manual verification route. Current provider behavior must be
verified during implementation; doctor does not assume old design docs define
the provider's current contract.

## Remote execution and time bounds

Use system SSH and the established host configuration. Do not introduce a new
SSH library or alter user authentication/host-key policy. The doctor path must
avoid creating persistent control sockets in the user's Cockpit config just
to diagnose a missing installation. Use a transient bounded connection; an
already-configured OpenSSH transport may still produce its normal system logs.

If remote Cockpit supports doctor JSON, collect that report with remote-host
recursion disabled (plain remote doctor does not traverse its hosts). Validate
its schema and label remote version mismatch. Otherwise perform basic remote
tmux/path checks and mark hooks/daemon details unverified. Remote Cockpit is not
a requirement for session visibility or process control.

Proposed budgets: 2 seconds for a simple local subprocess/HTTP check, 5 seconds
for SSH connection establishment, 10 seconds per host or integration request,
and a 30-second total run deadline with at most three concurrent host checks.
Deadline-expired selected core checks fail as timed out; optional checks warn.
The report remains partial and explicitly names uncompleted checks. Interactive
progress may go to stderr; Ctrl+C cancels child work promptly.

Evidence must not dump hook configs, environment maps, private keys, token
files, prompts, command output, or full HTTP responses. Report the relevant
field/path and a bounded sanitized error. Paths may identify users, so doctor
does not claim its JSON is anonymized.

## Guided initial setup

`cockpit init --interactive` requires a TTY. With redirected input it exits with
usage guidance and writes nothing. Plain `init` remains scriptable.

1. Explain that Cockpit uses existing tmux and that optional integrations can be
   added later. Report missing core dependencies without attempting installation.
2. Discover local sessions on the current server and offer their existing Git
   roots as unchecked candidates. Do not treat every pane directory as a repo.
3. Let the user add a path or select a parent directory to scan. Scan at most
   two directory levels and 200 candidates, skip dependency/cache directories,
   and do not recursively crawl home. Resolve explicitly selected symlinks;
   avoid symlink loops and do not recursively traverse symlinked directories.
   Deduplicate canonical roots while retaining distinct Git worktrees.
4. Show editable labels and paths. Validate uniqueness and reserved collisions.
   Session discovery is useful even if the user saves no repository entries.
5. Show the destination and exact minimal config diff, then choose Save or
   Cancel. Existing files are never overwritten silently. Merge selected
   entries while preserving unrelated settings/comments; if safe merging cannot
   be established, show the proposed snippet and leave the file unchanged.
6. Write atomically with a backup when modifying an existing file. Recheck its
   content fingerprint before writing; if it changed during the wizard, return
   to review with the new diff. Cancellation leaves the original intact.
7. Run the diagnostic collector and show next steps. Starting the dashboard,
   daemon, or hook installation remains a separately invoked existing command.

The minimal template has the grid, sensible refresh defaults, and selected
repos. It contains no active example vault/Hermes paths, no process commands,
and no mandatory GitHub or daemon integration. Write explicit `enabled = false`
for `[github]` and `[daemon]` in newly created minimal configs so the existing
default-enabled daemon behavior cannot create misleading setup warnings.
Other optional sections are commented examples or omitted with documented
defaults. Existing config values are preserved. Plain `init` on an existing file
keeps its current non-destructive behavior and directs the user to interactive
review; it must not suggest deletion as the only way forward.

Config creation does not install global agent hooks, trust them, change SSH
configuration, register MCP servers, or add login services. Those are existing
explicit setup actions, each described with its effect. There is no need to
force an optional integration decision to finish the wizard.

From the TUI, a help/diagnostics action can display a read-only report using the
same collector after the CLI ships. It is not a prerequisite for V1.

## Architecture and acceptance

Use a diagnostic collector independent of command printing/TUI rendering, with
injectable filesystem, command, network and clock dependencies. Reuse config
validation and host routing, but not helpers that create state or swallow read
errors. Remediations are data, never shell commands executed by the collector.

Acceptance scenarios:

1. Fresh installation with tmux, no sessions/repos/optional integrations: core
   ready and useful next steps; no files or processes created by doctor.
2. Missing and malformed configs yield complete partial reports and exit 1.
   JSON parses cleanly; usage mistakes exit 2; optional warnings alone exit 0.
3. Thin PATH, missing remote binary, unreadable tmux socket and unknown SSH host
   key are distinct failures with correct next actions and no interactive wait.
4. A port serving something other than Cockpit, or Cockpit using another config,
   does not pass the daemon identity check.
5. Hooks present but untrusted, old, unverified or never observed are distinct
   from recently delivered hook evidence. No diagnostic call grants trust.
6. Valid symlinked repos and worktrees pass; broken links and loops are bounded.
7. One slow host cannot prevent local results; total deadlines and cancellation
   terminate subprocesses and return explicit incomplete coverage.
8. With filesystem/process spies, doctor attempts no application-state writes,
   session creation, service launch, configured command execution or repair.
9. Wizard cancel, repeated init, concurrent config edits and safe-merge failure
   preserve existing configuration. Saved minimal config loads successfully.
10. A clean macOS/Linux install using the documented release path reaches a
    dashboard without Go installed. Reconcile README/module/version references
    and verify the shipped artifact rather than relying on a developer checkout.

## Deferred

Automatic repair, package-manager installation, interactive SSH authentication,
remote provisioning, hook test-event injection, recursive project discovery,
mandatory telemetry, and a configuration editor for every optional integration.
