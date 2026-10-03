package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/process"
	"github.com/jeffdhooton/cockpit/sources"
)

func panelModel(t *testing.T, width, height int) Model {
	t.Helper()
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{Label: "site", Path: "/tmp/site", Processes: []config.ProcessConfig{{Name: "dev", Command: "npm run dev"}}},
		},
		Hosts: []config.HostConfig{{Name: "mini", Tmux: "/opt/homebrew/bin/tmux"}},
	}
	cfg.General.SessionName = "cockpit"
	cfg.Signals.ShowUnpushed = true
	m := NewModel(cfg, "/tmp/config.toml")
	m.width, m.height = width, height
	m.view = ViewGrid // most panel tests start from the grid
	fixed := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return fixed }
	return m
}

func blockedPane(session, sid, pid, inv string) sources.PaneReport {
	return sources.PaneReport{Session: session, SessionID: sid, WindowID: "@1", PaneID: pid, Invocation: inv,
		Generation: "g1", Status: sources.AgentStatusNeedsInput, Reported: true, Recorded: true, WindowName: "agent"}
}

// feed applies a message the way the runtime does: Update returns the next
// model, which replaces the old one.
func (m *Model) feed(msg tea.Msg) {
	next, _ := m.Update(msg)
	*m = next.(Model)
}

func feedLocal(m *Model, panes ...sources.PaneReport) {
	m.feed(tmuxDataMsg{
		Sessions: []sources.TmuxSession{{Name: "site", ID: "$1", Generation: "g1"}, {Name: "api", ID: "$2", Generation: "g1"}},
		Panes:    panes,
		At:       m.now(),
	})
}

func TestAttentionOpensFromGridAndReturnsToTheSameTile(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("api", "$2", "%5", "inv"))
	m.gridHost = "mini"
	m.gridCursor = "docket"
	m.gridIndex = 2

	m.handleKey(keyMsg("a"))
	if m.view != ViewAttention {
		t.Fatalf("view = %v", m.view)
	}
	view := m.View()
	if !strings.Contains(view, "local/api") || !strings.Contains(view, "Open agent") {
		t.Errorf("queue should list the blocked pane with its action:\n%s", view)
	}
	m.handleKey(keyMsg("esc"))
	if m.view != ViewGrid || m.gridHost != "mini" || m.gridCursor != "docket" || m.gridIndex != 2 {
		t.Errorf("Esc must restore the exact previous view: view=%v host=%q cursor=%q idx=%d", m.view, m.gridHost, m.gridCursor, m.gridIndex)
	}
}

func TestAttentionSelectionSurvivesRefreshAndNewUrgentRows(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("site", "$1", "%2", "b"))
	m.handleKey(keyMsg("a"))
	sel := m.attn.selected
	if sel == "" {
		t.Fatal("no selection")
	}
	// An older-looking urgent item arrives; the tracker keeps ours first
	// by first sight, and the selection stays bound to the id regardless.
	feedLocal(&m, blockedPane("api", "$2", "%9", "a"), blockedPane("site", "$1", "%2", "b"))
	if m.attn.selected != sel {
		t.Errorf("selection moved from %q to %q", sel, m.attn.selected)
	}
	if len(m.attn.rows()) != 2 {
		t.Errorf("rows = %+v", m.attn.rows())
	}
	// Repeated identical polls do not duplicate.
	feedLocal(&m, blockedPane("api", "$2", "%9", "a"), blockedPane("site", "$1", "%2", "b"))
	if len(m.attn.rows()) != 2 {
		t.Errorf("duplicates after repeat poll: %+v", m.attn.rows())
	}
}

