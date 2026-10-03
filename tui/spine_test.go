package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jeffdhooton/cockpit/sources"
)

// fixtureSpine runs no spine: it answers `bearings --json` from testdata.
type fixtureSpine struct {
	out string
	err error
}

func (f fixtureSpine) RunSpine(_ context.Context, args ...string) (string, error) {
	if strings.Join(args, " ") != "bearings --json" {
		return "", errors.New("unexpected spine call")
	}
	return f.out, f.err
}

func spineFixture(t *testing.T) sources.SpineStatus {
	t.Helper()
	b, err := os.ReadFile("../testdata/spine/bearings.json")
	if err != nil {
		t.Fatal(err)
	}
	st := sources.GetSpineStatus(context.Background(), fixtureSpine{out: string(b)})
	if !st.Readable() {
		t.Fatalf("fixture unreadable: %v", st.Err)
	}
	return st
}

// spineModel is the grid test model after one spine read, at the fixture's time.
func spineModel(t *testing.T, width int, st sources.SpineStatus) Model {
	t.Helper()
	m := gridTestModel(width, 60)
	at := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	next, _ := m.Update(spineDataMsg{Status: st, At: at})
	return next.(Model)
}

func spineIndex(t *testing.T, targets []Target) int {
	t.Helper()
	n := -1
	for i, tg := range targets {
		if tg.Spine != nil {
			if n >= 0 {
				t.Fatalf("more than one spine tile: %v", labels(targets))
			}
			n = i
		}
	}
	if n < 0 {
		t.Fatalf("no spine tile: %v", labels(targets))
	}
	return n
}

func TestSpineTileSitsBesideSessionsAndFoldsItsSession(t *testing.T) {
	m := spineModel(t, 120, spineFixture(t))
	m.sessions.Sessions = append(m.sessions.Sessions, sess("spine"))
	targets := m.gridTargets()
	eq(t, labels(targets), []string{"my-app", "scry", "spine", "dotfiles"})
	spineIndex(t, targets)
	if targets[2].Hotkey != 0 {
		t.Error("the spine tile is not a session hotkey")
	}
}

func TestSpineTileIsShownBeforeTheFirstRead(t *testing.T) {
	m := gridTestModel(120, 40)
	targets := m.gridTargets()
	tile := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 24, false, false))
	if !strings.Contains(tile, "reading") {
		t.Errorf("an unread spine tile says so:\n%s", tile)
	}
}

func TestSpineTileSummarisesTheFleet(t *testing.T) {
	m := spineModel(t, 120, spineFixture(t))
	targets := m.gridTargets()
	tile := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 40, false, false))
	for _, want := range []string{"spine", "1 needs you", "1 goal 2 agents $12.4/$30"} {
		if !strings.Contains(tile, want) {
			t.Errorf("tile missing %q:\n%s", want, tile)
		}
	}
	narrow := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 23, false, false))
	if !strings.Contains(narrow, "1g 2a $12.4/$30") {
		t.Errorf("a narrow tile abbreviates the rollup:\n%s", narrow)
	}
	compact := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 30, false, true))
	if lines := strings.Split(compact, "\n"); len(lines) != 3 || !strings.Contains(lines[1], "● spine 1") {
		t.Errorf("compact tile is one line of marker, name and count:\n%s", compact)
	}
	if strings.Contains(compact, "$") {
		t.Errorf("compact tile drops the rollup:\n%s", compact)
	}
}

func TestSpineTileEmptyAndUnreadable(t *testing.T) {
	m := spineModel(t, 120, sources.SpineStatus{Snapshot: &sources.SpineSnapshot{}})
	targets := m.gridTargets()
	if tile := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 30, false, false)); !strings.Contains(tile, "nothing needs you") {
		t.Errorf("empty fleet tile:\n%s", tile)
	}

	st := sources.GetSpineStatus(context.Background(), fixtureSpine{out: "{ nope"})
	m = spineModel(t, 120, st)
	targets = m.gridTargets()
	tile := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 40, false, false))
	if !strings.Contains(tile, "unreadable") || !strings.Contains(tile, "JSON") {
		t.Errorf("unreadable tile names its reason:\n%s", tile)
	}
	if compact := ansi.Strip(renderTile(targets[spineIndex(t, targets)], 30, false, true)); !strings.Contains(compact, "⚠ spine ?") {
		t.Errorf("unreadable compact tile:\n%s", compact)
	}
}

