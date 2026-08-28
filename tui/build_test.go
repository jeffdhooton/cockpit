package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jhoot/cockpit/buildctl"
	"github.com/jhoot/cockpit/sources"
)

// fakeBuildClient is the hermetic BuildClient fake: no real buildctl, no real
// Build home, no tmux. Every method records its invocation.
type fakeBuildClient struct {
	sessions []buildctl.Session
	sessErr  error
	projects []buildctl.Project
	projErr  error

	launchErr error
	resumeErr error

	launched []buildctl.LaunchOptions
	resumed  []string
	attached []string
}

func (f *fakeBuildClient) ListSessions(context.Context) ([]buildctl.Session, error) {
	return f.sessions, f.sessErr
}

func (f *fakeBuildClient) ListProjects(context.Context) ([]buildctl.Project, error) {
	return f.projects, f.projErr
}

func (f *fakeBuildClient) Launch(_ context.Context, opts buildctl.LaunchOptions) (buildctl.Session, error) {
	f.launched = append(f.launched, opts)
	return buildctl.Session{}, f.launchErr
}

func (f *fakeBuildClient) Resume(_ context.Context, conversationID, permission string) (buildctl.Session, error) {
	f.resumed = append(f.resumed, conversationID+"|"+permission)
	return buildctl.Session{}, f.resumeErr
}

func (f *fakeBuildClient) AttachCommand(_ context.Context, runID string) (*exec.Cmd, error) {
	f.attached = append(f.attached, runID)
	return exec.Command("/bin/true"), nil
}

// stubResolveUnavailable makes resolveBuildClient fail as if no buildctl
// exists anywhere, so NewModel never touches the real Build home.
func stubResolveUnavailable(t *testing.T) {
	t.Helper()
	old := buildctlResolve
	buildctlResolve = func(string) (string, error) {
		return "", &buildctl.Error{Kind: buildctl.KindUnavailable, Message: "stubbed: no buildctl", ExitCode: -1}
	}
	t.Cleanup(func() { buildctlResolve = old })
}

func newBuildTestModel(t *testing.T) Model {
	t.Helper()
	stubResolveUnavailable(t)
	m := NewModel(testConfig(), "/tmp/config.toml")
	m.width = 100
	m.height = 40
	m.layout = CalculateLayout(100, 40, 0)
	return m
}

// TestBuildUnavailableAtStartup proves a missing buildctl is nonfatal and
// leaves legacy navigation fully intact with a quiet indicator.
func TestBuildUnavailableAtStartup(t *testing.T) {
	m := newBuildTestModel(t)
	if m.buildClient != nil {
		t.Fatal("expected no build client when resolution fails")
	}
	if m.sessions.BuildNote == "" {
		t.Error("expected a quiet unavailable indicator")
	}
	if !strings.Contains(m.sessions.BuildNote, "legacy only") {
		t.Errorf("indicator %q should say legacy-only operation continues", m.sessions.BuildNote)
	}
	if cmd := m.fetchBuild(); cmd != nil {
		t.Error("fetchBuild with no client must be a no-op")
	}

	// Legacy sessions still merge and render.
	m2, _ := m.Update(tmuxDataMsg{Sessions: []sources.TmuxSession{
		legacySession("dev", time.Now()),
	}})
	m = m2.(Model)
	if len(m.sessions.Sessions) != 1 || m.sessions.Sessions[0].Source != SourceLegacy {
		t.Fatalf("legacy sessions broken without Build: %+v", m.sessions.Sessions)
	}

	// Launch is blocked with a visible, actionable hint — not a crash.
	cmd := m.handleNavKey(keyMsg("L"))
	if cmd == nil {
		t.Fatal("L with no Build should still return a feedback cmd")
	}
	if m.mode != ModeNavigation {
		t.Errorf("mode changed to %d without Build", m.mode)
	}
	if !strings.Contains(m.transientErr, "Build unavailable") {
		t.Errorf("transient = %q, want an actionable Build-unavailable hint", m.transientErr)
	}
}

