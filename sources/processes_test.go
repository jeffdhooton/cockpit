package sources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jhoot/cockpit/config"
)

// fakeRunner records every tmux call and returns scripted output keyed by the
// tmux verb (the first argument).
type fakeRunner struct {
	calls   [][]string
	outputs map[string]string
	errs    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) == 0 {
		return "", nil
	}
	if err, ok := f.errs[args[0]]; ok {
		return "", err
	}
	return f.outputs[args[0]], nil
}

// called returns every recorded call for a tmux verb.
func (f *fakeRunner) called(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

func devRepo(procs ...config.ProcessConfig) config.RepoConfig {
	return config.RepoConfig{Label: "app", Path: "/r", Processes: procs}
}

func TestReconcileStartsMissingAutoStartProcesses(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|shell|0|111|1\n1|dev|0|222|0\n",
	}}
	repo := devRepo(
		config.ProcessConfig{Name: "dev", Command: "npm run dev"},
		config.ProcessConfig{Name: "test", Command: "npm test"},
	)

	if errs := ReconcileProcesses(context.Background(), f, repo); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	created := f.called("new-window")
	if len(created) != 1 {
		t.Fatalf("want 1 new-window, got %d: %v", len(created), created)
	}
	if !slices.Contains(created[0], "test") {
		t.Errorf("wrong window created: %v", created[0])
	}
}

func TestReconcileRespawnsDeadWindow(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|shell|0|111|1\n1|dev|1|222|0\n",
	}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "npm run dev"})

	ReconcileProcesses(context.Background(), f, repo)

	if len(f.called("respawn-window")) != 1 {
		t.Errorf("dead window should be respawned, calls: %v", f.calls)
	}
	if len(f.called("new-window")) != 0 {
		t.Errorf("dead window must not be duplicated, calls: %v", f.calls)
	}
}

func TestReconcileSkipsNonAutoStart(t *testing.T) {
	off := false
	f := &fakeRunner{outputs: map[string]string{"list-windows": "0|shell|0|111|1\n"}}
	repo := devRepo(config.ProcessConfig{Name: "test", Command: "npm test", AutoStart: &off})

	ReconcileProcesses(context.Background(), f, repo)

	if len(f.called("new-window")) != 0 {
		t.Errorf("auto_start = false must not launch, calls: %v", f.calls)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|shell|0|111|1\n1|dev|0|222|0\n",
	}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "npm run dev"})

	ReconcileProcesses(context.Background(), f, repo)

	for _, verb := range []string{"new-window", "respawn-window", "kill-window"} {
		if got := f.called(verb); len(got) != 0 {
			t.Errorf("%s should not run when everything is already up: %v", verb, got)
		}
	}
}

func TestReconcileCollectsErrorsAndKeepsGoing(t *testing.T) {
	f := &fakeRunner{
		outputs: map[string]string{"list-windows": "0|shell|0|111|1\n"},
		errs:    map[string]error{"new-window": errors.New("no server")},
	}
	repo := devRepo(
		config.ProcessConfig{Name: "dev", Command: "npm run dev"},
		config.ProcessConfig{Name: "test", Command: "npm test"},
	)

	errs := ReconcileProcesses(context.Background(), f, repo)

	if len(errs) != 2 {
		t.Fatalf("want an error per failed process, got %d: %v", len(errs), errs)
	}
	if len(f.called("new-window")) != 2 {
		t.Error("a failing process must not stop the ones after it")
	}
}

func TestReconcileWillNotStartWhenTheWindowListFailed(t *testing.T) {
	// The session is there — has-session succeeds — but listing its windows
	// failed. An empty list is then not evidence of an empty session, and
	// starting everything would duplicate whatever is already running.
	f := &fakeRunner{errs: map[string]error{"list-windows": errors.New("server exited unexpectedly")}}
	repo := devRepo(
		config.ProcessConfig{Name: "dev", Command: "npm run dev"},
		config.ProcessConfig{Name: "test", Command: "npm test"},
	)

	errs := ReconcileProcesses(context.Background(), f, repo)

	for _, verb := range []string{"new-window", "respawn-window"} {
		if got := f.called(verb); len(got) != 0 {
			t.Errorf("%s ran against a session we could not read: %v", verb, got)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("want the unreadable session reported, got %d: %v", len(errs), errs)
	}
}

func TestReconcileReportsMissingTmuxWithoutStarting(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{"list-windows": fmt.Errorf("tmux list-windows: %w", ErrTmuxNotFound)}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "npm run dev"})

	errs := ReconcileProcesses(context.Background(), f, repo)

	if got := f.called("new-window"); len(got) != 0 {
		t.Errorf("no tmux means nothing can be known, let alone started: %v", got)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrTmuxNotFound) {
		t.Fatalf("want a missing-tmux error, got %v", errs)
	}
}

