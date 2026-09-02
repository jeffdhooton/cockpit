# Hermes Tile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One grid tile showing whether the Hermes gateway is running and which platforms are connected, read from its dashboard's unauthenticated status endpoint.

**Architecture:** A new `sources.HermesStatus` source polled on its own interval; a `Target` variant carrying it; a signal when the gateway is down while reachable.

**Tech Stack:** Go `net/http`, `encoding/json`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-01-hermes-tile-design.md`

## Global Constraints

- 5-second timeout, 64KB body cap, decode only the fields named in the spec.
- No token, no writes, no Enter action.
- Unreachable ≠ stopped: different render, and only stopped signals.

---

### Task 1: Config

**Files:** `config/config.go`, `config/config_test.go`

- `type HermesConfig struct { Label, URL string; RefreshInterval int }`, `Config.Hermes []HermesConfig` (`toml:"hermes"`), default interval 30.
- [ ] Failing tests: bad URL rejected; `ftp://` rejected; label validated; default interval applied.
- [ ] Implement `validateHermes`. Commit: `Add hermes config`.

### Task 2: Source

**Files:** `sources/hermes.go`, `sources/hermes_test.go`

- `type HermesStatus struct { Label string; Reachable bool; Gateway string; Platforms []string; Version string; Err error }`
- `func GetHermesStatus(ctx, client *http.Client, cfg config.HermesConfig) HermesStatus`
- [ ] Failing tests with `httptest.Server`: the real fixture from the probe → `Gateway: "running"`, `Platforms: ["photon","slack"]`, `Version: "0.16.0"`; `gateway_running:false` → `"stopped"`; 401 → unreachable with `Err` naming 401; connection refused → unreachable; a 1MB body is cut at 64KB without hanging.
- [ ] Implement. Commit: `Read hermes gateway status`.

### Task 3: Tile and targets

**Files:** `tui/grid.go`, `tui/grid_test.go`, `tui/app.go`

- `Target.Hermes *sources.HermesStatus`; `BuildTargets` gains a `hermes []HermesStatus` parameter appended after running sessions.
- [ ] Failing tests: running renders `● gateway` and the platforms; stopped renders in warning; unreachable renders `⚠ unreachable`; a Hermes target sorts after running sessions and before dormant repos; Enter on it is a no-op (`m.jump` returns nil cmd).
- [ ] Implement, plus `fetchHermes()` on its own `tea.Tick` at `RefreshInterval`. Commit: `Show the hermes gateway on the grid`.

### Task 4: Signal

**Files:** `sources/signals.go`, `sources/signals_test.go`

- `SignalInput.Hermes []HermesStatus`; `SignalHermesDown` after blocked agents, before dead processes.
- [ ] Failing tests: stopped-and-reachable signals; unreachable does not.
- [ ] Implement. Commit: `Signal a stopped hermes gateway`.

### Task 5: Live and docs

- [ ] Add `[[hermes]]` to the live config with `http://100.96.45.73:9119`; confirm the tile.
- [ ] README paragraph under the remote hosts section. Commit: `Document the hermes tile`.
