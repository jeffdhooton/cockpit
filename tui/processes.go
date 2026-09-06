package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/process"
	"github.com/jeffdhooton/cockpit/sources"
)

// processesModel is the process panel for one project (or one unmanaged
// session, inspection-only). Everything it shows was read; nothing it does
// on open starts or reconciles anything.
type processesModel struct {
	key       string // project key, or "" for an unmanaged session
	host      string
	label     string
	unmanaged bool

	obs        *sources.ProcessObservation
	err        string
	observedAt time.Time
	loading    bool
	seq        int // request sequence; a reply with an older seq is dropped

	selected string // row key; survives refresh
	index    int
	scroll   int
	pending  map[string]string // row key → verb in flight
	confirm  *procConfirm
	output   procOutput
	fullOut  bool
	showCmd  bool
	message  string
}

// procConfirm is the scoped confirmation for a destructive action. It
// defaults to Cancel; the user has to move to the action to take it.
type procConfirm struct {
	kind     string // stop, restart, adopt
	process  string
	windowID string
	detail   string
	armed    bool
}

// procOutput is the bounded output read for the selected row.
type procOutput struct {
	window   string
	windowID string
	text     string
	lines    int
	seq      int
	loading  bool
	scroll   int // first visible line; -1 means follow the tail
	err      string
}

type (
	procObsMsg struct {
		key string
		seq int
		obs *sources.ProcessObservation
		err error
	}
	procOutputMsg struct {
		key      string
		window   string
		windowID string
		seq      int
		text     string
		lines    int
		err      error
	}
	procActionMsg struct {
		key     string
		process string
		res     process.Result
	}
)

func newProcessesModel() processesModel {
	return processesModel{pending: map[string]string{}, output: procOutput{scroll: -1}}
}

// open points the panel at a project (or session) and clears state that
// belonged to the previous one. The selection is preserved when the same
// project is reopened.
func (p *processesModel) open(key, host, label string, unmanaged bool, selectProcess string) {
	if p.key != key || p.host != host || p.label != label {
		p.selected = ""
		p.index = 0
		p.scroll = 0
		p.obs = nil
		p.err = ""
		p.output = procOutput{scroll: -1}
	}
	p.key, p.host, p.label, p.unmanaged = key, host, label, unmanaged
	p.loading = true
	p.confirm = nil
	p.fullOut = false
	p.message = ""
	if selectProcess != "" {
		p.selected = selectProcess
	}
}

// procRow is one line of the panel.
type procRow struct {
	key    string
	info   sources.ProcessInfo
	other  bool // under "Other windows"
	policy string
}

func rowKey(info sources.ProcessInfo) string {
	if info.Configured {
		return info.Name
	}
	if info.WindowID != "" {
		return "win:" + info.WindowID
	}
	return "win:" + info.Name
}

func (p *processesModel) rows() []procRow {
	if p.obs == nil {
		return nil
	}
	var rows []procRow
	for _, info := range p.obs.Processes {
		if !info.Configured {
			continue
		}
		policy := "manual"
		if info.AutoStart {
			policy = "starts with project"
		}
		rows = append(rows, procRow{key: rowKey(info), info: info, policy: policy})
	}
	for _, info := range p.obs.Processes {
		if info.Configured {
			continue
		}
		rows = append(rows, procRow{key: rowKey(info), info: info, other: true, policy: "unmanaged"})
	}
	return rows
}

func (p *processesModel) resolveSelection() {
	rows := p.rows()
	for i, r := range rows {
		if r.key == p.selected {
			p.index = i
			return
		}
	}
	if len(rows) == 0 {
		p.index = 0
		p.selected = ""
		return
	}
	p.index = clampInt(p.index, 0, len(rows)-1)
	p.selected = rows[p.index].key
}

func (p *processesModel) current() *procRow {
	rows := p.rows()
	for i := range rows {
		if rows[i].key == p.selected {
			return &rows[i]
		}
	}
	return nil
}

func (p *processesModel) move(delta int) {
	rows := p.rows()
	if len(rows) == 0 {
		return
	}
	p.index = clampInt(p.index+delta, 0, len(rows)-1)
	p.selected = rows[p.index].key
	p.output = procOutput{scroll: -1}
}

// apply folds an observation reply in, ignoring one for another project or
// an older request.
func (p *processesModel) apply(msg procObsMsg) bool {
	if msg.key != p.key || msg.seq < p.seq {
		return false
	}
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return true
	}
	p.err = ""
	p.obs = msg.obs
	p.observedAt = msg.obs.ObservedAt
	p.resolveSelection()
	return true
}

