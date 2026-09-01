# Unified Target Grid

**Date:** 2026-09-01
**Status:** Approved

## Problem

Cockpit's dashboard renders five panels: Sessions, Projects, Today, Notes, and a
Visualizer. In practice only two get used — the running tmux sessions and the
saved repos. The other three consume roughly 55% of the vertical budget.

The dashboard also refuses to render below 60 columns. Attaching from Termius on
a phone, where the pane is roughly 40–55 columns wide, produces only a "Terminal
too narrow" message. There is no way to see or reach sessions from a phone.

## Goals

1. Make the two things actually used — running sessions and saved repos — the
   default view, as a single navigable grid.
2. Make that grid work at phone width, so attaching from Termius gives a usable
   session switcher.
3. Preserve the existing dashboard and every panel in it. Nothing is deleted.

## Non-goals

- Deleting the Today, Notes, or Visualizer panels, or the Obsidian source.
- A separate mobile binary, subcommand, or tmux session.
- Changing how sessions are created, saved, or searched.

## Design

### Target: the unit the grid renders

A `Target` is one tile. It is either a running tmux session, a saved repo with no
session, or both joined together.

```go
type Target struct {
    Label   string                 // tile name; session name or repo label
    Session *sources.TmuxSession   // nil ⇒ dormant repo
    Repo    *sources.GitRepoStatus // nil ⇒ session with no configured repo
    Status  sources.ClaudeStatus   // from the existing pane-hash diff
}

func (t Target) Running() bool { return t.Session != nil }
```

`BuildTargets(sessions []sources.TmuxSession, repos []sources.GitRepoStatus,
statuses map[string]sources.ClaudeStatus, selfSession string) []Target` is a pure
function — no I/O, no model state — and carries the bulk of the test surface.

**Join key.** `session.Name == repo.Label`. This is the identity `tmuxJump`
already assumes when it switches to a session named after a repo label, so the
grid inherits the existing contract rather than inventing a second one.

**Filtering.** The session named `cfg.General.SessionName` (cockpit itself) is
excluded, matching the current `tmuxDataMsg` handler.

**Ordering.** Running targets first, then dormant, alphabetical by label within
each group. Alphabetical rather than last-used is deliberate: last-used reorders
tiles on every 5-second refresh, moving the tile under the cursor while the user
is aiming at it. A target crossing idle→working changes its dot, never its
position. A session starting or dying is the only thing that reorders the grid,
and the cursor rule below absorbs that.

### Cursor identity

The cursor is stored on the model as a **label**, not an index:

```go
gridCursor string // label of the selected target
```

It resolves to a position at render time. If the label is gone — session died,
repo removed from config — the cursor falls back to the nearest surviving index,
clamped. Storing an index instead would teleport the selection whenever a session
appears or disappears above it, which on a 5-second refresh cycle is a live
hazard rather than a theoretical one.

### Grid layout

```
cols = clamp(width / 22, 1, 4)
```

22 columns per cell = 18 content + 2 border + 2 gap. This yields 2 columns at
Termius width (~44), 1 column on a very narrow pane, and 4 on a wide desktop.
The cap of 4 is deliberate: an 8-wide grid on a 200-column terminal produces
tiles too sparse to scan, and the remaining width is better spent on the preview.

Each tile is 3 content lines inside the existing rounded border:

```
┌────────────┐
│ my-app     │  label, truncated with …
│ ● working  │  status: working / idle / attached / detached / no session
│ feat/auth ✗│  branch + dirty marker + unpushed, or blank when Repo == nil
└────────────┘
```

Tiles reuse `styles.go` — `ColorAccent` border and label on the selected tile,
`ColorBorder` otherwise; dormant tiles render their label in `MutedText`.

Vertical overflow scrolls by row, keeping the cursor row visible, with a muted
`▼ N more` line when clipped. This mirrors the converge-on-visible-rows approach
already in `ReposModel.View`.

