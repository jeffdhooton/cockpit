package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/sources"
)

// attentionModel is the queue view: the derived report, the selection held
// by item id, the tab, the filter, and the detail page when one is open.
type attentionModel struct {
	report  sources.AttentionReport
	tracker sources.AttentionTracker
	tab     int // 0 needs attention, 1 housekeeping
	// selected is the id of the selected row. It survives refresh: if the
	// item is still there the cursor follows it; if it resolved, resolved
	// holds it as a placeholder until the next navigation key, so a queued
	// Enter cannot act on the row that moved into its place.
	selected string
	resolved *sources.AttentionItem
	index    int
	filter   textinput.Model
	query    string
	// detail is the open detail page, or nil.
	detail *sources.AttentionItem
	// message is a transient line shown under the list.
	message string
	scroll  int
	// lastRows is the previous render's rows, kept so a resolved
	// placeholder can still show what it was.
	lastRows []sources.AttentionItem
}

func newAttentionModel() attentionModel {
	f := textinput.New()
	f.Placeholder = "filter host, project or kind"
	f.CharLimit = 64
	f.Width = 30
	return attentionModel{filter: f}
}

// refresh replaces the report, stamps first-observed times, and re-resolves
// the selection.
func (a *attentionModel) refresh(rep sources.AttentionReport, now time.Time) {
	a.tracker.Track(&rep, now)
	a.report = rep
	a.resolveSelection()
}

// rows is the visible list for the current tab and filter.
func (a *attentionModel) rows() []sources.AttentionItem {
	var out []sources.AttentionItem
	for _, i := range a.report.Items {
		if i.Housekeeping != (a.tab == 1) {
			continue
		}
		out = append(out, i)
	}
	return sources.FilterAttention(out, a.query)
}

// count is how many items sit on the housekeeping (or urgent) tab.
func (a *attentionModel) count(housekeeping bool) int {
	n := 0
	for _, i := range a.report.Items {
		if i.Housekeeping == housekeeping {
			n++
		}
	}
	return n
}

// resolveSelection finds the selected id in the current rows. A vanished
// selection becomes a placeholder rather than sliding onto a neighbour.
func (a *attentionModel) resolveSelection() {
	rows := a.rows()
	if a.selected == "" && a.resolved == nil {
		if len(rows) > 0 {
			a.selected = rows[0].ID
		}
		a.index = 0
		return
	}
	for i, r := range rows {
		if r.ID == a.selected {
			a.index = i
			a.resolved = nil
			return
		}
	}
	if a.resolved == nil && a.selected != "" {
		// Keep what it looked like so the placeholder can name it.
		a.resolved = &sources.AttentionItem{ID: a.selected, Title: a.selected, Detail: "resolved"}
		for _, prev := range a.lastRows {
			if prev.ID == a.selected {
				item := prev
				a.resolved = &item
			}
		}
	}
	if a.index > len(rows) {
		a.index = len(rows)
	}
}

// lastRows is retained so a resolved placeholder can still show its title.
func (a *attentionModel) remember() { a.lastRows = a.rows() }

func (a *attentionModel) select_(id string) {
	a.selected = id
	a.resolved = nil
	a.resolveSelection()
}