func (p *processesModel) applyOutput(msg procOutputMsg) bool {
	if msg.key != p.key || msg.seq < p.output.seq {
		return false
	}
	cur := p.current()
	if cur == nil || cur.info.WindowID != msg.windowID {
		// The selection moved since this read was requested.
		return false
	}
	p.output.loading = false
	p.output.window = msg.window
	p.output.windowID = msg.windowID
	p.output.lines = msg.lines
	if msg.err != nil {
		p.output.err = msg.err.Error()
		return true
	}
	p.output.err = ""
	p.output.text = trimPadding(sources.StripControl(msg.text))
	return true
}

// actions lists what the selected row can do right now.
func (p *processesModel) actions() []string {
	cur := p.current()
	if cur == nil || p.unmanaged {
		return nil
	}
	if _, busy := p.pending[cur.key]; busy {
		return []string{"working…"}
	}
	info := cur.info
	var out []string
	if info.WindowID != "" || info.WindowIndex >= 0 {
		out = append(out, "Enter attach", "l output")
	}
	if !info.Configured {
		return out
	}
	switch {
	case info.Adoptable:
		out = append(out, "A adopt")
	case info.Outcome == sources.OutcomeRunning && info.Controllable():
		out = append(out, "x stop", "R restart")
	case info.Outcome == sources.OutcomeRunning:
		// Split or ambiguous: inspection only.
	case info.Outcome == sources.OutcomeExited || info.Outcome == sources.OutcomeCompleted:
		if info.Controllable() {
			out = append(out, "s start")
		}
	default:
		out = append(out, "s start")
	}
	return out
}

func (p *processesModel) can(action string) bool {
	for _, a := range p.actions() {
		if strings.HasPrefix(a, action+" ") {
			return true
		}
	}
	return false
}

// view renders the panel.
func (p *processesModel) view(width, height int, now time.Time) string {
	if p.fullOut {
		return p.outputView(width, height, true)
	}
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	narrow := width < MobileMaxWidth

	title := "Processes · " + hostLabel(p.host) + "/" + clean(p.label)
	state := SuccessText.Render("Connected")
	switch {
	case p.loading && p.obs == nil:
		state = MutedText.Render("Reading…")
	case p.err != "":
		state = WarningText.Render("Unavailable")
	}
	header := SectionLabel(clip(title, inner-12), true)
	header = padRight(header, inner-lipgloss.Width(state)) + state

	var lines []string
	lines = append(lines, header)
	if p.err != "" {
		lines = append(lines, WarningText.Render("  "+clip("Last read failed: "+p.err, inner-2)))
		if p.obs != nil {
			lines = append(lines, MutedText.Render("  showing last-known state from "+ageOf(p.observedAt, now)+" ago; actions disabled"))
		}
	}
	if p.unmanaged {
		lines = append(lines, MutedText.Render("  Not a configured project: windows are shown for inspection only."))
	}

	rows := p.rows()
	if p.obs != nil && !p.obs.SessionExists && !p.unmanaged {
		lines = append(lines, MutedText.Render("  No session yet; nothing has been started."))
	}
	if len(rows) == 0 && p.obs != nil {
		lines = append(lines, MutedText.Render("  No processes declared and no windows open."))
	}

	nameW := 12
	if inner > 60 {
		nameW = 16
	}
	stateW := inner - 2 - nameW - 1
	policyW := 0
	if !narrow {
		policyW = 20
		stateW = inner - 2 - nameW - 1 - policyW - 1
	}
	if stateW < 8 {
		stateW = 8
	}

	otherHeader := false
	bodyStart := len(lines)
	for i, r := range rows {
		if r.other && !otherHeader {
			lines = append(lines, "", MutedText.Render("  Other windows"))
			otherHeader = true
		}
		selected := i == p.index
		style := lipgloss.NewStyle().Foreground(ColorFg)
		if selected {
			style = style.Foreground(ColorAccent).Bold(true)
		}
		name := padRight(style.Render(clip(clean(r.info.Name), nameW)), nameW)
		display := r.info.Display
		if verb, busy := p.pending[r.key]; busy {
			display = verb + "…"
		}
		stateStyle := stateStyleFor(r.info)
		line := RowCursor(selected) + name + " " + padRight(stateStyle.Render(clip(display, stateW)), stateW)
		if policyW > 0 {
			line += " " + MutedText.Render(clip(r.policy, policyW))
		}
		lines = append(lines, line)
	}
	_ = bodyStart

	// Details for the selected row.
	if cur := p.current(); cur != nil && !narrow {
		lines = append(lines, "")
		lines = append(lines, p.detailLines(cur, inner)...)
	}
	if p.message != "" {
		lines = append(lines, WarningText.Render("  "+clip(p.message, inner-2)))
	}
	if acts := p.actions(); len(acts) > 0 {
		lines = append(lines, MutedText.Render("  "+strings.Join(acts, " · ")))
	}

	// Output preview below, on a wide screen.
	if !narrow {
		used := len(lines) + 1
		if room := height - used; room >= 4 {
			lines = append(lines, "")
			lines = append(lines, strings.Split(p.outputView(width, room-1, false), "\n")...)
		}
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// trimPadding drops the blank rows tmux pads a pane with, and squeezes
// interior blank runs, so a one-line crash message is not buried under forty
// empty lines in the preview.
func trimPadding(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}

func stateStyleFor(info sources.ProcessInfo) lipgloss.Style {
	switch info.Outcome {
	case sources.OutcomeRunning:
		return SuccessText
	case sources.OutcomeExited:
		return ErrorText
	case sources.OutcomeStopped, sources.OutcomeCompleted:
		return MutedText
	case sources.OutcomeUnknown:
		return WarningText
	default:
		return MutedText
	}
}

func (p *processesModel) detailLines(cur *procRow, inner int) []string {
	info := cur.info
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, "  "+padRight(MutedText.Render(k), 12)+clip(clean(v), inner-14))
		}
	}
	if info.WindowID != "" {
		id := info.WindowID
		if info.PaneID != "" {
			id += " " + info.PaneID
		}
		if info.Generation != "" {
			id += " on server " + info.Generation
		}
		add("identity", id)
	}
	if info.Configured {
		switch {
		case info.Managed:
			add("ownership", "managed by cockpit")
		case info.Adoptable:
			add("ownership", "not started by cockpit: inspect only, or press A to adopt")
		}
		if info.Split {
			add("conflict", fmt.Sprintf("window has %d panes; stop/restart disabled to protect the others", info.Panes))
		}
		if info.Ambiguous {
			add("conflict", "several windows claim this process; close the extras first")
		}
		if info.DesiredState == sources.DesiredStopped {
			since := ""
			if info.StoppedAt != nil {
				since = " since " + info.StoppedAt.Format("15:04")
			}
			add("stop", "stopped by you"+since+"; lasts while this tmux session lives, until Start")
		}
		if info.ExitCode != nil {
			add("exit code", fmt.Sprintf("%d", *info.ExitCode))
		}
		if p.showCmd {
			add("command", info.Command)
		} else {
			add("command", "hidden · press c to show")
		}
	}
	return out
}

