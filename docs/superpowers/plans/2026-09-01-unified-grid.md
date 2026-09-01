# Unified Target Grid Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a single responsive grid of running tmux sessions and saved repos cockpit's default view, usable down to phone width, without deleting the existing five-panel dashboard.

**Architecture:** A pure `BuildTargets` function joins tmux sessions and configured repos into one ordered `[]Target`. Pure geometry functions (`GridCols`, `MoveGridCursor`, `resolveGridCursor`) handle column count and cursor motion. `RenderGrid` draws tiles. `Model` gains a `ViewMode` that routes both `View()` and `handleNavKey` to either the new grid or the untouched dashboard.

**Tech Stack:** Go 1.22, Bubbletea (`tea.Model` / `Update` / `View`), Lipgloss (styles, `JoinHorizontal`, `RoundedBorder`), Cobra + BurntSushi/toml for config. Standard `testing` package, no test framework.

**Spec:** `docs/superpowers/specs/2026-09-01-unified-grid-design.md`

## Global Constraints

- Nothing is deleted. `tui/tasks.go`, `tui/inbox.go`, `tui/viz.go`, all 8 visualizers, and `sources/obsidian.go` keep compiling and keep their tests passing.
- The dashboard render path and its key handling stay byte-for-byte behaviorally identical, reachable via `ViewDashboard`.
- Join key between a session and a repo is `session.Name == repo.Label`.
- Target ordering: running before dormant, alphabetical by label within each group.
- The grid cursor is stored as a **label**, not an index.
- Column count: `cols = clamp(width / 22, 1, 4)`.
- Tile height is 5 rows: 3 content lines plus 2 border rows.
- Preview and `fetchPreview` are gated on `width >= 70`.
- The "terminal too narrow" floor moves from 60 to 24 columns.
- Package is `tui` for all new TUI files; imports use the module path `github.com/jhoot/cockpit/...` (note: `jhoot`, not `jeffdhooton`).
- Verify with `go build ./... && go test ./...` and show output before claiming done.

---

### Task 1: Target and BuildTargets

The pure join. No model state, no I/O — this is where most of the correctness lives.

**Files:**
- Create: `tui/grid.go`
- Test: `tui/grid_test.go`

**Interfaces:**
- Consumes: `sources.TmuxSession{Name, Windows, Attached, LastUsed}`, `sources.GitRepoStatus{Label, Path, Branch, Dirty, DirtyCount, Unpushed, Behind, LastCommit, Error}`, `sources.ClaudeStatus` (`ClaudeStatusUnknown`/`Idle`/`Working`) — all already defined in `sources/source.go` and `sources/tmux.go`.
- Produces: `type Target struct{ Label string; Session *sources.TmuxSession; Repo *sources.GitRepoStatus; Status sources.ClaudeStatus }`, `func (t Target) Running() bool`, `func BuildTargets(sessions []sources.TmuxSession, repos []sources.GitRepoStatus, statuses map[string]sources.ClaudeStatus, selfSession string) []Target`.

- [ ] **Step 1: Write the failing test**

Create `tui/grid_test.go`:

```go
package tui

import (
	"testing"

	"github.com/jhoot/cockpit/sources"
)

func sess(name string) sources.TmuxSession {
	return sources.TmuxSession{Name: name, Windows: 1}
}

func repo(label string) sources.GitRepoStatus {
	return sources.GitRepoStatus{Label: label, Path: "/tmp/" + label, Branch: "main"}
}

func labels(targets []Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Label)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBuildTargetsJoinsSessionAndRepo(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("my-app")},
		[]sources.GitRepoStatus{repo("my-app")},
		nil, "cockpit",
	)
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1 (session and repo must join, not duplicate)", len(targets))
	}
	if targets[0].Session == nil || targets[0].Repo == nil {
		t.Fatalf("joined target missing a side: session=%v repo=%v", targets[0].Session, targets[0].Repo)
	}
	if !targets[0].Running() {
		t.Error("target with a session should report Running")
	}
}

func TestBuildTargetsSessionWithoutRepo(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("scratch")}, nil, nil, "cockpit")
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Repo != nil {
		t.Error("session with no config entry should have nil Repo")
	}
}

func TestBuildTargetsDormantRepo(t *testing.T) {
	targets := BuildTargets(nil, []sources.GitRepoStatus{repo("dotfiles")}, nil, "cockpit")
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Running() {
		t.Error("repo with no session should not report Running")
	}
}

func TestBuildTargetsExcludesSelfSession(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("cockpit"), sess("my-app")},
		nil, nil, "cockpit",
	)
	eq(t, labels(targets), []string{"my-app"})
}

func TestBuildTargetsOrdersRunningBeforeDormantAlphabetically(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("zeta"), sess("alpha")},
		[]sources.GitRepoStatus{repo("beta"), repo("alpha")},
		nil, "cockpit",
	)
	eq(t, labels(targets), []string{"alpha", "zeta", "beta"})
}

func TestBuildTargetsCarriesStatus(t *testing.T) {
	statuses := map[string]sources.ClaudeStatus{"my-app": sources.ClaudeStatusWorking}
	targets := BuildTargets([]sources.TmuxSession{sess("my-app")}, nil, statuses, "cockpit")
	if targets[0].Status != sources.ClaudeStatusWorking {
		t.Errorf("Status = %v, want Working", targets[0].Status)
	}
}

func TestBuildTargetsEmpty(t *testing.T) {
	if got := BuildTargets(nil, nil, nil, "cockpit"); len(got) != 0 {
		t.Errorf("got %d targets from empty inputs, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run TestBuildTargets -v`
Expected: FAIL — compile error, `undefined: BuildTargets` and `undefined: Target`.

- [ ] **Step 3: Write minimal implementation**

Create `tui/grid.go`:

