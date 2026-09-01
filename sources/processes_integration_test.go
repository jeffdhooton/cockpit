package sources

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/jhoot/cockpit/config"
)

// requireTmux skips when there is no tmux to drive. These tests talk to a real
// server, because the argv-level tests cannot catch things like base-index
// making window 0 nonexistent.
func requireTmux(t *testing.T) Runner {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	return ExecRunner{Timeout: 10 * time.Second}
}

// scratchRepo builds a throwaway session name and guarantees teardown.
func scratchRepo(t *testing.T, r Runner, procs ...config.ProcessConfig) config.RepoConfig {
	t.Helper()
	label := "cockpit-it-" + t.Name()
	label = sanitizeTestLabel(label)

	repo := config.RepoConfig{Label: label, Path: t.TempDir(), Processes: procs}
	t.Cleanup(func() {
		_, _ = r.Run(context.Background(), "kill-session", "-t", label)
	})
	return repo
}

func sanitizeTestLabel(s string) string {
	out := []rune{}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func TestIntegrationJumpCreatesSessionAndProcessWindows(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()

	repo := scratchRepo(t, r,
		config.ProcessConfig{Name: "ticker", Command: "while true; do sleep 1; done"},
		config.ProcessConfig{Name: "manual", Command: "sleep 60", AutoStart: boolPtr(false)},
	)

	created, err := EnsureSession(ctx, r, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("a fresh label should produce a new session")
	}
	if errs := ReconcileProcesses(ctx, r, repo); len(errs) != 0 {
		t.Fatalf("reconcile errors: %v", errs)
	}

	infos, err := InspectProcesses(ctx, r, repo)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ProcessInfo{}
	for _, i := range infos {
		byName[i.Name] = i
	}
	if got := byName["ticker"]; got.State != ProcessRunning {
		t.Errorf("ticker should be running, got %+v", got)
	}
	if got := byName["manual"]; got.State != ProcessNotStarted {
		t.Errorf("manual is not auto-start, so it should not be running: %+v", got)
	}
}

func TestIntegrationSelectFirstWindowWorksUnderAnyBaseIndex(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()

	repo := scratchRepo(t, r, config.ProcessConfig{Name: "ticker", Command: "while true; do sleep 1; done"})

	if _, err := EnsureSession(ctx, r, repo); err != nil {
		t.Fatal(err)
	}
	if errs := ReconcileProcesses(ctx, r, repo); len(errs) != 0 {
		t.Fatalf("reconcile errors: %v", errs)
	}

	// Focus a process window, then ask to go back to the shell.
	if _, err := r.Run(ctx, "select-window", "-t", Target(repo.Label, "ticker")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, SelectFirstWindowArgs(repo.Label)...); err != nil {
		t.Fatalf("selecting the first window must work whatever base-index is set to: %v", err)
	}

	windows, err := ListWindows(ctx, r, repo.Label)
	if err != nil {
		t.Fatal(err)
	}
	lowest := windows[0]
	for _, w := range windows {
		if w.Index < lowest.Index {
			lowest = w
		}
	}
	for _, w := range windows {
		if w.Active && w.Index != lowest.Index {
			t.Errorf("landed on window %d (%s), want the lowest-numbered window %d", w.Index, w.Name, lowest.Index)
		}
	}
}

func TestIntegrationDeadProcessStaysReadable(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()

	repo := scratchRepo(t, r, config.ProcessConfig{
		Name:    "boom",
		Command: "echo 'fatal: everything is on fire'; exit 1",
	})

	if _, err := EnsureSession(ctx, r, repo); err != nil {
		t.Fatal(err)
	}
	if errs := ReconcileProcesses(ctx, r, repo); len(errs) != 0 {
		t.Fatalf("reconcile errors: %v", errs)
	}

	// Give the command time to run and exit.
	deadline := time.Now().Add(5 * time.Second)
	var boom ProcessInfo
	for time.Now().Before(deadline) {
		infos, err := InspectProcesses(ctx, r, repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range infos {
			if i.Name == "boom" {
				boom = i
			}
		}
		if boom.State == ProcessDead {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if boom.State != ProcessDead {
		t.Fatalf("a command that exits should leave a dead window, got %+v", boom)
	}

	out, err := r.Run(ctx, CapturePaneArgs(Target(repo.Label, "boom"), 50)...)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "everything is on fire") {
		t.Errorf("the crash output must survive the exit, got %q", out)
	}
}

func TestIntegrationReconcileRespawnsWithoutDuplicating(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()

	repo := scratchRepo(t, r, config.ProcessConfig{
		Name:    "boom",
		Command: "echo dying; exit 1",
	})

	if _, err := EnsureSession(ctx, r, repo); err != nil {
		t.Fatal(err)
	}
	ReconcileProcesses(ctx, r, repo)
	time.Sleep(time.Second)

	// A second jump should reuse the dead window, not add another.
	ReconcileProcesses(ctx, r, repo)
	time.Sleep(time.Second)

	windows, err := ListWindows(ctx, r, repo.Label)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, w := range windows {
		if w.Name == "boom" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want exactly one boom window after two reconciles, got %d: %+v", count, windows)
	}
}

func boolPtr(b bool) *bool { return &b }

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