func TestSpinePreviewShowsFourSections(t *testing.T) {
	m := spineModel(t, 120, spineFixture(t))
	m.setGridCursor(m.gridTargets(), 2)
	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Needs you", "Allow up to $5", "Underway", "Now: payments waits on a money ask; carts merging",
		"Now: merging", "Charted next", "search v1: full-text search over notes",
		"Landed (last 24h)", "Address form validates postcodes", "rss v1: feeds for every tag",
		"notes: decisions.jsonl: permission denied",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("preview missing %q\n%s", want, view)
		}
	}

	m = spineModel(t, 120, sources.SpineStatus{Snapshot: &sources.SpineSnapshot{}})
	m.setGridCursor(m.gridTargets(), 2)
	preview := spinePreview(m.spine, m.now(), 80, 0)
	if n := strings.Count(ansi.Strip(preview), "none"); n != 4 {
		t.Errorf("every empty section says none, got %d:\n%s", n, preview)
	}
}

func TestSpinePreviewDropsOldLanded(t *testing.T) {
	st := spineFixture(t)
	now := time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC) // 22h50m after one, 26h30m after the other
	out := ansi.Strip(spinePreview(&st, now, 100, 0))
	if !strings.Contains(out, "Address form validates postcodes") || strings.Contains(out, "rss v1") {
		t.Errorf("landed keeps the last 24 hours only:\n%s", out)
	}
}

func TestSpineFeedsAttention(t *testing.T) {
	m := spineModel(t, 120, spineFixture(t))
	var found *sources.AttentionItem
	for _, it := range m.attn.report.Items {
		if it.Kind == sources.AttentionSpine {
			i := it
			found = &i
		}
	}
	if found == nil {
		t.Fatal("needs-you item must reach the queue")
	}
	row := ansi.Strip(strings.Join(m.attn.rowLines(*found, 200, false, false, m.now()), "\n"))
	for _, want := range []string{"shop", "checkout-v1", "Allow up to $5", "Open spine"} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing %q: %s", want, row)
		}
	}
	if cmd := m.attentionAction(*found); cmd == nil {
		t.Error("Enter on a spine item switches to the spine session")
	}

	m = spineModel(t, 120, sources.SpineStatus{Err: sources.ErrSpineNotFound})
	cov := ansi.Strip(strings.Join(m.attn.coverageLines(200, m.now()), "\n"))
	if !strings.Contains(cov, "spine") || !strings.Contains(cov, "not found") {
		t.Errorf("unreadable spine shows in coverage with its reason:\n%s", cov)
	}
}