func TestResolvedRowBlocksAQueuedEnter(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("site", "$1", "%2", "b"), blockedPane("api", "$2", "%9", "a"))
	m.handleKey(keyMsg("a"))
	first := m.attn.selected
	// The selected pane resumes work; the other item slides into its place.
	feedLocal(&m, blockedPane("api", "$2", "%9", "a"))
	if m.attn.resolved == nil || m.attn.resolved.ID != first {
		t.Fatalf("expected a resolved placeholder for %q, got %+v", first, m.attn.resolved)
	}
	if cmd := m.handleKey(keyMsg("enter")); cmd != nil {
		t.Error("Enter on a resolved placeholder must do nothing")
	}
	view := m.View()
	if !strings.Contains(view, "resolved") {
		t.Errorf("placeholder should be visible:\n%s", view)
	}
	m.handleKey(keyMsg("j"))
	if m.attn.resolved != nil || m.attn.current() == nil {
		t.Errorf("a navigation key dismisses the placeholder: resolved=%v current=%v", m.attn.resolved, m.attn.current())
	}
}

func TestAttentionShowsCoverageAndNeverClaimsHealthy(t *testing.T) {
	m := panelModel(t, 120, 40)
	m.feed(tmuxDataMsg{Err: errTest("permission denied"), At: m.now()})
	m.handleKey(keyMsg("a"))
	view := m.View()
	if !strings.Contains(view, "No attention items observed") {
		t.Errorf("empty state must be qualified:\n%s", view)
	}
	if strings.Contains(view, "healthy") {
		t.Errorf("must not claim health with data missing:\n%s", view)
	}
	if !strings.Contains(view, "unavailable") || !strings.Contains(view, "mini") {
		t.Errorf("coverage must list unreadable sources including the unpolled host:\n%s", view)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestAttentionHousekeepingTabDoesNotInflateBadge(t *testing.T) {
	m := panelModel(t, 120, 40)
	m.feed(gitDataMsg{Repos: []sources.GitRepoStatus{{Label: "site", Unpushed: 3}}})
	feedLocal(&m)
	if !strings.Contains(m.attn.badge(), "Attention 0") {
		t.Errorf("badge = %q", m.attn.badge())
	}
	m.handleKey(keyMsg("a"))
	if len(m.attn.rows()) != 0 {
		t.Errorf("unpushed work belongs in housekeeping, got %+v", m.attn.rows())
	}
	m.handleKey(keyMsg("tab"))
	if len(m.attn.rows()) != 1 || m.attn.rows()[0].Kind != sources.AttentionUnpushed {
		t.Errorf("housekeeping rows = %+v", m.attn.rows())
	}
}

func TestAttentionNarrowLayoutKeepsActionVisible(t *testing.T) {
	m := panelModel(t, 40, 12)
	feedLocal(&m, blockedPane("site", "$1", "%2", "b"))
	m.handleKey(keyMsg("a"))
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Errorf("render is %d lines tall for a 12-row terminal", len(lines))
	}
	for _, l := range lines {
		if w := visibleWidth(l); w > 40 {
			t.Errorf("line wider than 40: %d %q", w, l)
		}
	}
	if !strings.Contains(view, "local/site") || !strings.Contains(view, "Open agent") {
		t.Errorf("host/project and action must survive at 40 columns:\n%s", view)
	}
	m.handleKey(keyMsg("esc"))
	if m.view != ViewGrid {
		t.Error("Esc must still return at narrow width")
	}
}

func visibleWidth(s string) int {
	// lipgloss.Width measures cells and ignores ANSI escapes.
	return lipglossWidth(s)
}

func TestControlSequencesInNamesCannotReachTheTerminal(t *testing.T) {
	m := panelModel(t, 120, 40)
	p := blockedPane("evil\x1b[2J\x1b]0;pwned\x07", "$1", "%2", "b")
	m.feed(tmuxDataMsg{Sessions: []sources.TmuxSession{{Name: p.Session, ID: "$1", Generation: "g1"}}, Panes: []sources.PaneReport{p}, At: m.now()})
	m.handleKey(keyMsg("a"))
	view := m.View()
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0;") || strings.Contains(view, "\x07") {
		t.Errorf("control sequences leaked into the render")
	}
	if !strings.Contains(view, "evil") {
		t.Errorf("the printable part of the name should still show:\n%s", view)
	}
}