```go
package tui

import (
	"sort"

	"github.com/jhoot/cockpit/sources"
)

// Target is one tile in the grid: a running tmux session, a saved repo with no
// session, or both joined on session.Name == repo.Label — the same identity
// tmuxJump already assumes when it switches to a session named for a repo.
type Target struct {
	Label   string
	Session *sources.TmuxSession
	Repo    *sources.GitRepoStatus
	Status  sources.ClaudeStatus
}

// Running reports whether the target has a live tmux session behind it.
func (t Target) Running() bool { return t.Session != nil }

// BuildTargets joins sessions and repos into one ordered tile list. Running
// targets come first, then dormant, alphabetical within each group. Ordering is
// deliberately not last-used: on a 5-second refresh that would move the tile
// under the cursor while the user is aiming at it.
func BuildTargets(
	sessions []sources.TmuxSession,
	repos []sources.GitRepoStatus,
	statuses map[string]sources.ClaudeStatus,
	selfSession string,
) []Target {
	repoByLabel := make(map[string]*sources.GitRepoStatus, len(repos))
	for i := range repos {
		repoByLabel[repos[i].Label] = &repos[i]
	}

	var running, dormant []Target
	live := make(map[string]bool, len(sessions))

	for i := range sessions {
		s := &sessions[i]
		if s.Name == selfSession {
			continue
		}
		live[s.Name] = true
		running = append(running, Target{
			Label:   s.Name,
			Session: s,
			Repo:    repoByLabel[s.Name],
			Status:  statuses[s.Name],
		})
	}

	for i := range repos {
		r := &repos[i]
		if live[r.Label] {
			continue
		}
		dormant = append(dormant, Target{Label: r.Label, Repo: r})
	}

	sort.Slice(running, func(i, j int) bool { return running[i].Label < running[j].Label })
	sort.Slice(dormant, func(i, j int) bool { return dormant[i].Label < dormant[j].Label })

	return append(running, dormant...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run TestBuildTargets -v`