func TestReconcileStartsWhenTheSessionIsVerifiedAbsent(t *testing.T) {
	// Both calls fail because there is no such session. That is a verified
	// negative rather than an unknown, so the launch may proceed.
	f := &fakeRunner{errs: map[string]error{
		"list-windows": errors.New("can't find session: app"),
		"has-session":  errors.New("can't find session: app"),
	}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "npm run dev"})

	ReconcileProcesses(context.Background(), f, repo)

	if got := f.called("new-window"); len(got) != 1 {
		t.Errorf("want dev started in a session known to be absent, got %v", f.calls)
	}
}

func TestStartProcessSetsRemainOnExit(t *testing.T) {
	f := &fakeRunner{}
	p := config.ProcessConfig{Name: "dev", Command: "npm run dev"}

	if err := StartProcess(context.Background(), f, devRepo(p), p); err != nil {
		t.Fatal(err)
	}
	calls := f.called("new-window")
	if len(calls) != 1 {
		t.Fatalf("want a single new-window call, got %v", f.calls)
	}
	// The option must ride along in that same call. A separate follow-up call
	// can arrive after a fast-failing command has already exited, at which
	// point tmux has closed the window and discarded the error.
	if !slices.Contains(calls[0], "remain-on-exit") {
		t.Errorf("a crashed process must stay readable, so remain-on-exit belongs in the launch call: %v", calls[0])
	}
}

func TestEnsureSessionKeepsFailedCommandsReadable(t *testing.T) {
	// Set on the session before any process window exists, so no crash can be
	// lost to a race.
	f := &fakeRunner{errs: map[string]error{"has-session": errors.New("no such session")}}

	if _, err := EnsureSession(context.Background(), f, devRepo()); err != nil {
		t.Fatal(err)
	}
	calls := f.called("set-option")
	if len(calls) != 1 || !slices.Contains(calls[0], "failed") {
		t.Errorf("want remain-on-exit failed on the new session, got %v", f.calls)
	}
}

func TestEnsureSessionSurvivesAnOldTmux(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{
		"has-session": errors.New("no such session"),
		"set-option":  errors.New("value is invalid: failed"),
	}}

	if _, err := EnsureSession(context.Background(), f, devRepo()); err != nil {
		t.Errorf("an old tmux without the failed value must not block the session: %v", err)
	}
}

func TestInspectProcessesClassifiesState(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|shell|0|111|1\n1|dev|1|222|0\n2|scratch|0|333|0\n",
	}}
	repo := devRepo(
		config.ProcessConfig{Name: "dev", Command: "npm run dev"},
		config.ProcessConfig{Name: "test", Command: "npm test"},
	)

	infos, err := InspectProcesses(context.Background(), f, repo)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]ProcessInfo{}
	for _, i := range infos {
		byName[i.Name] = i
	}
	if got := byName["dev"]; got.State != ProcessDead || !got.Configured {
		t.Errorf("dev should be a dead configured process: %+v", got)
	}
	if got := byName["test"]; got.State != ProcessNotStarted || got.WindowIndex != -1 {
		t.Errorf("test should be not started with no window: %+v", got)
	}
	if got := byName["scratch"]; got.Configured || got.State != ProcessRunning {
		t.Errorf("an ad-hoc window should be reported as unconfigured and running: %+v", got)
	}
	if got := byName["shell"]; got.Configured {
		t.Errorf("window 0 is the user's shell, not a configured process: %+v", got)
	}
}

func TestInspectProcessesWithoutSession(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{"list-windows": errors.New("no such session")}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "npm run dev"})

	infos, err := InspectProcesses(context.Background(), f, repo)
	if err != nil {
		t.Fatalf("a missing session is not an error, it just means nothing is running: %v", err)
	}
	if len(infos) != 1 || infos[0].State != ProcessNotStarted {
		t.Errorf("want dev reported as not started, got %+v", infos)
	}
}

func TestEnsureSessionCreatesOnlyWhenMissing(t *testing.T) {
	existing := &fakeRunner{}
	created, err := EnsureSession(context.Background(), existing, devRepo())
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("has-session succeeding means the session is already there")
	}
	if len(existing.called("new-session")) != 0 {
		t.Errorf("must not recreate an existing session: %v", existing.calls)
	}

	missing := &fakeRunner{errs: map[string]error{"has-session": errors.New("no such session")}}
	created, err = EnsureSession(context.Background(), missing, devRepo())
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("a missing session should be created")
	}
	if len(missing.called("new-session")) != 1 {
		t.Errorf("want a new-session call: %v", missing.calls)
	}
}