// outputView renders the output pane. Full is the whole screen; otherwise
// it is the preview below the list.
func (p *processesModel) outputView(width, height int, full bool) string {
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	cur := p.current()
	var lines []string
	title := "Output"
	if cur != nil {
		title += " · " + clean(cur.info.Name)
	}
	if p.output.lines > 0 {
		title += fmt.Sprintf(" · last %d lines", p.output.lines)
	}
	hint := ""
	if full && inner >= MobileMaxWidth {
		hint = MutedText.Render("j/k scroll · G follow · L load 2000 · Esc back")
	}
	header := padRight(SectionLabel(clip(title, inner-lipgloss.Width(hint)-1), full), inner-lipgloss.Width(hint)) + hint
	lines = append(lines, clip(header, inner))
	switch {
	case cur == nil:
		lines = append(lines, MutedText.Render("  nothing selected"))
	case cur.info.WindowID == "" && cur.info.WindowIndex < 0:
		lines = append(lines, MutedText.Render("  not started; no output"))
	case p.output.err != "":
		lines = append(lines, WarningText.Render("  "+clip("output unavailable: "+p.output.err, inner-2)))
	case p.output.loading && p.output.text == "":
		lines = append(lines, MutedText.Render("  reading…"))
	default:
		body := strings.Split(strings.TrimRight(p.output.text, "\n"), "\n")
		avail := height - 1
		if avail < 1 {
			avail = 1
		}
		start := p.output.scroll
		if start < 0 || start > len(body)-avail {
			start = len(body) - avail
		}
		if start < 0 {
			start = 0
		}
		end := start + avail
		if end > len(body) {
			end = len(body)
		}
		for _, l := range body[start:end] {
			lines = append(lines, "  "+clip(clean(l), inner-2))
		}
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// scrollOutput moves the output view; reaching the bottom resumes following.
func (p *processesModel) scrollOutput(delta int, visible int) {
	body := strings.Count(strings.TrimRight(p.output.text, "\n"), "\n") + 1
	bottom := body - visible
	if bottom < 0 {
		bottom = 0
	}
	cur := p.output.scroll
	if cur < 0 {
		cur = bottom
	}
	cur = clampInt(cur+delta, 0, bottom)
	if cur >= bottom {
		p.output.scroll = -1 // follow
	} else {
		p.output.scroll = cur
	}
}

// confirmView renders the scoped confirmation dialog.
func (p *processesModel) confirmView(width int) string {
	c := p.confirm
	if c == nil {
		return ""
	}
	dialogW := 60
	if width < 64 {
		dialogW = width - 4
	}
	var lines []string
	where := hostLabel(p.host) + "/" + clean(p.label) + " · " + clean(c.process)
	switch c.kind {
	case "stop":
		lines = append(lines, AccentText.Bold(true).Render("Stop "+where+"?"))
		lines = append(lines, "", "This terminates the tmux window's running work. It is not a", "graceful application shutdown.", "", "The process stays stopped on the next project entry until you start it.")
	case "restart":
		lines = append(lines, AccentText.Bold(true).Render("Restart "+where+"?"))
		lines = append(lines, "", "This terminates the tmux window's running work and relaunches", "the configured command in the same window. Not a graceful shutdown.")
	case "adopt":
		lines = append(lines, AccentText.Bold(true).Render("Adopt window as "+where+"?"))
		lines = append(lines, "", "Cockpit did not start this window. Adopting marks it as the", "configured process so Stop/Restart apply to it. Nothing is launched", "or replaced.", "", MutedText.Render(clip(c.detail, dialogW-6)))
	}
	cancel, act := "[ Cancel ]", "  "+strings.ToUpper(c.kind[:1])+c.kind[1:]+"  "
	if c.armed {
		cancel, act = "  Cancel  ", "[ "+strings.ToUpper(c.kind[:1])+c.kind[1:]+" ]"
		act = ErrorText.Bold(true).Render(act)
	} else {
		cancel = AccentText.Bold(true).Render(cancel)
	}
	lines = append(lines, "", cancel+"   "+act, "", MutedText.Render("←/→ or Tab choose · Enter select · y confirm · Esc cancel"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogW).
		Render(strings.Join(lines, "\n"))
}

// --- commands (on Model, which owns the service) ---

func (m *Model) fetchProcObs() tea.Cmd {
	p := &m.procs
	p.seq++
	seq := p.seq
	key, host, label, unmanaged := p.key, p.host, p.label, p.unmanaged
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var obs sources.ProcessObservation
		var err error
		if unmanaged {
			r, rerr := svc.RunnerFor(config.RepoConfig{Host: host})
			if rerr != nil {
				return procObsMsg{key: key, seq: seq, err: rerr}
			}
			obs, err = sources.ObserveProcesses(ctx, r, config.RepoConfig{Label: label, Host: host}, time.Now())
		} else {
			obs, err = svc.Inspect(ctx, key)
		}
		if err != nil {
			return procObsMsg{key: key, seq: seq, err: err}
		}
		return procObsMsg{key: key, seq: seq, obs: &obs}
	}
}

// fetchProcOutput reads the selected row's pane. Only the visible selection
// is captured, and a reply for a row that is no longer selected is dropped.
func (m *Model) fetchProcOutput(lines int) tea.Cmd {
	p := &m.procs
	cur := p.current()
	if cur == nil || cur.info.PaneID == "" {
		return nil
	}
	if lines <= 0 {
		lines = process.DefaultOutputLines
	}
	if lines > process.MaxOutputLines {
		lines = process.MaxOutputLines
	}
	p.output.seq++
	p.output.loading = true
	seq := p.output.seq
	key, host, window, windowID, paneID := p.key, p.host, cur.info.Name, cur.info.WindowID, cur.info.PaneID
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		r, err := svc.RunnerFor(config.RepoConfig{Host: host})
		if err != nil {
			return procOutputMsg{key: key, window: window, windowID: windowID, seq: seq, err: err}
		}
		out, err := r.Run(ctx, sources.CapturePaneArgs(paneID, lines)...)
		if err != nil {
			return procOutputMsg{key: key, window: window, windowID: windowID, seq: seq, err: err}
		}
		return procOutputMsg{key: key, window: window, windowID: windowID, seq: seq, text: sources.StripControl(out), lines: lines}
	}
}

func (m *Model) runProcAction(kind, proc, windowID string) tea.Cmd {
	p := &m.procs
	p.pending[proc] = kind + "ing"
	key := p.key
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var res process.Result
		switch kind {
		case "start":
			res = svc.Start(ctx, key, proc)
		case "stop":
			res = svc.Stop(ctx, key, proc)
		case "restart":
			res = svc.Restart(ctx, key, proc)
		case "adopt":
			res = svc.Adopt(ctx, key, proc, windowID)
		}
		return procActionMsg{key: key, process: proc, res: res}
	}
}