Expected: PASS, 7 tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add tui/grid.go tui/grid_test.go
git commit -m "Add Target type and BuildTargets join"
```

---

### Task 2: Grid geometry and cursor

Column count, cursor movement, and label-based cursor resolution. Still pure functions.

**Files:**
- Modify: `tui/grid.go`
- Test: `tui/grid_test.go`

**Interfaces:**
- Consumes: `Target` from Task 1.
- Produces: constants `gridCellWidth = 22`, `gridMaxCols = 4`, `gridTileH = 5`, `MobileMaxWidth = 70`, `MinTerminalWidth = 24`; `func GridCols(width int) int`; `func MoveGridCursor(idx, count, cols, dx, dy int) int`; `func resolveGridCursor(targets []Target, label string, prev int) int`.

- [ ] **Step 1: Write the failing test**

Append to `tui/grid_test.go`:

```go
func TestGridCols(t *testing.T) {
	cases := []struct{ width, want int }{
		{30, 1},  // very narrow phone pane
		{44, 2},  // Termius vertical
		{70, 3},
		{120, 4}, // capped
		{200, 4}, // capped
	}
	for _, c := range cases {
		if got := GridCols(c.width); got != c.want {
			t.Errorf("GridCols(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

func TestGridColsNeverZero(t *testing.T) {
	for _, w := range []int{0, 1, 10, 21} {
		if got := GridCols(w); got < 1 {
			t.Errorf("GridCols(%d) = %d, want >= 1", w, got)
		}
	}
}

func TestMoveGridCursorClampsAtEdges(t *testing.T) {
	// 5 targets, 2 columns: rows are [0 1] [2 3] [4]
	cases := []struct {
		name           string
		idx, dx, dy    int
		want           int
	}{
		{"left at start", 0, -1, 0, 0},
		{"right at end", 4, 1, 0, 4},
		{"up from top row", 1, 0, -1, 0},
		{"down from full row", 2, 0, 1, 4},
		{"down onto short final row clamps to last", 3, 0, 1, 4},
		{"down from last row stays", 4, 0, 1, 4},
		{"up from last row", 4, 0, -1, 2},
		{"right within row", 0, 1, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MoveGridCursor(c.idx, 5, 2, c.dx, c.dy); got != c.want {
				t.Errorf("MoveGridCursor(%d, 5, 2, %d, %d) = %d, want %d",
					c.idx, c.dx, c.dy, got, c.want)
			}
		})
	}
}

func TestMoveGridCursorEmpty(t *testing.T) {
	if got := MoveGridCursor(0, 0, 2, 0, 1); got != 0 {
		t.Errorf("MoveGridCursor on empty grid = %d, want 0", got)
	}
}

func TestResolveGridCursorFindsLabel(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("alpha"), sess("beta"), sess("gamma")},
		nil, nil, "cockpit",
	)
	if got := resolveGridCursor(targets, "gamma", 0); got != 2 {
		t.Errorf("resolveGridCursor = %d, want 2", got)
	}
}

func TestResolveGridCursorSurvivesInsertionAbove(t *testing.T) {
	// Selection is on "gamma" at index 1; "alpha" then appears above it.
	before := BuildTargets([]sources.TmuxSession{sess("beta"), sess("gamma")}, nil, nil, "cockpit")
	idx := resolveGridCursor(before, "gamma", 0)
	after := BuildTargets(
		[]sources.TmuxSession{sess("alpha"), sess("beta"), sess("gamma")},
		nil, nil, "cockpit",
	)
	if got := resolveGridCursor(after, "gamma", idx); got != 2 {
		t.Errorf("cursor moved off gamma: got index %d, want 2", got)
	}
}

func TestResolveGridCursorVanishedLabelClamps(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("alpha")}, nil, nil, "cockpit")
	if got := resolveGridCursor(targets, "gone", 7); got != 0 {
		t.Errorf("resolveGridCursor with stale label = %d, want 0 (clamped)", got)
	}
	if got := resolveGridCursor(nil, "gone", 3); got != 0 {
		t.Errorf("resolveGridCursor on empty targets = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestGridCols|TestMoveGridCursor|TestResolveGridCursor' -v`
Expected: FAIL — compile error, `undefined: GridCols`, `undefined: MoveGridCursor`, `undefined: resolveGridCursor`.

- [ ] **Step 3: Write minimal implementation**

Append to `tui/grid.go`:

```go
const (
	// gridCellWidth is one tile's total footprint: 18 content + 2 border + 2 gap.
	gridCellWidth = 22
	// gridMaxCols caps the grid. An 8-wide grid on a 200-column terminal is too
	// sparse to scan, and the width is better spent on the preview.
	gridMaxCols = 4
	// gridTileH is a tile's total height: 3 content lines + 2 border rows.
	gridTileH = 5

	// MobileMaxWidth is the threshold below which the preview is dropped and the
	// grid takes the full screen.
	MobileMaxWidth = 70
	// MinTerminalWidth is the floor below which even one tile is illegible.
	MinTerminalWidth = 24
)

// GridCols returns the column count for a given terminal width.
func GridCols(width int) int {
	cols := width / gridCellWidth
	if cols < 1 {
		return 1
	}
	if cols > gridMaxCols {
		return gridMaxCols
	}
	return cols
}

// MoveGridCursor returns the index after a directional move. Horizontal moves
// step by one and so walk the list linearly across row boundaries, which is the
// fast path on a phone. Vertical moves step by a full row and clamp to the last
// target, so a short final row is still reachable from the row above.
func MoveGridCursor(idx, count, cols, dx, dy int) int {
	if count <= 0 {
		return 0
	}
	idx += dx + dy*cols
	if idx < 0 {
		return 0
	}
	if idx >= count {
		return count - 1
	}
	return idx
}

// resolveGridCursor turns the stored cursor label into an index. When the label
// is gone — session died, repo dropped from config — it clamps the previous
// index into range so the selection lands on a neighbour instead of jumping to
// the top.
func resolveGridCursor(targets []Target, label string, prev int) int {
	for i := range targets {
		if targets[i].Label == label {
			return i
		}
	}
	if len(targets) == 0 {
		return 0
	}
	if prev < 0 {
		return 0
	}
	if prev >= len(targets) {
		return len(targets) - 1
	}
	return prev
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestGridCols|TestMoveGridCursor|TestResolveGridCursor' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add tui/grid.go tui/grid_test.go
git commit -m "Add grid geometry and label-based cursor resolution"
```

---

### Task 3: Tile and grid rendering

**Files:**
- Modify: `tui/grid.go`
- Test: `tui/grid_test.go`

**Interfaces:**
- Consumes: `Target`, `GridCols`, `gridTileH` from Tasks 1–2; existing helpers from `tui/styles.go` (`BoldText`, `MutedText`, `AccentText`, `PurpleText`, `WarningText`, `StatusClean`, `StatusDirty`, `StatusUnpushed`, `ColorAccent`, `ColorBorder`, `ColorMuted`, `StatusDot`, `Variant*`, `Truncate`) and `formatIdleTime` from `tui/sessions.go`.
- Produces: `func renderTile(t Target, width int, selected bool) string`, `func RenderGrid(targets []Target, cursor, width, height int) string`.

- [ ] **Step 1: Write the failing test**

Append to `tui/grid_test.go` (add `"strings"` and `"github.com/charmbracelet/lipgloss"` to the import block):

```go
func TestRenderGridFitsWidth(t *testing.T) {
	targets := BuildTargets(
		[]sources.TmuxSession{sess("my-app"), sess("cockpit-ui"), sess("a-very-long-session-name-here")},
		[]sources.GitRepoStatus{repo("dotfiles"), repo("side-project")},
		nil, "cockpit",
	)
	for _, width := range []int{40, 55, 120} {
		out := RenderGrid(targets, 0, width, 20)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width=%d: line %d is %d cells wide: %q", width, i, w, line)
			}
		}
	}
}

func TestRenderGridHandlesOddAndEmptyCounts(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		var sessions []sources.TmuxSession
		for i := 0; i < n; i++ {
			sessions = append(sessions, sess(string(rune('a'+i))))
		}
		targets := BuildTargets(sessions, nil, nil, "cockpit")
		out := RenderGrid(targets, 0, 44, 20) // must not panic
		if out == "" {
			t.Errorf("n=%d: RenderGrid returned empty string", n)
		}
	}
}

func TestRenderGridShowsMoreIndicatorWhenClipped(t *testing.T) {
	var sessions []sources.TmuxSession
	for i := 0; i < 12; i++ {
		sessions = append(sessions, sess("s"+string(rune('a'+i))))
	}
	targets := BuildTargets(sessions, nil, nil, "cockpit")
	// height 10 fits 2 rows of 2 columns = 4 tiles, leaving 8 hidden.
	out := RenderGrid(targets, 0, 44, 10)
	if !strings.Contains(out, "more") {
		t.Errorf("clipped grid should show a 'more' indicator, got:\n%s", out)
	}
}

func TestRenderGridScrollsToKeepCursorVisible(t *testing.T) {
	var sessions []sources.TmuxSession
	for i := 0; i < 12; i++ {
		sessions = append(sessions, sess("s"+string(rune('a'+i))))
	}
	targets := BuildTargets(sessions, nil, nil, "cockpit")
	last := len(targets) - 1
	out := RenderGrid(targets, last, 44, 10)
	if !strings.Contains(out, targets[last].Label) {
		t.Errorf("selected target %q not visible in clipped grid:\n%s", targets[last].Label, out)
	}
}

func TestRenderTileShowsGitStateForDormantRepo(t *testing.T) {
	r := repo("dotfiles")
	r.Branch = "main"
	r.Dirty = true
	r.DirtyCount = 3
	targets := BuildTargets(nil, []sources.GitRepoStatus{r}, nil, "cockpit")
	out := renderTile(targets[0], 22, false)
	if !strings.Contains(out, "main") {
		t.Errorf("dormant tile should show its branch, got:\n%s", out)
	}
}

func TestRenderTileHeight(t *testing.T) {
	targets := BuildTargets([]sources.TmuxSession{sess("my-app")}, nil, nil, "cockpit")
	out := renderTile(targets[0], 22, true)
	if got := len(strings.Split(out, "\n")); got != gridTileH {
		t.Errorf("tile height = %d lines, want %d", got, gridTileH)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestRenderGrid|TestRenderTile' -v`
Expected: FAIL — compile error, `undefined: RenderGrid`, `undefined: renderTile`.

- [ ] **Step 3: Write minimal implementation**

Append to `tui/grid.go` (add `"fmt"`, `"strings"`, and `"github.com/charmbracelet/lipgloss"` to its import block):

```go
// renderTile draws one target: label, status, and git state. Every piece is
// truncated to the inner width before styling, so the tile can never wrap and
// blow its 3-line content budget.
func renderTile(t Target, width int, selected bool) string {
	inner := width - 4 // 2 border cells + 2 padding cells
	if inner < 6 {
		inner = 6
	}

	nameStyle := BoldText
	switch {
	case selected:
		nameStyle = BoldText.Foreground(ColorAccent)
	case !t.Running():
		nameStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	}
	name := nameStyle.Render(Truncate(t.Label, inner))

	status := StatusDot("no session", VariantMuted)
	if t.Running() {
		status = StatusDot("detached", VariantMuted)
		if t.Session.Attached {
			status = StatusDot("attached", VariantAccent)
		}
		switch t.Status {
		case sources.ClaudeStatusIdle:
			label := "idle"
			if age := formatIdleTime(t.Session.LastUsed); age != "" {
				label = "idle " + age
			}
			status = StatusDot(Truncate(label, inner-2), VariantMuted)
		case sources.ClaudeStatusWorking:
			status = StatusDot("working", VariantAccent)
		}
	}

	git := ""
	if t.Repo != nil {
		if t.Repo.Error != nil {
			git = WarningText.Render("git err")
		} else {
			// Reserve room for the trailing markers before truncating the branch.
			branchW := inner - 6
			if branchW < 3 {
				branchW = 3
			}
			git = PurpleText.Render(Truncate(t.Repo.Branch, branchW))
			if t.Repo.Dirty {
				git += " " + StatusDirty.Render(fmt.Sprintf("✗%d", t.Repo.DirtyCount))
			} else {
				git += " " + StatusClean.Render("✓")
			}
			if t.Repo.Unpushed > 0 && lipgloss.Width(git)+3 <= inner {
				git += " " + StatusUnpushed.Render(fmt.Sprintf("↑%d", t.Repo.Unpushed))
			}
		}
	}

	borderColor := ColorBorder
	if selected {
		borderColor = ColorAccent
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(inner).
		Height(3).
		MaxHeight(gridTileH).
		Render(name + "\n" + status + "\n" + git)
}

// RenderGrid lays targets out in a responsive grid, scrolling by row to keep the
// cursor visible and appending a muted count when tiles are clipped.
func RenderGrid(targets []Target, cursor, width, height int) string {
	if len(targets) == 0 {
		return MutedText.Render("No sessions or repos. Add repos in ") +
			AccentText.Render("~/.config/cockpit/config.toml")
	}

	cols := GridCols(width)
	cellW := width / cols
	rows := (len(targets) + cols - 1) / cols

	visibleRows := height / gridTileH
	if visibleRows < 1 {
		visibleRows = 1
	}

	offset := 0
	if cursorRow := cursor / cols; cursorRow >= visibleRows {
		offset = cursorRow - visibleRows + 1
	}

	var out []string
	for r := offset; r < rows && r < offset+visibleRows; r++ {
		var cells []string
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= len(targets) {
				cells = append(cells, lipgloss.NewStyle().Width(cellW).Height(gridTileH).Render(""))
				continue
			}
			cells = append(cells, renderTile(targets[i], cellW, i == cursor))
		}
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}

	if shown := (offset + visibleRows) * cols; shown < len(targets) {
		out = append(out, MutedText.Render(fmt.Sprintf("  ▼ %d more", len(targets)-shown)))
	}

	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestRenderGrid|TestRenderTile' -v`
Expected: PASS. If `TestRenderGridFitsWidth` fails at width 40, the cause is `cellW = width / cols` leaving a remainder — the fix is in `renderTile`'s truncation, not in widening the cell.

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add tui/grid.go tui/grid_test.go
git commit -m "Add tile and responsive grid rendering"
```

---

### Task 4: Config default_view

**Files:**
- Modify: `config/config.go:21-24` (GeneralConfig), `config/config.go:79-96` (applyDefaults), `config/config.go:107-118` (validate)
- Modify: `config_template.go`
- Test: `config/config_test.go`

**Interfaces:**
- Produces: `config.GeneralConfig.DefaultView string` (toml key `default_view`), defaulting to `"grid"`, validated against `"grid"`/`"dashboard"`.

- [ ] **Step 1: Write the failing test**

Append to `config/config_test.go`:

```go
func TestDefaultViewDefaultsToGrid(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.General.DefaultView != "grid" {
		t.Errorf("DefaultView = %q, want %q", cfg.General.DefaultView, "grid")
	}
}

func TestDefaultViewRejectsUnknownValue(t *testing.T) {
	cfg := &Config{
		General:  GeneralConfig{RefreshInterval: 5, DefaultView: "gird"},
		Obsidian: ObsidianConfig{VaultPath: "/tmp/vault"},
		Signals:  SignalsConfig{StaleSessionThreshold: "24h"},
	}
	if err := validate(cfg); err == nil {
		t.Error("validate should reject an unknown default_view (a silent typo is worse than an error)")
	}
}

func TestDefaultViewAcceptsDashboard(t *testing.T) {
	cfg := &Config{
		General:  GeneralConfig{RefreshInterval: 5, DefaultView: "dashboard"},
		Obsidian: ObsidianConfig{VaultPath: "/tmp/vault"},
		Signals:  SignalsConfig{StaleSessionThreshold: "24h"},
	}
	if err := validate(cfg); err != nil {
		t.Errorf("validate rejected dashboard: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jeff/workspace/cockpit && go test ./config/ -run TestDefaultView -v`
Expected: FAIL — compile error, `unknown field DefaultView in struct literal`.

- [ ] **Step 3: Write minimal implementation**

In `config/config.go`, add the field to `GeneralConfig`:

```go
type GeneralConfig struct {
	SessionName     string `toml:"session_name"`
	RefreshInterval int    `toml:"refresh_interval"`
	DefaultView     string `toml:"default_view"` // "grid" (default) or "dashboard"
}
```

In `applyDefaults`, after the `RefreshInterval` default:

```go
	if cfg.General.DefaultView == "" {
		cfg.General.DefaultView = "grid"
	}
```

In `validate`, before the final `return nil`:

```go
	switch cfg.General.DefaultView {
	case "", "grid", "dashboard":
	default:
		return fmt.Errorf("config: default_view must be \"grid\" or \"dashboard\", got %q", cfg.General.DefaultView)
	}
```

In `config_template.go`, add to the `[general]` block:

```
# Startup view: "grid" (sessions + repos as one grid) or "dashboard" (all panels)
default_view = "grid"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jeff/workspace/cockpit && go test ./config/ -v`
Expected: PASS, including the pre-existing config tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add config/config.go config/config_test.go config_template.go
git commit -m "Add general.default_view config option"
```

---

### Task 5: View mode and grid view wiring

Route `View()` to the grid or the dashboard. The dashboard body moves into its own method unchanged so the overlay code can serve both.

**Files:**
- Modify: `tui/app.go:159-194` (Model fields), `tui/app.go:196-221` (NewModel), `tui/app.go:785-896` (View)
- Modify: `tui/app.go:1047-1063` (fetchPreview)
- Test: `tui/grid_test.go`

**Interfaces:**
- Consumes: `BuildTargets`, `GridCols`, `resolveGridCursor`, `RenderGrid`, `MobileMaxWidth`, `MinTerminalWidth`, `gridTileH` from Tasks 1–3; `config.GeneralConfig.DefaultView` from Task 4.
- Produces: `type ViewMode int` with `ViewGrid`/`ViewDashboard`; `Model.view ViewMode`, `Model.gridCursor string`, `Model.gridIndex int`; `func (m Model) gridTargets() []Target`; `func (m Model) gridView() string`; `func (m Model) dashboardView() string`.

- [ ] **Step 1: Write the failing test**

Append to `tui/grid_test.go` (add `"github.com/jhoot/cockpit/config"` to imports):

```go
func gridTestModel(width, height int) Model {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = width
	m.height = height
	m.layout = CalculateLayout(width, height, 0)
	m.sessions.Loading = false
	m.sessions.Sessions = []sources.TmuxSession{sess("my-app"), sess("scry")}
	m.repos.Loading = false
	m.repos.Repos = []sources.GitRepoStatus{repo("dotfiles")}
	return m
}

func TestDefaultViewIsGrid(t *testing.T) {
	cfg := testConfig()
	cfg.General.DefaultView = "grid"
	if m := NewModel(cfg, "/tmp/config.toml"); m.view != ViewGrid {
		t.Errorf("view = %v, want ViewGrid", m.view)
	}
}

func TestDashboardViewHonoursConfig(t *testing.T) {
	cfg := testConfig()
	cfg.General.DefaultView = "dashboard"
	if m := NewModel(cfg, "/tmp/config.toml"); m.view != ViewDashboard {
		t.Errorf("view = %v, want ViewDashboard", m.view)
	}
}

func TestGridTargetsExcludeCockpitSession(t *testing.T) {
	m := gridTestModel(120, 40)
	m.sessions.Sessions = append(m.sessions.Sessions, sess("cockpit"))
	for _, tg := range m.gridTargets() {
		if tg.Label == "cockpit" {
			t.Error("cockpit's own session must not appear as a target")
		}
	}
}

func TestGridViewRendersAtPhoneWidth(t *testing.T) {
	m := gridTestModel(44, 24)
	out := m.View()
	if !strings.Contains(out, "my-app") {
		t.Errorf("phone-width view should list sessions, got:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 44 {
			t.Errorf("line %d is %d cells wide at width 44: %q", i, w, line)
		}
	}
}

func TestNarrowFloorIsTwentyFour(t *testing.T) {
	m := gridTestModel(20, 24)
	if out := m.View(); !strings.Contains(out, "too narrow") {
		t.Errorf("width 20 should show the too-narrow message, got:\n%s", out)
	}
	m = gridTestModel(44, 24)
	if out := m.View(); strings.Contains(out, "too narrow") {
		t.Error("width 44 should render the grid, not the too-narrow message")
	}
}

func TestPreviewSkippedAtPhoneWidth(t *testing.T) {
	m := gridTestModel(44, 24)
	if cmd := m.fetchPreview(); cmd != nil {
		t.Error("fetchPreview should be a no-op below MobileMaxWidth")
	}
	m = gridTestModel(120, 40)
	if cmd := m.fetchPreview(); cmd == nil {
		t.Error("fetchPreview should still run on desktop width")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestDefaultViewIsGrid|TestDashboardViewHonours|TestGridTargets|TestGridViewRenders|TestNarrowFloor|TestPreviewSkipped' -v`
Expected: FAIL — compile error, `undefined: ViewGrid`, `m.view undefined`, `m.gridTargets undefined`.

- [ ] **Step 3: Write minimal implementation**

In `tui/app.go`, after the `Mode` constants (around line 43), add:

```go
// ViewMode selects the top-level layout.
type ViewMode int

const (
	ViewGrid      ViewMode = iota // unified sessions + repos grid (default)
	ViewDashboard                 // the five-panel dashboard
)
```

Add to the `Model` struct, next to `mode Mode`:

```go
	view       ViewMode
	gridCursor string // label of the selected target; survives list churn
	gridIndex  int    // last resolved index, used as a fallback when the label is gone
```

In `NewModel`, set the initial view from config — add before `return m`:

```go
	if cfg.General.DefaultView == "dashboard" {
		m.view = ViewDashboard
	}
```

Rename the existing `func (m Model) View() string` to `func (m Model) dashboardView() string`, and delete its first block (the `if m.width < 60` guard) and its trailing overlay blocks (the three `if m.mode == Mode...` blocks) — those move to the new `View()`. `dashboardView` now ends at:

```go
	return lipgloss.JoinVertical(lipgloss.Left,
		sessionsPanel,
		middleRow,
		bottomRow,
		keyhints,
	)
}
```

Add the new `View()`, `gridTargets`, and `gridView` to `tui/grid.go`:

```go
// View renders the active view, then any modal overlay on top of it.
func (m Model) View() string {
	if m.width < MinTerminalWidth {
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			WarningText.Render("Terminal too narrow.\nResize or press q to quit."))
	}

	page := m.dashboardView()
	if m.view == ViewGrid {
		page = m.gridView()
	}

	switch m.mode {
	case ModeNewSession:
		page = m.overlay(page, m.renderNewSessionDialog())
	case ModeSearch:
		page = m.overlay(page, m.renderSearchDialog())
	case ModeVizPicker:
		page = m.overlay(page, m.renderVizPickerDialog())
	}
	return page
}

func (m Model) overlay(page, dialog string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ColorBg))
}

// gridTargets builds the current tile list from live sessions and configured repos.
func (m Model) gridTargets() []Target {
	return BuildTargets(m.sessions.Sessions, m.repos.Repos, m.sessions.Statuses, m.config.General.SessionName)
}

// gridView renders the unified grid, plus the session preview on desktop widths.
func (m Model) gridView() string {
	targets := m.gridTargets()
	cursor := resolveGridCursor(targets, m.gridCursor, m.gridIndex)

	hints := GridKeyhintsView(m.width)
	if m.transientErr != "" {
		hints = WarningText.Render(m.transientErr)
	}

	body := m.height - 1 // keyhints row
	if body < gridTileH {
		body = gridTileH
	}

	gridH := body
	showPreview := m.width >= MobileMaxWidth && len(targets) > 0
	if showPreview {
		gridH = body * 3 / 5
		if gridH < gridTileH+3 {
			gridH = gridTileH + 3
		}
	}

	// Panel chrome eats 2 border rows + 1 title row and 2 border + 2 padding cells.
	grid := RenderGrid(targets, cursor, m.width-4, gridH-3)
	page := RenderPanel("Cockpit", grid, m.width, gridH, true)

	if showPreview {
		previewH := body - gridH
		page = lipgloss.JoinVertical(lipgloss.Left, page, m.renderPreviewPanel(previewH))
	}

	return lipgloss.JoinVertical(lipgloss.Left, page, hints)
}

// renderPreviewPanel renders the capture-pane output for the selected session.
func (m Model) renderPreviewPanel(height int) string {
	name := m.selectedSessionName()
	if name == "" || m.sessionPreview == "" {
		return RenderPanel("Preview", MutedText.Render("(no preview)"), m.width, height, false)
	}

	innerW := m.width - 4
	maxLines := height - 3
	if maxLines < 1 {
		maxLines = 1
	}

	lines := strings.Split(m.sessionPreview, "\n")
	for i, line := range lines {
		if len(line) > innerW {
			lines[i] = line[:innerW-1] + "…"
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return RenderPanel(name, strings.Join(lines, "\n"), m.width, height, false)
}
```

In `fetchPreview` (`tui/app.go`), add the width gate as the first statement:

```go
func (m Model) fetchPreview() tea.Cmd {
	// capture-pane on every cursor move is latency nobody wants over a phone link,
	// and nothing renders the preview below this width anyway.
	if m.width < MobileMaxWidth {
		return nil
	}
	name := m.selectedSessionName()
	...
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestDefaultViewIsGrid|TestDashboardViewHonours|TestGridTargets|TestGridViewRenders|TestNarrowFloor|TestPreviewSkipped' -v`
Expected: FAIL on the first run with `undefined: GridKeyhintsView` — that function arrives in Task 6. Add this temporary stub to `tui/grid.go` to get the task green, and replace it in Task 6:

```go
// GridKeyhintsView renders the grid's key bar. Filled in by Task 6.
func GridKeyhintsView(width int) string { return "" }
```

Then re-run. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add tui/app.go tui/grid.go tui/grid_test.go
git commit -m "Route View between grid and dashboard modes"
```

---

### Task 6: Grid key handling

**Files:**
- Modify: `tui/grid.go`
- Modify: `tui/app.go:442-547` (handleNavKey)
- Modify: `tui/keyhints.go`
- Test: `tui/grid_test.go`

**Interfaces:**
- Consumes: `MoveGridCursor`, `resolveGridCursor`, `GridCols`, `Target` from Tasks 1–3; `ViewGrid`/`ViewDashboard`, `Model.gridCursor`, `Model.gridIndex` from Task 5; existing `tmuxSwitch`, `tmuxJump`, `tmuxSwitchResultMsg`, `saveSessionAsRepo` from `tui/app.go`.
- Produces: `func (m *Model) handleGridKey(msg tea.KeyMsg) tea.Cmd`, `func (m *Model) setGridCursor(targets []Target, idx int)`, `func (m *Model) enterTarget(targets []Target, idx int) tea.Cmd`, real `func GridKeyhintsView(width int) string`.

- [ ] **Step 1: Write the failing test**

Append to `tui/grid_test.go` (add `tea "github.com/charmbracelet/bubbletea"` to imports):

```go
func TestGridKeysMoveCursor(t *testing.T) {
	m := gridTestModel(44, 24) // 2 cols; targets: my-app, scry (running), dotfiles (dormant)
	targets := m.gridTargets()
	if len(targets) != 3 {
		t.Fatalf("setup: got %d targets, want 3", len(targets))
	}

	m.handleGridKey(keyMsg("l"))
	if m.gridCursor != targets[1].Label {
		t.Errorf("after l: cursor = %q, want %q", m.gridCursor, targets[1].Label)
	}

	m.handleGridKey(keyMsg("j"))
	if m.gridCursor != targets[2].Label {
		t.Errorf("after j onto short final row: cursor = %q, want %q", m.gridCursor, targets[2].Label)
	}

	m.handleGridKey(keyMsg("k"))
	if m.gridCursor != targets[0].Label {
		t.Errorf("after k: cursor = %q, want %q", m.gridCursor, targets[0].Label)
	}

	m.handleGridKey(keyMsg("h"))
	if m.gridCursor != targets[0].Label {
		t.Errorf("h at start should clamp, cursor = %q", m.gridCursor)
	}
}

func TestGridCursorKeepsSessionsCursorAligned(t *testing.T) {
	m := gridTestModel(120, 40)
	m.handleGridKey(keyMsg("l")) // move to the second running target
	targets := m.gridTargets()
	idx := resolveGridCursor(targets, m.gridCursor, m.gridIndex)
	if got := m.selectedSessionName(); got != targets[idx].Label {
		t.Errorf("sessions cursor out of sync: selectedSessionName = %q, grid cursor = %q",
			got, targets[idx].Label)
	}
}

func TestGridEnterReturnsCommandForRunningAndDormant(t *testing.T) {
	m := gridTestModel(120, 40)
	targets := m.gridTargets()

	m.setGridCursor(targets, 0) // running
	if cmd := m.enterTarget(targets, 0); cmd == nil {
		t.Error("Enter on a running target should return a switch cmd")
	}

	dormant := len(targets) - 1
	if targets[dormant].Running() {
		t.Fatalf("setup: target %d should be dormant", dormant)
	}
	if cmd := m.enterTarget(targets, dormant); cmd == nil {
		t.Error("Enter on a dormant repo should return a jump cmd")
	}
}

func TestGridEnterOnEmptyGridIsSafe(t *testing.T) {
	m := gridTestModel(44, 24)
	m.sessions.Sessions = nil
	m.repos.Repos = nil
	if cmd := m.enterTarget(m.gridTargets(), 0); cmd != nil {
		t.Error("Enter on an empty grid should return nil, not a cmd")
	}
}

func TestGridDashboardToggle(t *testing.T) {
	m := gridTestModel(120, 40)
	m.handleGridKey(keyMsg("d"))
	if m.view != ViewDashboard {
		t.Errorf("d in grid view should switch to dashboard, view = %v", m.view)
	}
	m.handleNavKey(keyMsg("d"))
	if m.view != ViewGrid {
		t.Errorf("d in dashboard view should switch back to grid, view = %v", m.view)
	}
}

func TestGridQuitAndRefresh(t *testing.T) {
	m := gridTestModel(120, 40)
	if cmd := m.handleGridKey(keyMsg("q")); cmd == nil {
		t.Error("q should return a quit cmd")
	}
	if cmd := m.handleGridKey(keyMsg("r")); cmd == nil {
		t.Error("r should return a refresh cmd")
	}
}

func TestGridSearchKeyOpensOverlay(t *testing.T) {
	m := gridTestModel(120, 40)
	m.handleGridKey(keyMsg("/"))
	if m.mode != ModeSearch {
		t.Errorf("/ should enter search mode, mode = %v", m.mode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestGridKeys|TestGridCursorKeeps|TestGridEnter|TestGridDashboardToggle|TestGridQuit|TestGridSearchKey' -v`
Expected: FAIL — compile error, `undefined: handleGridKey`, `undefined: setGridCursor`, `undefined: enterTarget`.

- [ ] **Step 3: Write minimal implementation**

Append to `tui/grid.go`:

```go
// setGridCursor moves the selection and keeps SessionsModel.Cursor aligned, so
// the preview, `s` save, and the search overlay keep working off one selection.
func (m *Model) setGridCursor(targets []Target, idx int) {
	if idx < 0 || idx >= len(targets) {
		return
	}
	m.gridIndex = idx
	m.gridCursor = targets[idx].Label
	for i, s := range m.sessions.Sessions {
		if s.Name == targets[idx].Label {
			m.sessions.Cursor = i
			return
		}
	}
}

// enterTarget switches to a running session, or creates and switches to one for
// a dormant repo.
func (m *Model) enterTarget(targets []Target, idx int) tea.Cmd {
	if idx < 0 || idx >= len(targets) {
		return nil
	}
	t := targets[idx]
	if t.Running() {
		name := t.Label
		return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxSwitch(name)} }
	}
	if t.Repo == nil {
		return nil
	}
	label, path := t.Label, t.Repo.Path
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJump(label, path)} }
}

// handleGridKey is the grid view's key surface. It is deliberately narrower than
// the dashboard's: panel-scoped keys have no meaning when no panels are shown.
func (m *Model) handleGridKey(msg tea.KeyMsg) tea.Cmd {
	targets := m.gridTargets()
	cols := GridCols(m.width)
	idx := resolveGridCursor(targets, m.gridCursor, m.gridIndex)

	move := func(dx, dy int) tea.Cmd {
		m.setGridCursor(targets, MoveGridCursor(idx, len(targets), cols, dx, dy))
		return m.fetchPreview()
	}

	switch msg.String() {
	case "h", "left":
		return move(-1, 0)
	case "l", "right":
		return move(1, 0)
	case "k", "up":
		return move(0, -1)
	case "j", "down":
		return move(0, 1)
	case "enter":
		return m.enterTarget(targets, idx)
	case "d":
		m.view = ViewDashboard
		return nil
	case "n":
		m.mode = ModeNewSession
		m.newSessionStep = 0
		m.newSessionPath = ""
		m.newSessionErr = ""
		m.newSessionInput.SetValue("")
		m.newSessionInput.Placeholder = "~/workspace/my-project"
		m.newSessionInput.Focus()
		return nil
	case "s":
		if idx < len(targets) && targets[idx].Running() {
			return m.saveSessionAsRepo()
		}
		return nil
	case "/":
		m.mode = ModeSearch
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.updateSearchResults()
		return nil
	case "r":
		return tea.Batch(m.fetchTmux(), m.fetchGit(), m.fetchGitHub())
	case "q":
		return tea.Quit
	}
	return nil
}
```

Add `tea "github.com/charmbracelet/bubbletea"` to `tui/grid.go`'s imports.

Replace the `GridKeyhintsView` stub from Task 5 with the real one in `tui/keyhints.go`:

```go
// GridKeyhintsView renders the grid view's key bar. Hints truncate from the
// right, so the phone sees the first few and the desktop sees them all.
func GridKeyhintsView(width int) string {
	hints := []struct{ key, desc string }{
		{"hjkl", "nav"},
		{"Enter", "jump"},
		{"n", "new"},
		{"s", "save"},
		{"/", "find"},
		{"d", "dash"},
		{"r", "refresh"},
		{"q", "quit"},
	}

	var parts []string
	totalLen := 0
	for _, h := range hints {
		key := strings.ToUpper(h.key)
		plainLen := len(key) + 1 + len(h.desc) + 3
		if totalLen+plainLen > width && len(parts) > 0 {
			break
		}
		parts = append(parts, AccentText.Render(key)+" "+MutedText.Render(h.desc))
		totalLen += plainLen
	}
	return "  " + strings.Join(parts, MutedText.Render(" · "))
}
```

Delete the temporary stub from `tui/grid.go`.

In `tui/app.go`, route `handleNavKey` at its top:

```go
func (m *Model) handleNavKey(msg tea.KeyMsg) tea.Cmd {
	if m.view == ViewGrid {
		return m.handleGridKey(msg)
	}
	switch msg.String() {
```

and add a `d` case to the dashboard's switch, next to `case "q":`:

```go
	case "d":
		m.view = ViewGrid
		return nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jeff/workspace/cockpit && go test ./tui/ -run 'TestGridKeys|TestGridCursorKeeps|TestGridEnter|TestGridDashboardToggle|TestGridQuit|TestGridSearchKey' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add tui/grid.go tui/app.go tui/keyhints.go tui/grid_test.go
git commit -m "Add grid key handling and key hints"
```

---

### Task 7: Reconcile existing tests, docs, and full verification

Two existing tests drive `handleNavKey` directly and assume the dashboard's key surface. That behavior now lives behind `ViewDashboard`, so the tests must say so. This is a relocation of behavior, not a weakening of the assertions.

**Files:**
- Modify: `tui/app_test.go:93-116` (TestFocusCycling), `tui/app_test.go:140-176` (TestCaptureModeEnterExit, TestCaptureModeBlocksNavKeys)
- Modify: `README.md`

- [ ] **Step 1: Run the full suite to see what the new routing broke**

Run: `cd /Users/jeff/workspace/cockpit && go build ./... && go test ./...`
Expected: FAIL in `tui` — `TestFocusCycling`, `TestCaptureModeEnterExit`, and `TestCaptureModeBlocksNavKeys` fail because `handleNavKey` now routes to the grid, where `tab` and `c` do nothing.

- [ ] **Step 2: Point those tests at the dashboard view**

In `tui/app_test.go`, add `m.view = ViewDashboard` after the `m.height = 40` line in each of `TestFocusCycling`, `TestCaptureModeEnterExit`, and `TestCaptureModeBlocksNavKeys`. For example:

```go
func TestFocusCycling(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = 100
	m.height = 40
	m.view = ViewDashboard // Tab panel cycling is a dashboard behavior
	...
```

`TestQuitReturnsQuit` and `TestRefreshKey` need no change — `q` and `r` work in both views. `TestMinimumTerminalWidth` needs no change — width 50 now renders the grid, which is still non-empty.

- [ ] **Step 3: Run the full suite**

Run: `cd /Users/jeff/workspace/cockpit && go build ./... && go test ./...`
Expected: PASS across `cmd`, `config`, `sources`, and `tui`.

- [ ] **Step 4: Update the README**

In `README.md`, replace the ASCII layout diagram with the grid, and add a note under "What it does":

```markdown
- **Grid** — Running tmux sessions and saved repos as one grid. `hjkl` to move,
  Enter to jump; a dormant repo gets a session created on the spot. The grid
  reflows from 4 columns down to 1, so it stays usable over SSH from a phone.
- **Dashboard** — Press `d` for the full five-panel view: Projects, Today,
  Notes, and the visualizer. Set `default_view = "dashboard"` in config.toml to
  make it the startup view.
```

- [ ] **Step 5: Commit**

```bash
cd /Users/jeff/workspace/cockpit
git add tui/app_test.go README.md
git commit -m "Scope dashboard key tests to dashboard view; document grid"
```

- [ ] **Step 6: Manual smoke test**

Run cockpit in a narrow pane and confirm the grid behaves:

```bash
cd /Users/jeff/workspace/cockpit && go build -o /tmp/cockpit-grid . && \
  tmux new-session -d -s grid-smoke -x 44 -y 24 /tmp/cockpit-grid && \
  sleep 2 && tmux capture-pane -t grid-smoke -p; tmux kill-session -t grid-smoke
```

Expected: a 2-column grid of tiles, no line wider than 44 cells, key hints on the
bottom row, no "too narrow" message.

---

## Self-Review

**Spec coverage:** `Target`/`BuildTargets` → Task 1. Cursor identity → Tasks 2, 6. Grid layout and column math → Tasks 2, 3. Views and the `d` toggle → Tasks 5, 6. `default_view` config → Task 4. Preview gating → Task 5. Key table → Task 6. The 24-column floor → Task 5. Files table → all tasks. Test surface → distributed across Tasks 1, 2, 3, 6, and closed out in Task 7.

**Type consistency:** `BuildTargets(sessions, repos, statuses, selfSession)` is called with that exact argument order in Tasks 1, 3, 5, and 6. `MoveGridCursor(idx, count, cols, dx, dy)` matches between Task 2's definition and Task 6's call. `renderTile(t, width, selected)` and `RenderGrid(targets, cursor, width, height)` match between Tasks 3 and 5. `GridKeyhintsView(width)` is stubbed in Task 5 and replaced in Task 6 — flagged explicitly in both.

**Known ordering wrinkle:** Task 5 cannot compile without `GridKeyhintsView`, which belongs with the other key-bar code in Task 6. Task 5 Step 4 carries the stub and Task 6 Step 3 deletes it. Executing the tasks out of order will not work.