### Views

```go
type ViewMode int
const (
    ViewGrid ViewMode = iota  // default
    ViewDashboard             // today's five-panel layout, unchanged
)
```

`d` toggles. `general.default_view = "grid" | "dashboard"` in config.toml sets
the startup view, defaulting to `"grid"`.

`ViewDashboard` renders exactly what `View()` renders today, with Tab panel
cycling, `c` capture, `x` toggle, `v`/`V` visualizer, and the Today/Notes/Viz
panels all intact. That code path is untouched.

### Preview

Desktop only. Above ~70 columns the grid takes the upper portion of the screen
and the existing session preview — `renderPreviewHeader` plus the `capture-pane`
output — occupies the rest, showing the selected target when it is running. Below
70 columns the grid takes the full screen and `fetchPreview` is not issued.

This is the only phone-specific branch in the feature. It matters for more than
space: `fetchPreview` shells out to `tmux capture-pane` on every cursor move, and
over a phone connection that is latency nobody asked for.

### Keys in grid view

| Key | Action |
|---|---|
| `h` `l` / ← → | cursor ∓1, clamped |
| `j` `k` / ↓ ↑ | cursor ∓`cols`, clamped to the last target on a short final row |
| `Enter` | running → `tmuxSwitch(label)`; dormant → `tmuxJump(label, repo.Path)` |
| `n` | new session dialog (unchanged) |
| `s` | save selected running session to config (unchanged) |
| `/` | session search overlay (unchanged) |
| `r` | refresh all sources |
| `d` | switch to dashboard view |
| `q` | quit |

`Enter` dispatch is the one new piece of behavior; both destination functions
already exist. A dormant target with no `Repo.Path` cannot occur — dormant
targets are constructed from config entries, which always carry a path.

### Floor

Below 24 columns the existing "terminal too narrow" message renders instead. One
tile is illegible below that, and the 60-column floor in `View()` moves down to
24 to make room for the phone case.

## Files

| File | Change |
|---|---|
| `tui/grid.go` | **new** — `Target`, `BuildTargets`, column math, tile + grid rendering, grid key handling |
| `tui/grid_test.go` | **new** — the test surface below |
| `tui/app.go` | `ViewMode` field + toggle; `View()` branches to grid; `handleNavKey` routes to grid keys in `ViewGrid`; `fetchPreview` gated on width; 60→24 floor |
| `tui/keyhints.go` | grid-view hint set |
| `config/config.go` | `DefaultView` field on `GeneralConfig`, defaulting to `"grid"` |
| `config_template.go` | document `default_view` |
| `tui/app_test.go` | `TestFocusCycling` and `TestCaptureModeEnterExit` set `ViewDashboard` first — Tab cycling and `c` are dashboard behaviors now |
| `README.md` | grid as the default view; phone usage |

Estimated ~450 new lines across `grid.go` and its tests, above the 300-line
guidance in CLAUDE.md. Flagged and accepted when the design was approved.

## Testing

Tests first, per TDD.

**`BuildTargets`** — the join (session + matching repo produce one tile, not
two); a session with no config entry (Repo nil, no git line); a repo with no
session (dormant); the cockpit session excluded; running-before-dormant ordering;
alphabetical within group; empty inputs.

**Cursor** — label survives a refresh that inserts a target above it; a vanished
label falls back to a clamped neighbor; movement clamps at all four edges; `j`
from a full row onto a short final row lands on the last target rather than
overshooting into nothing.

**Column math** — `cols` at widths 30, 44, 70, 120, 200 hits 1, 2, 3, 4, 4.

**Rendering** — no rendered line exceeds terminal width at 40, 55, and 120
columns; renders without panic at 0, 1, and odd target counts.

**Enter dispatch** — running target selects switch, dormant selects jump.

**Regression** — the existing `tui` and `sources` suites stay green.

Verification before completion: `go build ./... && go test ./...`, output shown.