func TestProcessesOpensFromGridWithoutLaunching(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m)
	targets := m.gridTargets()
	idx := -1
	for i, t := range targets {
		if t.Label == "site" && t.Host == "" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no site tile in %+v", targets)
	}
	m.setGridCursor(targets, idx)
	m.handleKey(keyMsg("p"))
	if m.view != ViewProcesses || m.procs.key != "site" {
		t.Fatalf("view=%v key=%q", m.view, m.procs.key)
	}
	// A dormant observation arrives: the declared command is listed as not
	// started; nothing was launched (no runner call could have happened in
	// the model itself; the fetch command is what would talk to tmux).
	obs := &sources.ProcessObservation{Project: "site", Session: "site", Processes: []sources.ProcessInfo{
		{Name: "dev", Command: "npm run dev", Configured: true, AutoStart: true, WindowIndex: -1, State: sources.ProcessNotStarted, Outcome: sources.OutcomeNotStarted, Display: "Not started", DesiredState: sources.DesiredRunning},
	}, ObservedAt: m.now()}
	m.feed(procObsMsg{key: "site", seq: m.procs.seq, obs: obs})
	view := m.View()
	if !strings.Contains(view, "Not started") || !strings.Contains(view, "starts with project") {
		t.Errorf("panel should list the declared command:\n%s", view)
	}
	if strings.Contains(view, "npm run dev") {
		t.Errorf("the command is hidden until requested:\n%s", view)
	}
	m.handleKey(keyMsg("c"))
	if !strings.Contains(m.View(), "npm run dev") {
		t.Errorf("c should reveal the configured command")
	}
	if !m.procs.can("s") || m.procs.can("x") {
		t.Errorf("a not-started row offers start only: %v", m.procs.actions())
	}
	m.handleKey(keyMsg("esc"))
	if m.view != ViewGrid || m.gridCursor != "site" {
		t.Errorf("Esc must return to the tile: view=%v cursor=%q", m.view, m.gridCursor)
	}
}

func TestHostBoxIsNotAProject(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m)
	targets := m.gridTargets()
	for i, tg := range targets {
		if tg.HostBox {
			m.setGridCursor(targets, i)
		}
	}
	m.handleKey(keyMsg("p"))
	if m.view != ViewGrid || !strings.Contains(m.transientErr, "Open a project") {
		t.Errorf("view=%v err=%q", m.view, m.transientErr)
	}
}

func TestProcessesStopNeedsScopedConfirmationDefaultingToCancel(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m)
	m.openProcessesFor("", "site", "dev")
	obs := &sources.ProcessObservation{Project: "site", Session: "site", SessionExists: true, Generation: "g1", Processes: []sources.ProcessInfo{
		{Name: "dev", Configured: true, AutoStart: true, State: sources.ProcessRunning, Outcome: sources.OutcomeRunning, Display: "Running",
			DesiredState: sources.DesiredRunning, Managed: true, WindowID: "@1", PaneID: "%1", Panes: 1, Generation: "g1"},
	}, ObservedAt: m.now()}
	m.feed(procObsMsg{key: "site", seq: m.procs.seq, obs: obs})
	if !m.procs.can("x") {
		t.Fatalf("a running managed row offers stop: %v", m.procs.actions())
	}
	m.handleKey(keyMsg("x"))
	if m.mode != ModeConfirm || m.procs.confirm == nil || m.procs.confirm.kind != "stop" {
		t.Fatalf("mode=%v confirm=%+v", m.mode, m.procs.confirm)
	}
	view := m.View()
	if !strings.Contains(view, "local/site") || !strings.Contains(view, "dev") || !strings.Contains(view, "not a") {
		t.Errorf("confirmation must name host/project/process and disclaim graceful shutdown:\n%s", view)
	}
	// Enter with the default selection cancels.
	if cmd := m.handleKey(keyMsg("enter")); cmd != nil || m.mode != ModeNavigation {
		t.Error("Enter must default to Cancel")
	}
	m.handleKey(keyMsg("x"))
	m.handleKey(keyMsg("tab"))
	if cmd := m.handleKey(keyMsg("enter")); cmd == nil {
		t.Error("after moving to Stop, Enter runs the action")
	}
	if m.procs.pending["dev"] == "" {
		t.Error("the row should be marked pending while the action runs")
	}
	if m.procs.can("x") {
		t.Errorf("duplicate actions are disabled while pending: %v", m.procs.actions())
	}
}

