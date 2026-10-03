package process

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// These tests drive a private tmux server (-L, no config) so nothing they do
// can touch the user's sessions. They are the evidence for the lifecycle
// acceptance items that argv-level tests cannot give.

func privateTmux(t *testing.T) sources.ExecRunner {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, t.Name())
	r := sources.ExecRunner{Timeout: 10 * time.Second, NoConfig: true, Socket: "cockpit-proc-" + name + "-" + strconv.Itoa(os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run(context.Background(), "kill-server") })
	return r
}

func liveService(t *testing.T, procs ...config.ProcessConfig) (*Service, config.RepoConfig, sources.ExecRunner) {
	t.Helper()
	r := privateTmux(t)
	repo := config.RepoConfig{Label: "proj", Path: t.TempDir(), Processes: procs}
	cfg := &config.Config{Repos: []config.RepoConfig{repo}}
	cfg.General.SessionName = "cockpit"
	return New(cfg, r), repo, r
}

func byName(obs sources.ProcessObservation) map[string]sources.ProcessInfo {
	m := map[string]sources.ProcessInfo{}
	for _, p := range obs.Processes {
		m[p.Name] = p
	}
	return m
}

func settle() { time.Sleep(300 * time.Millisecond) }

func TestIntegrationInspectDormantProjectLaunchesNothing(t *testing.T) {
	s, _, r := liveService(t, config.ProcessConfig{Name: "dev", Command: "sleep 60"})
	obs, err := s.Inspect(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if obs.SessionExists || byName(obs)["dev"].Outcome != sources.OutcomeNotStarted {
		t.Errorf("obs = %+v", obs)
	}
	if out, err := r.Run(context.Background(), "list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Errorf("inspection created a session: %q", out)
	}
}

func TestIntegrationStartCreatesShellAndOneWindow(t *testing.T) {
	s, _, r := liveService(t,
		config.ProcessConfig{Name: "dev", Command: "sleep 60"},
		config.ProcessConfig{Name: "worker", Command: "sleep 60"},
	)
	ctx := context.Background()
	res := s.Start(ctx, "proj", "worker")
	if res.Outcome != OutcomeStarted {
		t.Fatalf("got %+v", res)
	}
	windows, err := sources.ListWindows(ctx, r, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 {
		t.Fatalf("want shell + worker, got %+v", windows)
	}
	if windows[1].Name != "worker" || windows[1].Managed != "worker" {
		t.Errorf("worker window = %+v", windows[1])
	}
	if byName(*res.Observation)["dev"].Outcome != sources.OutcomeNotStarted {
		t.Error("the sibling auto-start process must not launch on a single-row start")
	}

	// A second start is a no-op; a concurrent burst yields one window.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Start(ctx, "proj", "dev") }()
	}
	wg.Wait()
	windows, _ = sources.ListWindows(ctx, r, "proj")
	devs := 0
	for _, w := range windows {
		if w.Name == "dev" {
			devs++
		}
	}
	if devs != 1 {
		t.Errorf("concurrent starts produced %d dev windows: %+v", devs, windows)
	}
}

func TestIntegrationExitCodesAndCompletion(t *testing.T) {
	s, _, _ := liveService(t,
		config.ProcessConfig{Name: "crash", Command: "echo boom-output; exit 3"},
		config.ProcessConfig{Name: "done", Command: "exit 0"},
	)
	ctx := context.Background()
	if res := s.Start(ctx, "proj", "crash"); res.Outcome != OutcomeStarted {
		t.Fatalf("got %+v", res)
	}
	if res := s.Start(ctx, "proj", "done"); res.Outcome != OutcomeStarted {
		t.Fatalf("got %+v", res)
	}
	settle()
	obs, err := s.Inspect(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	m := byName(obs)
	if c := m["crash"]; c.Outcome != sources.OutcomeExited || c.ExitCode == nil || *c.ExitCode != 3 {
		t.Errorf("crash = %+v", c)
	}
	if d := m["done"]; d.Outcome != sources.OutcomeCompleted || d.Display != "Completed" {
		t.Errorf("done = %+v", d)
	}
	out, err := s.ReadOutput(ctx, "proj", "crash", 50)
	if err != nil || !strings.Contains(out.Text, "boom-output") {
		t.Errorf("crash output must be readable: %v %q", err, out.Text)
	}
}

func TestIntegrationStopSurvivesReentryAndRestart(t *testing.T) {
	s, repo, r := liveService(t,
		config.ProcessConfig{Name: "dev", Command: "sleep 60"},
		config.ProcessConfig{Name: "worker", Command: "sleep 60"},
	)
	ctx := context.Background()
	if _, errs, err := s.Prepare(ctx, r, repo); err != nil || len(errs) != 0 {
		t.Fatalf("prepare: %v %v", err, errs)
	}
	if res := s.Stop(ctx, "proj", "dev"); res.Outcome != OutcomeStopped {
		t.Fatalf("stop: %+v", res)
	}

	// Project entry reconciles; the stopped process must stay down.
	if _, errs, err := s.Prepare(ctx, r, repo); err != nil || len(errs) != 0 {
		t.Fatalf("re-enter: %v %v", err, errs)
	}
	// A fresh service is a restarted TUI/daemon: the intent lives in tmux.
	fresh := New(s.Cfg, r)
	fresh.Reconcile(ctx, repo)
	obs, _ := fresh.Inspect(ctx, "proj")
	m := byName(obs)
	if m["dev"].Outcome != sources.OutcomeStopped || m["dev"].Display != "Stopped by you" {
		t.Errorf("dev after re-entry = %+v", m["dev"])
	}
	if m["worker"].Outcome != sources.OutcomeRunning {
		t.Errorf("worker must keep its policy: %+v", m["worker"])
	}

	// Explicit start resumes it.
	if res := fresh.Start(ctx, "proj", "dev"); res.Outcome != OutcomeStarted {
		t.Fatalf("start: %+v", res)
	}
	obs, _ = fresh.Inspect(ctx, "proj")
	if d := byName(obs)["dev"]; d.Outcome != sources.OutcomeRunning || d.DesiredState != sources.DesiredRunning {
		t.Errorf("dev after start = %+v", d)
	}
}

func TestIntegrationStopTargetsIdentityDespiteRenamesAndMoves(t *testing.T) {
	s, _, r := liveService(t,
		config.ProcessConfig{Name: "dev", Command: "sleep 60"},
		config.ProcessConfig{Name: "other", Command: "sleep 60"},
	)
	ctx := context.Background()
	s.Start(ctx, "proj", "dev")
	s.Start(ctx, "proj", "other")
	windows, _ := sources.ListWindows(ctx, r, "proj")
	var devID, otherID string
	for _, w := range windows {
		switch w.Managed {
		case "dev":
			devID = w.ID
		case "other":
			otherID = w.ID
		}
	}
	// Reorder and rename so index and name both lie.
	if _, err := r.Run(ctx, "move-window", "-s", devID, "-t", "proj:9"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "rename-window", "-t", otherID, "dev"); err != nil {
		t.Fatal(err)
	}
	res := s.Stop(ctx, "proj", "dev")
	if res.Outcome != OutcomeStopped {
		t.Fatalf("stop: %+v", res)
	}
	windows, _ = sources.ListWindows(ctx, r, "proj")
	for _, w := range windows {
		if w.ID == devID {
			t.Errorf("the managed dev window survived the stop: %+v", w)
		}
	}
	found := false
	for _, w := range windows {
		if w.ID == otherID && !w.Dead {
			found = true
		}
	}
	if !found {
		t.Errorf("the renamed neighbour was killed: %+v", windows)
	}
}

func TestIntegrationSplitAndLegacyWindowsAreProtected(t *testing.T) {
	s, _, r := liveService(t,
		config.ProcessConfig{Name: "dev", Command: "sleep 60"},
		config.ProcessConfig{Name: "legacy", Command: "sleep 60"},
	)
	ctx := context.Background()
	s.Start(ctx, "proj", "dev")
	// Split the managed window; a stop must now refuse.
	if _, err := r.Run(ctx, "split-window", "-d", "-t", "proj:dev", "sleep 60"); err != nil {
		t.Fatal(err)
	}
	if res := s.Stop(ctx, "proj", "dev"); res.Outcome != OutcomeBlocked || !strings.Contains(res.Message, "split") {
		t.Errorf("split stop = %+v", res)
	}
	if res := s.Restart(ctx, "proj", "dev"); res.Outcome != OutcomeBlocked {
		t.Errorf("split restart = %+v", res)
	}

	// A pre-existing unmarked window named for a configured process.
	if _, err := r.Run(ctx, "new-window", "-d", "-t", "proj:", "-n", "legacy", "sleep 60"); err != nil {
		t.Fatal(err)
	}
	obs, _ := s.Inspect(ctx, "proj")
	leg := byName(obs)["legacy"]
	if !leg.Adoptable || leg.Managed || leg.Controllable() {
		t.Fatalf("legacy = %+v", leg)
	}
	if res := s.Start(ctx, "proj", "legacy"); res.Outcome != OutcomeBlocked {
		t.Errorf("start over a legacy window = %+v", res)
	}
	if res := s.Stop(ctx, "proj", "legacy"); res.Outcome != OutcomeBlocked {
		t.Errorf("stop of a legacy window = %+v", res)
	}
	pidBefore := leg.PanePID
	if res := s.Adopt(ctx, "proj", "legacy", leg.WindowID); res.Outcome != OutcomeAdopted {
		t.Fatalf("adopt = %+v", res)
	}
	obs, _ = s.Inspect(ctx, "proj")
	leg = byName(obs)["legacy"]
	if !leg.Managed || !leg.Controllable() || leg.PanePID != pidBefore {
		t.Errorf("adoption must mark without relaunching: %+v", leg)
	}
	if res := s.Stop(ctx, "proj", "legacy"); res.Outcome != OutcomeStopped {
		t.Errorf("stop after adoption = %+v", res)
	}
}