func TestStopAndRestartProcess(t *testing.T) {
	f := &fakeRunner{}
	if err := StopProcess(context.Background(), f, "app", "dev"); err != nil {
		t.Fatal(err)
	}
	if len(f.called("kill-window")) != 1 {
		t.Errorf("stop should kill the window: %v", f.calls)
	}

	g := &fakeRunner{}
	p := config.ProcessConfig{Name: "dev", Command: "npm run dev"}
	if err := RestartProcess(context.Background(), g, devRepo(p), p); err != nil {
		t.Fatal(err)
	}
	if len(g.called("respawn-window")) != 1 {
		t.Errorf("restart should respawn the window: %v", g.calls)
	}
}

func TestListSessionsUsesTheRunner(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-sessions": "app|2|1|1700000000||||\ndocs|1|0|1700000001||||\n",
	}}

	got, err := ListSessions(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got))
	}
	if got[0].Name != "app" || got[0].Windows != 2 || !got[0].Attached {
		t.Errorf("got %+v", got[0])
	}
}

func TestListSessionsWithoutAServer(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{"list-sessions": errors.New("no server running")}}

	got, err := ListSessions(context.Background(), f)
	if err != nil {
		t.Fatalf("no tmux server means no sessions, not an error: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestListSessionsDistinguishesMissingTmuxFromNoServer(t *testing.T) {
	// "no tmux server" means nothing is running. "tmux is not installed" means
	// we cannot know. Reporting the second as the first makes every answer a
	// confident lie — which is exactly what happens under launchd, where the
	// PATH does not include Homebrew.
	missing := &fakeRunner{errs: map[string]error{
		"list-sessions": fmt.Errorf("tmux list-sessions: %w", ErrTmuxNotFound),
	}}
	if _, err := ListSessions(context.Background(), missing); !errors.Is(err, ErrTmuxNotFound) {
		t.Errorf("a missing tmux binary must surface, got err = %v", err)
	}

	noServer := &fakeRunner{errs: map[string]error{
		"list-sessions": errors.New("no server running on /tmp/tmux-501/default"),
	}}
	got, err := ListSessions(context.Background(), noServer)
	if err != nil {
		t.Errorf("no server running is an answer, not a failure: %v", err)
	}
	if got != nil {
		t.Errorf("want no sessions, got %+v", got)
	}
}

func TestInspectProcessesSurfacesMissingTmux(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{
		"list-windows": fmt.Errorf("tmux list-windows: %w", ErrTmuxNotFound),
	}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "x"})

	if _, err := InspectProcesses(context.Background(), f, repo); !errors.Is(err, ErrTmuxNotFound) {
		t.Errorf("reporting every process as not_started because tmux is missing is a lie: err = %v", err)
	}
}

func TestExecRunnerReportsMissingTmux(t *testing.T) {
	r := ExecRunner{Binary: "tmux-definitely-not-installed", Timeout: 5 * time.Second}
	_, err := r.Run(context.Background(), "list-sessions")
	if !errors.Is(err, ErrTmuxNotFound) {
		t.Errorf("want ErrTmuxNotFound, got %v", err)
	}
}

func TestReconcileNeverLaunchesAgainstAnUnreachableHost(t *testing.T) {
	// A link drop makes has-session fail too, which locally would read as
	// "verified absent". Over ssh it is unknown, and unknown must not launch.
	f := &fakeRunner{errs: map[string]error{
		"list-windows": fmt.Errorf("ssh: %w", ErrHostUnreachable),
		"has-session":  fmt.Errorf("ssh: %w", ErrHostUnreachable),
	}}
	repo := devRepo(config.ProcessConfig{Name: "dev", Command: "npm run dev"})

	errs := ReconcileProcesses(context.Background(), f, repo)

	if got := f.called("new-window"); len(got) != 0 {
		t.Errorf("launched against an unreachable host: %v", got)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrHostUnreachable) {
		t.Fatalf("want the transport error surfaced, got %v", errs)
	}
}

func TestEnsureSessionDoesNotCreateOnAnUnreachableHost(t *testing.T) {
	// has-session failing because the link is down is not "no such session".
	f := &fakeRunner{errs: map[string]error{"has-session": fmt.Errorf("ssh: %w", ErrHostUnreachable)}}

	_, err := EnsureSession(context.Background(), f, devRepo())

	if !errors.Is(err, ErrHostUnreachable) {
		t.Fatalf("want the transport error, got %v", err)
	}
	if len(f.called("new-session")) != 0 {
		t.Error("a session must not be created on a host that cannot be reached")
	}
}