func TestSplitAndUnmanagedRowsCannotBeStopped(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m)
	m.openProcessesFor("", "site", "dev")
	for _, info := range []sources.ProcessInfo{
		{Name: "dev", Configured: true, State: sources.ProcessRunning, Outcome: sources.OutcomeRunning, Managed: true, WindowID: "@1", PaneID: "%1", Panes: 2, Split: true, Display: "Running (split)"},
		{Name: "dev", Configured: true, State: sources.ProcessRunning, Outcome: sources.OutcomeRunning, Adoptable: true, WindowID: "@1", PaneID: "%1", Panes: 1, Display: "Running (unmanaged)"},
	} {
		obs := &sources.ProcessObservation{Project: "site", Session: "site", SessionExists: true, Processes: []sources.ProcessInfo{info}, ObservedAt: m.now()}
		m.procs.seq++
		m.feed(procObsMsg{key: "site", seq: m.procs.seq, obs: obs})
		if m.procs.can("x") || m.procs.can("R") {
			t.Errorf("%s: stop/restart offered: %v", info.Display, m.procs.actions())
		}
		m.handleKey(keyMsg("x"))
		if m.mode == ModeConfirm || m.procs.message == "" {
			t.Errorf("%s: x must explain rather than confirm", info.Display)
		}
		if info.Adoptable && !m.procs.can("A") {
			t.Error("an unmanaged matching window offers adoption")
		}
	}
}

func TestLateOutputCannotReplaceTheSelectedPreview(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m)
	m.openProcessesFor("", "site", "dev")
	obs := &sources.ProcessObservation{Project: "site", Session: "site", SessionExists: true, Processes: []sources.ProcessInfo{
		{Name: "dev", Configured: true, State: sources.ProcessRunning, Outcome: sources.OutcomeRunning, Managed: true, WindowID: "@1", PaneID: "%1", Panes: 1, Display: "Running"},
		{Name: "zsh", State: sources.ProcessRunning, Outcome: sources.OutcomeRunning, WindowID: "@0", PaneID: "%0", Panes: 1, Display: "Running"},
	}, ObservedAt: m.now()}
	m.feed(procObsMsg{key: "site", seq: m.procs.seq, obs: obs})
	// A read for dev is in flight when the selection moves to zsh.
	m.procs.output.seq = 3
	m.handleKey(keyMsg("j"))
	m.feed(procOutputMsg{key: "site", window: "dev", windowID: "@1", seq: 3, text: "dev output"})
	if strings.Contains(m.procs.output.text, "dev output") {
		t.Error("a late reply for the previous row replaced the preview")
	}
	// An older sequence for the right row is also dropped.
	m.procs.output.seq = 5
	m.feed(procOutputMsg{key: "site", window: "zsh", windowID: "@0", seq: 4, text: "stale"})
	if m.procs.output.text == "stale" {
		t.Error("an older sequence must not win")
	}
	m.feed(procOutputMsg{key: "site", window: "zsh", windowID: "@0", seq: 5, text: "\x1b[31mfresh\x1b[0m"})
	if m.procs.output.text != "fresh" {
		t.Errorf("output = %q", m.procs.output.text)
	}
}

