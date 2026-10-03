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
	preview := spinePreview(m.spine, m.now(), 80, 0, 0)
	if n := strings.Count(ansi.Strip(preview), "none"); n != 4 {
		t.Errorf("every empty section says none, got %d:\n%s", n, preview)
	}
}

func TestSpinePreviewDropsOldLanded(t *testing.T) {
	st := spineFixture(t)
	now := time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC) // 22h50m after one, 26h30m after the other
	out := ansi.Strip(spinePreview(&st, now, 100, 0, 0))
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
	out := spinePreview(&st, time.Now(), 200, 0, 0)
	if strings.Contains(out, "pwned") || strings.Contains(out, "\x1b]") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x07") {
		t.Errorf("preview passes control sequences through: %q", out)
	}

	bad := sources.SpineStatus{Err: errors.New("spine bearings: exit 1: oops" + esc)}
	for _, s := range []string{spinePreview(&bad, time.Now(), 200, 0, 0), func() string { a, b := spineTileLines(&bad, 60); return a + b }()} {
		if strings.Contains(s, "pwned") || strings.Contains(s, "\x1b]") || strings.Contains(s, "\x1b[2J") {
			t.Errorf("unreadable reason passes control sequences through: %q", s)
		}
	}
}

// spineAt120x24 is the root model at an ordinary 120x24 terminal after one
// spine read, with the spine tile selected by walking the grid with keys.
func spineAt120x24(t *testing.T, st sources.SpineStatus) Model {
	t.Helper()
	m := spineModel(t, 120, st)
	m = press(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	for i := 0; i < 20; i++ {
		if sel, ok := m.gridSelected(); ok && sel.Spine != nil {
			return m
		}
		m = press(t, m, runes("l"))
	}
	t.Fatalf("the l key never reached the spine tile\n%s", ansi.Strip(m.View()))
	return m
}

func press(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func runes(k string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }

func viewAt120x24(t *testing.T, m Model) string {
	t.Helper()
	view := ansi.Strip(m.View())
	if n := len(strings.Split(view, "\n")); n > 24 {
		t.Fatalf("view is %d rows, taller than the terminal\n%s", n, view)
	}
	return view
}

// At 120x24 the preview panel has a handful of rows. Every section heading
// stays named on its summary line, and J walks the window down until every
// item and its Now line, the second Underway item included, has been on
// screen; K walks back to the top.
func TestSpinePreviewScrollsEveryItemIntoViewAt120x24(t *testing.T) {
	m := spineAt120x24(t, spineFixture(t))
	want := []string{
		"Needs you", "Allow up to $5 for the sandbox payment API?",
		"Underway", "checkout v1: one-page checkout with saved carts", "Now: payments waits on a money ask; carts merging",
		"Saved carts survive sign-out", "Now: merging",
		"Charted next", "search v1: full-text search over notes",
		"Landed (last 24h)", "Address form validates postcodes", "rss v1: feeds for every tag",
		"notes: decisions.jsonl: permission denied",
	}
	view := viewAt120x24(t, m)
	if strings.Contains(view, "Saved carts survive sign-out") {
		t.Fatalf("fixture fits at 120x24; the test no longer proves scrolling\n%s", view)
	}
	for _, h := range []string{"Needs you 1", "Underway 2", "Charted next 1", "Landed (last 24h) 2", "↓", "J/K scroll fleet"} {
		if !strings.Contains(view, h) {
			t.Errorf("top of the scroll missing %q\n%s", h, view)
		}
	}
	seen := map[string]bool{}
	var last string
	for i := 0; i < 40 && view != last; i++ {
		for _, w := range want {
			if strings.Contains(view, w) {
				seen[w] = true
			}
		}
		last = view
		m = press(t, m, runes("J"))
		view = viewAt120x24(t, m)
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("scrolling never showed %q", w)
		}
	}
	if strings.Contains(view, "more ·") || !strings.Contains(view, "↑") {
		t.Errorf("the bottom must say what is above and nothing below\n%s", view)
	}
	// The offset is clamped: more J at the bottom changes nothing.
	bottom := m.spineScroll
	m = press(t, m, runes("J"))
	if m.spineScroll != bottom {
		t.Errorf("scrolled past the bottom: %d, then %d", bottom, m.spineScroll)
	}
	for i := 0; i < 40; i++ {
		m = press(t, m, runes("K"))
	}
	if m.spineScroll != 0 || !strings.Contains(viewAt120x24(t, m), "Allow up to $5") {
		t.Errorf("K must return to the top, at %d\n%s", m.spineScroll, viewAt120x24(t, m))
	}
	m = press(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.spineScroll == 0 {
		t.Error("pgdown did not scroll the spine preview")
	}
}

func TestSpinePreviewEmptyFleetSaysNoneInEverySectionAt120x24(t *testing.T) {
	m := spineAt120x24(t, sources.SpineStatus{Snapshot: &sources.SpineSnapshot{}})
	seen := strings.Builder{}
	for i := 0; i < 20; i++ {
		seen.WriteString(viewAt120x24(t, m))
		m = press(t, m, runes("J"))
	}
	view := seen.String()
	for _, h := range []string{"Needs you", "Underway", "Charted next", "Landed (last 24h)"} {
		if !strings.Contains(view, h+" none") {
			t.Errorf("summary must say %s none\n%s", h, view)
		}
	}
	if n := strings.Count(view, "  none"); n < 4 {
		t.Errorf("each empty section must say none under its heading, saw %d\n%s", n, view)
	}
}

func TestSpinePreviewScrollResetsOffTheTileAndWhenTheSnapshotShrinks(t *testing.T) {
	m := spineAt120x24(t, spineFixture(t))
	m = press(t, m, runes("J"))
	m = press(t, m, runes("J"))
	if m.spineScroll != 2 {
		t.Fatalf("two J scrolled to %d", m.spineScroll)
	}
	m = press(t, m, runes("h"))
	if m.spineScroll != 0 {
		t.Errorf("leaving the spine tile kept offset %d", m.spineScroll)
	}
	// Off the spine tile, J and K do nothing.
	m = press(t, m, runes("J"))
	if m.spineScroll != 0 {
		t.Errorf("J off the spine tile scrolled to %d", m.spineScroll)
	}

	m = press(t, m, runes("l"))
	m = press(t, m, runes("J"))
	m = press(t, m, runes("J"))
	smaller := spineFixture(t)
	smaller.Snapshot.Landed = nil
	m = press(t, m, spineDataMsg{Status: smaller, At: m.now()})
	if m.spineScroll != 0 {
		t.Errorf("a shorter snapshot kept offset %d", m.spineScroll)
	}
}

// A tall panel shows the whole layout with no summary or scroll line, so
// what a big terminal sees is unchanged.
func TestSpinePreviewUnscrolledWhenItFits(t *testing.T) {
	st := spineFixture(t)
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	out := ansi.Strip(spinePreview(&st, now, 120, 97, 5))
	if out != ansi.Strip(spinePreview(&st, now, 120, 0, 0)) || strings.Contains(out, "scroll") {
		t.Errorf("a panel the layout fits must show it whole:\n%s", out)
	}
	for rows := 1; rows <= 12; rows++ {
		for off := 0; off <= 20; off++ {
			got := ansi.Strip(spinePreview(&st, now, 120, rows, off))
			if n := len(strings.Split(got, "\n")); n > rows {
				t.Errorf("%d rows at %d: rendered %d lines\n%s", rows, off, n, got)
			}
			for _, h := range []string{"Needs you", "Underway", "Charted next", "Landed"} {
				if !strings.Contains(got, h) {
					t.Errorf("%d rows at %d: missing %q\n%s", rows, off, h, got)
				}
			}
		}
	}
}