// TestBuildFailureDropsStaleRecords proves any Build fetch failure removes
// previously listed Build sessions, so stale attach/resume actions become
// impossible, while legacy sessions survive.
func TestBuildFailureDropsStaleRecords(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	// Populate both sources.
	m2, _ := m.Update(tmuxDataMsg{Sessions: []sources.TmuxSession{legacySession("dev", time.Now())}})
	m = m2.(Model)
	m2, _ = m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-1", "Build session", "idle", true, true, false, time.Now()),
	}})
	m = m2.(Model)
	if len(m.sessions.Sessions) != 2 {
		t.Fatalf("setup: got %d sessions, want 2", len(m.sessions.Sessions))
	}

	// Build becomes unavailable / malformed / incompatible: all equivalent
	// at this boundary — degrade to legacy-only.
	for _, err := range []error{
		buildctl.ErrUnavailable,
		buildctl.ErrMalformed,
		buildctl.ErrUnsupportedSchema,
		buildctl.ErrTimeout,
	} {
		m2, _ = m.Update(buildDataMsg{Err: err})
		m = m2.(Model)
		if len(m.sessions.Sessions) != 1 || m.sessions.Sessions[0].Source != SourceLegacy {
			t.Fatalf("err %v: stale Build records survived: %+v", err, m.sessions.Sessions)
		}
		if m.sessions.BuildNote == "" {
			t.Errorf("err %v: missing quiet failure indicator", err)
		}
	}
}

// TestAttachUsesContractRunID proves Enter on an attachable Build session
// attaches through the contract run_id and nothing else.
func TestAttachUsesContractRunID(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-1", "Build work", "idle", true, true, false, time.Now()),
	}})
	m = m2.(Model)
	m.focused = PanelSessions
	m.sessions.Cursor = 0

	cmd := m.handleEnter()
	if cmd == nil {
		t.Fatal("Enter on attachable Build session returned nil cmd")
	}
	if len(fake.attached) != 1 || fake.attached[0] != "run-conv-1" {
		t.Errorf("attached = %v, want exactly [run-conv-1]", fake.attached)
	}
	// Executing the ExecProcess cmd only produces bubbletea's internal exec
	// message; the child is run by the Program with the terminal suspended.
	msg := cmd()
	if got := fmt.Sprintf("%T", msg); got != "tea.execMsg" {
		t.Errorf("cmd() = %s, want tea.execMsg (suspend/restore path)", got)
	}
}

// TestResumeGatedByContractFlag proves resume fires only when the contract
// record says resumable, with the contract conversation id.
func TestResumeGatedByContractFlag(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-dead", "Exited work", "exited", false, false, true, time.Now()),
	}})
	m = m2.(Model)
	m.focused = PanelSessions

	cmd := m.handleEnter()
	if cmd == nil {
		t.Fatal("Enter on resumable Build session returned nil cmd")
	}
	msg := cmd()
	result, ok := msg.(buildActionResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want buildActionResultMsg", msg)
	}
	if result.Err != nil || result.Verb != "resume" {
		t.Errorf("result = %+v, want clean resume", result)
	}
	if len(fake.resumed) != 1 || fake.resumed[0] != "conv-dead|standard" {
		t.Errorf("resumed = %v, want [conv-dead|standard]", fake.resumed)
	}

	// The result triggers a refresh so availability flags stay contract-true.
	m2, refresh := m.Update(result)
	m = m2.(Model)
	if refresh == nil {
		t.Error("resume result should schedule a Build refresh")
	}
}

// TestNeitherAttachableNorResumable proves a Build session the contract
// forbids both actions on produces only a visible hint — no child process.
func TestNeitherAttachableNorResumable(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-x", "Detached run", "disconnected", true, false, false, time.Now()),
	}})
	m = m2.(Model)
	m.focused = PanelSessions

	cmd := m.handleEnter()
	if len(fake.attached) != 0 || len(fake.resumed) != 0 {
		t.Errorf("action fired despite contract flags: attached=%v resumed=%v", fake.attached, fake.resumed)
	}
	if cmd == nil {
		t.Fatal("expected a feedback cmd for the hint")
	}
	if !strings.Contains(m.transientErr, "not attachable or resumable") {
		t.Errorf("transient = %q, want a not-allowed hint", m.transientErr)
	}
}