func TestProcessesPanelAtFortyByTwelve(t *testing.T) {
	m := panelModel(t, 40, 12)
	feedLocal(&m)
	m.openProcessesFor("", "site", "dev")
	obs := &sources.ProcessObservation{Project: "site", Session: "site", SessionExists: true, Processes: []sources.ProcessInfo{
		{Name: "dev", Configured: true, AutoStart: true, State: sources.ProcessDead, Outcome: sources.OutcomeExited, Managed: true, WindowID: "@1", PaneID: "%1", Panes: 1, Display: "Exited (code 1)"},
	}, ObservedAt: m.now()}
	m.feed(procObsMsg{key: "site", seq: m.procs.seq, obs: obs})
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Errorf("render is %d lines tall", len(lines))
	}
	for _, l := range lines {
		if w := visibleWidth(l); w > 40 {
			t.Errorf("line wider than 40: %d %q", w, l)
		}
	}
	if !strings.Contains(view, "Exited") {
		t.Errorf("state must be visible:\n%s", view)
	}
	m.handleKey(keyMsg("l"))
	if !m.procs.fullOut {
		t.Fatal("l opens full-screen output at narrow width")
	}
	m.feed(procOutputMsg{key: "site", window: "dev", windowID: "@1", seq: m.procs.output.seq, text: "boom\n", lines: 200})
	if !strings.Contains(m.View(), "boom") {
		t.Errorf("output should render:\n%s", m.View())
	}
	m.handleKey(keyMsg("esc"))
	if m.procs.fullOut || m.procs.selected != "dev" {
		t.Errorf("Esc returns to the same row: fullOut=%v selected=%q", m.procs.fullOut, m.procs.selected)
	}
}

func TestAttentionCrashItemOpensProcessesWithRowSelectedAndReturns(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m)
	m.feed(processDataMsg{
		ByLabel: map[string][]sources.ProcessInfo{},
		Obs: map[string]sources.ProcessObservation{"site": {Project: "site", Session: "site", SessionExists: true, Generation: "g1", Processes: []sources.ProcessInfo{
			{Name: "dev", Configured: true, State: sources.ProcessDead, Outcome: sources.OutcomeExited, Managed: true, WindowID: "@1", PaneID: "%1", Panes: 1, Display: "Exited (code 1)", DesiredState: sources.DesiredRunning, Generation: "g1"},
		}, ObservedAt: m.now()}},
	})
	m.handleKey(keyMsg("a"))
	item := m.attn.current()
	if item == nil || item.Kind != sources.AttentionProcessExited {
		t.Fatalf("item = %+v", item)
	}
	m.handleKey(keyMsg("enter"))
	if m.view != ViewProcesses || m.procs.selected != "dev" || m.procs.key != "site" {
		t.Fatalf("view=%v selected=%q key=%q", m.view, m.procs.selected, m.procs.key)
	}
	if !strings.Contains(m.View(), "Exited (code 1)") {
		t.Errorf("the dead process must be shown as exited:\n%s", m.View())
	}
	m.handleKey(keyMsg("esc"))
	if m.view != ViewAttention || m.attn.selected != item.ID {
		t.Errorf("Esc returns to the queue with the same item: view=%v sel=%q", m.view, m.attn.selected)
	}
	m.handleKey(keyMsg("esc"))
	if m.view != ViewGrid {
		t.Errorf("second Esc returns to the grid: %v", m.view)
	}
}

