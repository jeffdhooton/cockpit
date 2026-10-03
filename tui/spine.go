package tui

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/sources"
)

// spineSession is the tmux session the spine tile and spine attention items
// switch to. A local session of this name is folded into the spine tile.
const spineSession = "spine"

// spineCursor is the grid cursor's name for the spine tile. No label holds a
// NUL, so a repo or session called spine never lands on the fleet tile.
const spineCursor = "\x00spine"

// spineDataMsg carries one read of `spine bearings --json`.
type spineDataMsg struct {
	Status sources.SpineStatus
	At     time.Time
}

// fetchSpine reads the spine fleet snapshot. It runs on the local cadence:
// at startup, on every local tick and on refresh. The read is the only
// spine command Cockpit itself ever runs.
func (m Model) fetchSpine() tea.Cmd {
	now := m.now
	return func() tea.Msg {
		st := sources.GetSpineStatus(context.Background(), sources.LocalSpineRunner{})
		return spineDataMsg{Status: st, At: now()}
	}
}

// spineTarget is the grid's one spine tile, before or after the first read.
func (m Model) spineTarget() Target {
	st := sources.SpineStatus{}
	if m.spine != nil {
		st = *m.spine
	}
	return Target{Label: spineSession, Spine: &st}
}

// money renders a dollar amount without trailing zeros: $20, $12.4.
func money(v float64) string {
	return "$" + strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// spineMarker is the spine tile's state in one cell: a warning when the
// snapshot is unreadable or something needs you, a ring before the first read.
func spineMarker(st *sources.SpineStatus) string {
	glyph, v := "●", VariantAccent
	switch {
	case st.Snapshot == nil && st.Err == nil:
		glyph, v = "○", VariantMuted
	case !st.Readable():
		glyph, v = "⚠", VariantWarning
	case st.NeedsYouCount() > 0:
		v = VariantWarning
	}
	return lipgloss.NewStyle().Foreground(variantColor(v)).Render(glyph)
}

// spineCompactCount is what the one-line phone tile shows after the name.
func spineCompactCount(st *sources.SpineStatus) string {
	switch {
	case st.Snapshot == nil && st.Err == nil:
		return MutedText.Render("…")
	case !st.Readable():
		return WarningText.Render("?")
	case st.NeedsYouCount() > 0:
		return WarningText.Render(strconv.Itoa(st.NeedsYouCount()))
	}
	return MutedText.Render("0")
}

// spineTileLines are the spine tile's status and detail lines: what needs
// you, then the underway goals, agents and spend against cap. Unreadable
// says why instead.
func spineTileLines(st *sources.SpineStatus, inner int) (string, string) {
	switch {
	case st.Snapshot == nil && st.Err == nil:
		return StatusRing(Truncate("reading", inner-2), VariantMuted), ""
	case !st.Readable():
		reason := "no snapshot"
		if st.Err != nil {
			// The tile already names spine; keep the cells for the why.
			reason = clean(strings.TrimPrefix(st.Err.Error(), "spine bearings "))
		}
		return WarningText.Render("⚠ " + Truncate("unreadable", inner-2)), MutedText.Render(clip(reason, inner))
	}
	status := StatusDot(Truncate("nothing needs you", inner-2), VariantAccent)
	if n := st.NeedsYouCount(); n > 0 {
		status = StatusDot(Truncate(fmt.Sprintf("%d needs you", n), inner-2), VariantWarning)
	}
	g, a := st.UnderwayGoalCount(), st.UnderwayAgentCount()
	spend := money(st.UnderwaySpent()) + "/" + money(st.UnderwayCap())
	detail := fmt.Sprintf("%d %s %d %s %s", g, plural(g, "goal"), a, plural(a, "agent"), spend)
	if len(detail) > inner {
		detail = fmt.Sprintf("%dg %da %s", g, a, spend)
	}
	return status, MutedText.Render(clip(detail, inner))
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// spinePreview renders the snapshot's four sections for the preview panel.
func spinePreview(st *sources.SpineStatus, now time.Time, width int) string {
	if st == nil || (st.Snapshot == nil && st.Err == nil) {
		return MutedText.Render("reading spine bearings…")
	}
	if !st.Readable() {
		reason := "no snapshot"
		if st.Err != nil {
			reason = clean(st.Err.Error())
		}
		return WarningText.Render(clip("⚠ spine unreadable: "+reason, width))
	}
	snap := st.Snapshot
	var lines []string
	section := func(title string, items []sources.SpineItem, now bool) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, BoldText.Render(title))
		if len(items) == 0 {
			lines = append(lines, "  "+MutedText.Render("none"))
			return
		}
		for _, it := range items {
			lines = append(lines, "  "+clip(spineItemLine(it), width-2))
			if now && it.Now != "" {
				lines = append(lines, "    "+MutedText.Render(clip("Now: "+clean(it.Now), width-4)))
			}
		}
	}
	section("Needs you", snap.NeedsYou, false)
	section("Underway", snap.Underway, true)
	section("Charted next", snap.ChartedNext, false)
	var landed []sources.SpineItem
	for _, it := range snap.Landed {
		if it.At.IsZero() || now.Sub(it.At) <= 24*time.Hour {
			landed = append(landed, it)
		}
	}
	section("Landed (last 24h)", landed, false)
	if len(snap.Errors) > 0 {
		lines = append(lines, "")
		for _, e := range snap.Errors {
			lines = append(lines, WarningText.Render(clip("⚠ "+clean(e), width)))
		}
	}
	return strings.Join(lines, "\n")
}

// spineItemLine names an item's repository, goal and stream before its title.
func spineItemLine(it sources.SpineItem) string {
	where := clean(it.Repo)
	if it.Goal != "" {
		where += "/" + clean(it.Goal)
	}
	if it.Stream != "" {
		where += " " + clean(it.Stream)
	}
	line := AccentText.Render(where) + " · " + clean(it.Title)
	if it.Kind == "goal" && it.Cap > 0 {
		line += MutedText.Render(" " + money(it.Spent) + "/" + money(it.Cap))
	}
	return line
}

// openSpine switches the client to the spine session, creating it with the
// configured command when it is absent. It changes nothing in spine itself:
// the session runs whatever spine.command says, as a person would.
func (m Model) openSpine() tea.Cmd {
	command := m.config.Spine.SessionCommand()
	return func() tea.Msg { return tmuxSwitchResultMsg{Err: tmuxJumpSpine(command)} }
}

func tmuxJumpSpine(command string) error {
	ctx := context.Background()
	r := sources.DefaultRunner()
	if _, err := r.Run(ctx, sources.HasSessionArgs("="+spineSession)...); err != nil {
		if err := exec.Command("tmux", "new-session", "-d", "-s", spineSession, command).Run(); err != nil {
			return fmt.Errorf("start spine session: %w", err)
		}
	}
	return tmuxSwitch(spineSession)
}