// TestAttachFailureLeavesTUIIntact proves a failed interactive attach (child
// non-zero exit) surfaces a transient error and leaves mode, sessions, and
// navigation exactly as they were — the terminal is not corrupted.
func TestAttachFailureLeavesTUIIntact(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{}
	m.buildClient = fake

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-1", "Build work", "idle", true, true, false, time.Now()),
	}})
	m = m2.(Model)
	sessionsBefore := m.sessions.Sessions
	modeBefore := m.mode
	cursorBefore := m.sessions.Cursor

	m2, cmd := m.Update(attachResultMsg{Err: errors.New("exit status 1")})
	m = m2.(Model)

	if !strings.Contains(m.transientErr, "attach") {
		t.Errorf("transient = %q, want an attach failure message", m.transientErr)
	}
	if m.mode != modeBefore || m.sessions.Cursor != cursorBefore {
		t.Error("attach failure disturbed TUI state")
	}
	if len(m.sessions.Sessions) != len(sessionsBefore) {
		t.Error("attach failure disturbed the session list")
	}
	if cmd == nil {
		t.Error("attach result should schedule a refresh")
	}

	// Successful detach is silent and also refreshes.
	m.transientErr = ""
	m.transientTimer = 0
	m2, cmd = m.Update(attachResultMsg{Err: nil})
	m = m2.(Model)
	if strings.Contains(m.transientErr, "attach") {
		t.Errorf("clean detach should not report an error: %q", m.transientErr)
	}
	if cmd == nil {
		t.Error("clean detach should schedule a refresh")
	}
}

// TestBuildPreviewUsesContractOnly proves selecting a Build session renders
// contract data synchronously and never issues a pane-capture command.
func TestBuildPreviewUsesContractOnly(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-1", "Build work", "needs_input", true, true, false, time.Now()),
	}})
	m = m2.(Model)
	m.focused = PanelSessions

	cmd := m.refreshPreview()
	if cmd != nil {
		t.Error("Build preview must not spawn a capture command (private tmux server)")
	}
	if !strings.Contains(m.sessionPreview, "attachable=true") ||
		!strings.Contains(m.sessionPreview, "Needs input") ||
		!strings.Contains(m.sessionPreview, "program-health") {
		t.Errorf("preview missing contract data:\n%s", m.sessionPreview)
	}

	// Legacy selection still captures via the default tmux server.
	m2, _ = m.Update(tmuxDataMsg{Sessions: []sources.TmuxSession{legacySession("dev", time.Now().Add(time.Hour))}})
	m = m2.(Model)
	m.sessions.Cursor = 0 // newest first: the legacy session
	if m.sessions.Sessions[0].Source != SourceLegacy {
		t.Fatalf("expected legacy session first, got %+v", m.sessions.Sessions[0])
	}
	if cmd := m.refreshPreview(); cmd == nil {
		t.Error("legacy preview should issue a capture command")
	}
}

// TestLegacyEnterUnchanged proves Enter on a legacy session still switches
// via the default tmux server (the cmd is not executed here — it would talk
// to a real tmux).
func TestLegacyEnterUnchanged(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}

	m2, _ := m.Update(tmuxDataMsg{Sessions: []sources.TmuxSession{legacySession("dev", time.Now())}})
	m = m2.(Model)
	m.focused = PanelSessions

	if cmd := m.handleEnter(); cmd == nil {
		t.Error("Enter on legacy session returned nil cmd")
	}
	if len(m.buildClient.(*fakeBuildClient).attached) != 0 {
		t.Error("legacy switch must never touch the Build client")
	}
}

