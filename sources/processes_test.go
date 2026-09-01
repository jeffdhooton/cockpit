package sources

import (
	"context"
	"errors"
	"slices"
	"testing"

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
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t0\t222\t0\n",
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
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n",
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
	f := &fakeRunner{outputs: map[string]string{"list-windows": "0\tshell\t0\t111\t1\n"}}
	repo := devRepo(config.ProcessConfig{Name: "test", Command: "npm test", AutoStart: &off})

	ReconcileProcesses(context.Background(), f, repo)

	if len(f.called("new-window")) != 0 {
		t.Errorf("auto_start = false must not launch, calls: %v", f.calls)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t0\t222\t0\n",
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
		outputs: map[string]string{"list-windows": "0\tshell\t0\t111\t1\n"},
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

func TestStartProcessSetsRemainOnExit(t *testing.T) {
	f := &fakeRunner{}
	p := config.ProcessConfig{Name: "dev", Command: "npm run dev"}

	if err := StartProcess(context.Background(), f, devRepo(p), p); err != nil {
		t.Fatal(err)
	}
	if len(f.called("new-window")) != 1 {
		t.Errorf("want a new-window call, got %v", f.calls)
	}
	if len(f.called("set-window-option")) != 1 {
		t.Errorf("a crashed process must stay readable, so remain-on-exit is required: %v", f.calls)
	}
}

func TestStartProcessSurvivesRemainOnExitFailure(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{"set-window-option": errors.New("too old")}}
	p := config.ProcessConfig{Name: "dev", Command: "npm run dev"}

	if err := StartProcess(context.Background(), f, devRepo(p), p); err != nil {
		t.Errorf("remain-on-exit is a nicety, not a requirement: %v", err)
	}
}

func TestInspectProcessesClassifiesState(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n2\tscratch\t0\t333\t0\n",
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