// move steps the selection. Any navigation key dismisses a placeholder.
func (a *attentionModel) move(delta int) {
	rows := a.rows()
	if a.resolved != nil {
		a.resolved = nil
		// The placeholder occupied index; land on the row now at that
		// position, clamped.
		a.index = clampInt(a.index, 0, len(rows)-1)
		if len(rows) > 0 {
			a.selected = rows[a.index].ID
		} else {
			a.selected = ""
		}
		return
	}
	if len(rows) == 0 {
		a.selected = ""
		a.index = 0
		return
	}
	a.index = clampInt(a.index+delta, 0, len(rows)-1)
	a.selected = rows[a.index].ID
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// current is the selected item, or nil when the selection is a placeholder
// or the list is empty.
func (a *attentionModel) current() *sources.AttentionItem {
	if a.resolved != nil {
		return nil
	}
	for _, r := range a.rows() {
		if r.ID == a.selected {
			item := r
			return &item
		}
	}
	return nil
}

func (a *attentionModel) switchTab() {
	a.tab = 1 - a.tab
	a.resolved = nil
	a.selected = ""
	a.index = 0
	a.resolveSelection()
}

// actionLabel is the primary action's name as shown on the row.
func actionLabel(i sources.AttentionItem) string {
	switch i.Action {
	case sources.ActionOpenAgent:
		return "Open agent"
	case sources.ActionOpenSession:
		return "Open session"
	case sources.ActionInspectProcess:
		return "Inspect process"
	case sources.ActionViewCI:
		return "View check details"
	case sources.ActionViewGateway:
		return "Gateway details"
	case sources.ActionOpenProject:
		return "Project details"
	case sources.ActionOpenSpine:
		return "Open spine"
	}
	return ""
}

func ageOf(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// attentionBadge is the persistent hint text: fresh actionable items, and
// separately how many sources could not be read.
func (a *attentionModel) badge() string {
	s := fmt.Sprintf("Attention %d", a.report.Actionable())
	if a.report.Unavailable > 0 {
		s += fmt.Sprintf(" · %d unavailable", a.report.Unavailable)
	}
	return s
}

// view renders the queue into width × height cells.
func (a *attentionModel) view(width, height int, now time.Time, filtering bool) string {
	if a.detail != nil {
		return a.detailView(width, height, now)
	}
	narrow := width < MobileMaxWidth
	inner := width - 4
	if inner < 10 {
		inner = 10
	}

	needs := fmt.Sprintf("Needs attention %d", a.report.Actionable())
	house := fmt.Sprintf("Housekeeping %d", a.count(true))
	unavailable := fmt.Sprintf("%d unavailable", a.report.Unavailable)
	if narrow {
		needs = fmt.Sprintf("Needs %d", a.report.Actionable())
		house = fmt.Sprintf("Housekeep %d", a.count(true))
		unavailable = fmt.Sprintf("%d unavail", a.report.Unavailable)
	}
	var header string
	if a.tab == 0 {
		header = AccentText.Bold(true).Render(needs) + MutedText.Render(" · "+house)
	} else {
		header = MutedText.Render(needs+" · ") + AccentText.Bold(true).Render(house)
	}
	if a.report.Unavailable > 0 {
		right := WarningText.Render(unavailable)
		if pad := inner - lipgloss.Width(header) - lipgloss.Width(right); pad > 0 {
			header += strings.Repeat(" ", pad) + right
		} else {
			header += " " + right
		}
	}

	var lines []string
	lines = append(lines, clip(header, inner))
	if filtering || a.query != "" {
		lines = append(lines, "  / "+a.filter.View())
	}

	rows := a.rows()
	rowH := 1
	if narrow {
		rowH = 2
	}
	// Budget: header, optional filter, coverage block, hints handled outside.
	coverage := a.coverageLines(inner, now)
	avail := height - len(lines) - len(coverage) - 1
	if avail < rowH {
		avail = rowH
	}
	visible := avail / rowH
	if visible < 1 {
		visible = 1
	}
	if a.index < a.scroll {
		a.scroll = a.index
	}
	if a.index >= a.scroll+visible {
		a.scroll = a.index - visible + 1
	}

	if len(rows) == 0 && a.resolved == nil {
		lines = append(lines, MutedText.Render("  No attention items observed"))
	}
	end := a.scroll + visible
	if end > len(rows) {
		end = len(rows)
	}
	for i := a.scroll; i < end; i++ {
		r := rows[i]
		selected := i == a.index && a.resolved == nil
		if a.resolved != nil && i == a.index {
			lines = append(lines, a.placeholderLines(inner, narrow)...)
		}
		lines = append(lines, a.rowLines(r, inner, narrow, selected, now)...)
	}
	if a.resolved != nil && a.index >= end {
		lines = append(lines, a.placeholderLines(inner, narrow)...)
	}
	if end < len(rows) {
		lines = append(lines, MutedText.Render(fmt.Sprintf("  ▼ %d more", len(rows)-end)))
	}
	if a.message != "" {
		lines = append(lines, WarningText.Render("  "+clip(a.message, inner-2)))
	}
	lines = append(lines, coverage...)
	return strings.Join(lines, "\n")
}

func (a *attentionModel) placeholderLines(inner int, narrow bool) []string {
	title := "resolved"
	if a.resolved != nil && a.resolved.Title != "" {
		title = clean(a.resolved.Title)
	}
	line := RowCursor(true) + MutedText.Render(clip(title+" — resolved; press a key to continue", inner-2))
	if narrow {
		return []string{line, "    " + MutedText.Render("no action")}
	}
	return []string{line}
}

func (a *attentionModel) rowLines(r sources.AttentionItem, inner int, narrow, selected bool, now time.Time) []string {
	title := clean(r.Title)
	detail := clean(r.Detail)
	if r.Kind == sources.AttentionSpine && r.Project != "" {
		// A spine ask names its repository too: goals share names across repos.
		detail = clean(r.Project) + " · " + detail
	}
	age := ageOf(r.FirstObserved, now)
	action := actionLabel(r)
	style := lipgloss.NewStyle().Foreground(ColorFg)
	detailStyle := WarningText
	if r.Housekeeping {
		detailStyle = MutedText
	}
	if r.Observation != sources.ObservationFresh {
		style = MutedText
		detailStyle = MutedText
		detail += " (stale)"
		action = "unavailable"
	}
	if selected {
		style = style.Foreground(ColorAccent).Bold(true)
	}
	cursor := RowCursor(selected)
	if narrow {
		// Two lines: host/project on the first, action and age on the
		// second, so both stay visible however narrow the screen.
		first := cursor + style.Render(clip(title, inner-2-len(age)-1))
		if age != "" {
			first = padRight(first, inner-len(age)) + MutedText.Render(age)
		}
		second := "    " + detailStyle.Render(clip(detail, inner-4))
		third := "    " + AccentText.Render(clip(action, inner-4))
		return []string{first, second + " " + third[4:]}
	}
	titleW := inner * 2 / 5
	if titleW < 12 {
		titleW = 12
	}
	// Budget: cursor 2 + title + 2 + detail + 2 + age + 2 + action.
	detailW := inner - 2 - titleW - 2 - 2 - len(age) - 2 - lipgloss.Width(action)
	if detailW < 6 {
		detailW = 6
	}
	line := cursor + padRight(style.Render(clip(title, titleW)), titleW) + "  " +
		padRight(detailStyle.Render(clip(detail, detailW)), detailW) + "  " +
		padRight(MutedText.Render(age), len(age)+2) + AccentText.Render(action)
	return []string{clip(line, inner)}
}

// coverageLines renders the sources that could not be read, and the
// limited-precision notes. It is never filtered away.
func (a *attentionModel) coverageLines(inner int, now time.Time) []string {
	var out []string
	for _, c := range a.report.Coverage {
		if c.Observation == sources.ObservationFresh && !strings.Contains(c.Detail, "session level") && !strings.Contains(c.Detail, "not checked") {
			continue
		}
		label := c.Scope
		if label == "" {
			label = c.Source
		}
		age := ageOf(c.ObservedAt, now)
		text := clean(c.Detail)
		if c.Observation == sources.ObservationUnavailable && age != "" {
			text += " — last checked " + age + " ago"
		}
		style := WarningText
		if c.Observation == sources.ObservationFresh {
			style = MutedText
		}
		out = append(out, "  "+style.Render(clip(label+"  "+text, inner-2)))
	}
	if len(out) > 0 {
		out = append([]string{""}, out...)
	}
	return out
}

// detailView renders one item's details full-screen.
func (a *attentionModel) detailView(width, height int, now time.Time) string {
	i := a.detail
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	var lines []string
	lines = append(lines, SectionLabel(clean(i.Title), true))
	add := func(k, v string) {
		if v == "" {
			return
		}
		for n, l := range wrap(clean(v), inner-14) {
			key := padRight(MutedText.Render(k), 12)
			if n > 0 {
				key = strings.Repeat(" ", 12)
			}
			lines = append(lines, "  "+key+l)
		}
	}
	add("kind", string(i.Kind))
	add("host", hostLabel(i.Host))
	add("project", i.Project)
	add("detail", i.Detail)
	add("observed", string(i.Observation)+", "+ageOf(i.ObservedAt, now)+" ago")
	add("first seen", ageOf(i.FirstObserved, now)+" ago (this Cockpit instance)")
	add("source", i.Source)
	if i.Coverage != "" {
		add("coverage", i.Coverage)
	}
	t := i.Target
	switch t.Type {
	case "ci":
		add("repository", t.Repo)
		add("branch", t.Branch)
		add("run id", t.RunID)
		add("run url", t.RunURL)
		if validRunURL(t.RunURL) {
			lines = append(lines, "", "  "+AccentText.Render("o")+" "+MutedText.Render("open run in browser"))
		} else if t.RunURL != "" {
			lines = append(lines, "", "  "+MutedText.Render("url not openable here; copy it from above"))
		}
	case "hermes":
		add("gateway", i.Detail)
		add("last check", ageOf(i.ObservedAt, now)+" ago")
		lines = append(lines, "", "  "+MutedText.Render("Open the host's grid to reach a shell; nothing is started from here."))
	case "session", "pane":
		add("session", t.Session)
		if t.SessionID != "" {
			add("session id", t.SessionID+" on "+t.Generation)
		}
		add("window", t.WindowID)
		add("pane", t.PaneID)
		if i.Actionable() {
			lines = append(lines, "", "  "+AccentText.Render("Enter")+" "+MutedText.Render("attach to the existing session (creates nothing)"))
		}
	case "project":
		add("session", t.Session)
		add("branch", t.Branch)
		lines = append(lines, "", "  "+AccentText.Render("Enter")+" "+MutedText.Render("attach if a session exists (creates nothing)"))
	case "process":
		add("process", t.Process)
		add("window", t.WindowID)
	case "spine":
		add("goal", t.Goal)
		add("stream", t.Stream)
		add("agent", t.Agent)
		lines = append(lines, "", "  "+AccentText.Render("Enter")+" "+MutedText.Render("switch to the spine session; answer there"))
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	return strings.Join(lines, "\n")
}

// itemTarget converts an item into an attach target.
func itemTarget(i sources.AttentionItem) sources.AttachTarget {
	t := i.Target
	return sources.AttachTarget{Host: t.Host, Generation: t.Generation, Session: t.Session, SessionID: t.SessionID, WindowID: t.WindowID, PaneID: t.PaneID}
}