func TestTUIAndMCPDeriveEquivalentRecords(t *testing.T) {
	// The TUI assembles the same AttentionInput shape the daemon collects;
	// given equal observations, the derived items are identical.
	m := panelModel(t, 120, 40)
	panes := []sources.PaneReport{blockedPane("site", "$1", "%2", "b")}
	feedLocal(&m, panes...)
	in := m.attentionInput()
	direct := sources.DeriveAttention(sources.AttentionInput{
		Config: m.config.Signals, SelfSession: "cockpit", Now: m.now(),
		Hosts: []sources.HostReport{
			{Host: "", Outcome: sources.ObservationFresh, ObservedAt: m.now(), Sessions: in.Hosts[0].Sessions, Panes: panes},
			{Host: "mini", Outcome: sources.ObservationUnavailable, Err: "not polled yet"},
		},
	})
	viaTUI := sources.DeriveAttention(in)
	if len(viaTUI.Items) != len(direct.Items) || viaTUI.Items[0].ID != direct.Items[0].ID || viaTUI.Items[0].Target != direct.Items[0].Target {
		t.Errorf("tui %+v\ndirect %+v", viaTUI.Items, direct.Items)
	}
	if viaTUI.Unavailable != direct.Unavailable {
		t.Errorf("coverage differs: %d vs %d", viaTUI.Unavailable, direct.Unavailable)
	}
}

// keep the process package import meaningful for the service type check
var _ = process.OutcomeStarted
var _ = tea.KeyEnter

func TestRunURLOpenerOnlyAcceptsGitHubHTTPS(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/me/site/actions/runs/42": true,
		"http://github.com/me/site/actions/runs/42":  false,
		"https://evil.example/github.com/x":          false,
		"https://github.com/me/site; rm -rf /":       false,
		"":                                           false,
	} {
		if got := validRunURL(url); got != want {
			t.Errorf("validRunURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestSessionsIsTheDefaultViewAndGridIsG(t *testing.T) {
	m := panelModel(t, 120, 40)
	m = NewModel(m.config, "/tmp/config.toml")
	m.width, m.height = 120, 40
	if m.view != ViewSessions {
		t.Fatalf("default view = %v", m.view)
	}
	m.handleKey(keyMsg("g"))
	if m.view != ViewGrid {
		t.Fatalf("g should open the grid, got %v", m.view)
	}
	m.handleKey(keyMsg("d"))
	if m.view != ViewSessions {
		t.Errorf("d in the grid returns to the session view, got %v", m.view)
	}
}

func TestSessionsEnterNeverLaunches(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("api", "$2", "%5", "inv"))
	m.view = ViewSessions
	cmd := m.handleKey(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("Enter on a session attaches")
	}
	if m.sess.current() == nil || m.sess.current().Name != "api" {
		t.Errorf("selection = %+v", m.sess.current())
	}
}

func TestSessionsOpenProjectIsExplicit(t *testing.T) {
	m := panelModel(t, 120, 40)
	m.feed(gitDataMsg{Repos: []sources.GitRepoStatus{{Label: "site", Branch: "main"}}})
	m.feed(tmuxDataMsg{Sessions: []sources.TmuxSession{{Name: "api", ID: "$2", Generation: "g1"}}, At: m.now()})
	m.view = ViewSessions
	if len(m.sess.ws.Hosts[0].Dormant) == 0 {
		t.Fatalf("site should be dormant: %+v", m.sess.ws.Hosts[0])
	}
	// The selected session is not a configured project: o explains.
	if cmd := m.handleKey(keyMsg("o")); cmd != nil || m.sess.message == "" {
		t.Errorf("o on an unconfigured session must explain, cmd=%v msg=%q", cmd, m.sess.message)
	}
	m.sess.selected = ""
	m.sess.query = "nomatch"
	if cmd := m.handleKey(keyMsg("o")); cmd == nil {
		t.Error("o with no selection opens the first dormant project")
	}
}

func TestSessionsReturnFromAttentionKeepsPane(t *testing.T) {
	m := panelModel(t, 120, 40)
	feedLocal(&m, blockedPane("api", "$2", "%5", "inv"))
	m.view = ViewSessions
	m.sess.paneFocus = true
	m.handleKey(keyMsg("a"))
	m.handleKey(keyMsg("esc"))
	if m.view != ViewSessions || m.sess.pane != "%5" || !m.sess.paneFocus {
		t.Errorf("view=%v pane=%q focus=%v", m.view, m.sess.pane, m.sess.paneFocus)
	}
}