// TestLaunchDialogFlow drives the full launch dialog and asserts the exact
// contract call it produces.
func TestLaunchDialogFlow(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{
		projects: []buildctl.Project{
			{ID: "proj-1", Label: "program-health", RootPath: "/x", HostKind: "local"},
		},
	}
	m.buildClient = fake

	cmd := m.handleNavKey(keyMsg("L"))
	if m.mode != ModeBuildLaunch {
		t.Fatalf("mode = %d, want ModeBuildLaunch", m.mode)
	}
	if !m.launchLoading {
		t.Error("expected projects to load on entry")
	}
	if cmd == nil {
		t.Fatal("expected a project fetch cmd")
	}

	// Projects arrive (already local-filtered by fetchBuildProjects).
	m2, _ := m.Update(buildProjectsMsg{Projects: fake.projects})
	m = m2.(Model)
	if m.launchLoading || len(m.launchProjects) != 1 {
		t.Fatalf("projects not loaded: %+v", m.launchProjects)
	}

	m.handleBuildLaunchKey(keyMsg("enter")) // project → agent
	if m.launchStep != 1 {
		t.Fatalf("step = %d, want 1 (agent)", m.launchStep)
	}
	m.handleBuildLaunchKey(keyMsg("right")) // choose codex
	if m.launchAgent != 1 {
		t.Errorf("agent = %d, want 1 (codex)", m.launchAgent)
	}
	m.handleBuildLaunchKey(keyMsg("enter")) // agent → permission
	m.handleBuildLaunchKey(keyMsg("right")) // choose dangerous explicitly
	m.handleBuildLaunchKey(keyMsg("enter")) // permission → prompt
	if m.launchStep != 3 {
		t.Fatalf("step = %d, want 3 (prompt)", m.launchStep)
	}

	m.launchInput.SetValue("Investigate the failing test")
	cmd = m.handleBuildLaunchKey(keyMsg("enter"))
	if m.mode != ModeNavigation {
		t.Errorf("mode = %d after submit, want ModeNavigation", m.mode)
	}
	if cmd == nil {
		t.Fatal("submit returned nil cmd")
	}

	msg := cmd()
	result, ok := msg.(buildActionResultMsg)
	if !ok || result.Err != nil || result.Verb != "launch" {
		t.Fatalf("launch result = %+v, want clean launch", msg)
	}
	if len(fake.launched) != 1 {
		t.Fatalf("launched = %v, want exactly one call", fake.launched)
	}
	got := fake.launched[0]
	want := buildctl.LaunchOptions{
		ProjectID:  "proj-1",
		Agent:      "codex",
		Permission: "dangerous",
		Prompt:     "Investigate the failing test",
	}
	if got != want {
		t.Errorf("launch opts = %+v, want %+v", got, want)
	}
}

// TestLaunchProjectFiltering proves the launch dialog offers only
// non-archived local projects, per the contract's Goal 1 scoping.
func TestLaunchProjectFiltering(t *testing.T) {
	m := newBuildTestModel(t)
	fake := &fakeBuildClient{
		projects: []buildctl.Project{
			{ID: "p1", Label: "local-live", HostKind: "local"},
			{ID: "p2", Label: "local-archived", HostKind: "local", Archived: true},
			{ID: "p3", Label: "remote-live", HostKind: "ssh"},
		},
	}
	m.buildClient = fake

	cmd := m.fetchBuildProjects()
	if cmd == nil {
		t.Fatal("nil fetch cmd")
	}
	msg, ok := cmd().(buildProjectsMsg)
	if !ok {
		t.Fatalf("msg = %T, want buildProjectsMsg", msg)
	}
	if len(msg.Projects) != 1 || msg.Projects[0].ID != "p1" {
		t.Errorf("launchable projects = %+v, want only p1", msg.Projects)
	}
}

// TestLaunchProjectFetchFailure proves a failed project list is visible in
// the dialog and cannot be launched through.
func TestLaunchProjectFetchFailure(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}

	m.handleNavKey(keyMsg("L"))
	m2, _ := m.Update(buildProjectsMsg{Err: buildctl.ErrUnavailable})
	m = m2.(Model)
	if m.launchErr == "" {
		t.Error("expected an inline dialog error")
	}
	// Enter with no projects must not submit anything.
	if cmd := m.handleBuildLaunchKey(keyMsg("enter")); cmd != nil {
		t.Error("enter with no projects must not produce a launch cmd")
	}
	if m.launchStep != 0 {
		t.Errorf("step advanced to %d with no projects", m.launchStep)
	}
}

// TestSaveSessionAsRepoSkipsBuild proves the legacy save-to-config action
// refuses Build sessions (Build owns their cwd/lifecycle) without touching
// the config file.
func TestSaveSessionAsRepoSkipsBuild(t *testing.T) {
	m := newBuildTestModel(t)
	m.buildClient = &fakeBuildClient{}

	m2, _ := m.Update(buildDataMsg{Sessions: []buildctl.Session{
		buildSession("conv-1", "Build work", "idle", true, true, false, time.Now()),
	}})
	m = m2.(Model)
	m.focused = PanelSessions
	m.sessions.Cursor = 0

	cmd := m.handleNavKey(keyMsg("s"))
	if cmd == nil {
		t.Fatal("expected a feedback cmd")
	}
	if !strings.Contains(m.transientErr, "managed by Build") {
		t.Errorf("transient = %q, want a managed-by-Build hint", m.transientErr)
	}
}