func TestSpineTileEnterAndKeys(t *testing.T) {
	m := spineModel(t, 120, spineFixture(t))
	targets := m.gridTargets()
	i := spineIndex(t, targets)
	if cmd := m.enterTarget(targets, i); cmd == nil {
		t.Error("Enter on the spine tile switches to the spine session")
	}
	m.setGridCursor(targets, i)
	if cmd := m.handleGridKey(keyMsg("p")); cmd != nil || m.view != ViewGrid {
		t.Error("p on the spine tile opens nothing")
	}
	if m.gridLocalSession() != "" {
		t.Error("the spine tile is not a local session to preview or save")
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{20.000000000000004: "$20", 12.4: "$12.4", 0: "$0", 3.456: "$3.46"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestSpineTileCursorDoesNotCollideWithARepoNamedSpine(t *testing.T) {
	m := spineModel(t, 120, spineFixture(t))
	m.repos.Repos = append(m.repos.Repos, repo("spine"))
	targets := m.gridTargets()
	fleet := spineIndex(t, targets)
	repoAt := -1
	for i, tg := range targets {
		if tg.Label == "spine" && tg.Spine == nil {
			repoAt = i
		}
	}
	if repoAt < 0 {
		t.Fatalf("the spine repo needs its own tile: %v", labels(targets))
	}

	m.setGridCursor(targets, repoAt)
	if got := resolveGridCursor(m.gridTargets(), m.gridCursor, m.gridIndex); got != repoAt {
		t.Errorf("selecting the spine repo resolved to %d, want %d", got, repoAt)
	}
	if sel, _ := m.gridSelected(); sel.Spine != nil {
		t.Error("the spine repo selected the fleet tile")
	}

	m.setGridCursor(targets, fleet)
	if got := resolveGridCursor(m.gridTargets(), m.gridCursor, m.gridIndex); got != fleet {
		t.Errorf("selecting the fleet resolved to %d, want %d", got, fleet)
	}
	// A repo tile appearing ahead of it must not steal the selection.
	m.repos.Repos = append([]sources.GitRepoStatus{repo("aaa")}, m.repos.Repos...)
	if sel, _ := m.gridSelected(); sel.Spine == nil {
		t.Errorf("the fleet selection moved to %q", sel.Label)
	}
}

func TestSpinePreviewStripsControlSequences(t *testing.T) {
	esc := "\x1b]0;pwned\x07\x1b[2J"
	st := sources.SpineStatus{Snapshot: &sources.SpineSnapshot{
		NeedsYou: []sources.SpineItem{{Repo: "shop" + esc, Goal: "g" + esc, Stream: "s" + esc, Title: "ask" + esc}},
		Underway: []sources.SpineItem{{Repo: "shop", Goal: "g", Kind: "goal", Title: "t", Now: "now" + esc}},
		Errors:   []string{"notes: broken" + esc},
	}}
	out := spinePreview(&st, time.Now(), 200, 0)
	if strings.Contains(out, "pwned") || strings.Contains(out, "\x1b]") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x07") {
		t.Errorf("preview passes control sequences through: %q", out)
	}

	bad := sources.SpineStatus{Err: errors.New("spine bearings: exit 1: oops" + esc)}
	for _, s := range []string{spinePreview(&bad, time.Now(), 200, 0), func() string { a, b := spineTileLines(&bad, 60); return a + b }()} {
		if strings.Contains(s, "pwned") || strings.Contains(s, "\x1b]") || strings.Contains(s, "\x1b[2J") {
			t.Errorf("unreadable reason passes control sequences through: %q", s)
		}
	}
}

// At an ordinary 120x24 terminal the preview panel has a handful of rows; the
// four sections must all still be on screen, full fleet or empty.
func TestSpinePreviewFitsA120x24Terminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   sources.SpineStatus
		want []string
	}{
		{"fixture", spineFixture(t), []string{"Needs you (1)", "Underway (", "Charted next (", "Landed (last 24h) ("}},
		{"empty", sources.SpineStatus{Snapshot: &sources.SpineSnapshot{}}, []string{"Needs you  none", "Underway  none", "Charted next  none", "Landed (last 24h)  none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := spineModel(t, 120, tc.st)
			next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
			m = next.(Model)
			targets := m.gridTargets()
			m.setGridCursor(targets, spineIndex(t, targets))
			view := ansi.Strip(m.View())
			if n := len(strings.Split(view, "\n")); n > 24 {
				t.Errorf("view is %d rows, taller than the terminal", n)
			}
			for _, want := range tc.want {
				if !strings.Contains(view, want) {
					t.Errorf("preview missing %q at 120x24\n%s", want, view)
				}
			}
		})
	}
}

func TestSpineFittedPreviewSharesRowsAndSummarisesWhenTiny(t *testing.T) {
	st := spineFixture(t)
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	for rows := 1; rows <= 12; rows++ {
		out := ansi.Strip(spinePreview(&st, now, 120, rows))
		if n := len(strings.Split(out, "\n")); n > rows {
			t.Errorf("%d rows: rendered %d lines\n%s", rows, n, out)
		}
		for _, want := range []string{"Needs you", "Underway", "Charted next", "Landed"} {
			if !strings.Contains(out, want) {
				t.Errorf("%d rows: missing %q\n%s", rows, want, out)
			}
		}
	}
	if out := ansi.Strip(spinePreview(&st, now, 120, 8)); !strings.Contains(out, "Allow up to $5") || !strings.Contains(out, "search v1") {
		t.Errorf("spare rows go to every section in turn:\n%s", out)
	}
}
