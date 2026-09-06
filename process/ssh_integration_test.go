package process

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// Live SSH acceptance against an explicitly designated test host. It runs
// only when COCKPIT_SSH_TEST_HOST names a configured ssh alias and
// COCKPIT_SSH_TEST_TMUX names a remote tmux wrapper bound to a private
// socket, so nothing here can touch that machine's own sessions. The
// remote repo directory is COCKPIT_SSH_TEST_REPO (a throwaway checkout).
func sshTestHost(t *testing.T) (config.HostConfig, config.RepoConfig) {
	t.Helper()
	host := os.Getenv("COCKPIT_SSH_TEST_HOST")
	tmux := os.Getenv("COCKPIT_SSH_TEST_TMUX")
	repo := os.Getenv("COCKPIT_SSH_TEST_REPO")
	if host == "" || tmux == "" || repo == "" {
		t.Skip("set COCKPIT_SSH_TEST_HOST, COCKPIT_SSH_TEST_TMUX and COCKPIT_SSH_TEST_REPO to run the live ssh test")
	}
	return config.HostConfig{Name: host, Tmux: tmux},
		config.RepoConfig{Host: host, Label: "site", Path: repo, Processes: []config.ProcessConfig{
			{Name: "dev", Command: "sleep 300"},
			{Name: "worker", Command: "sleep 300"},
			{Name: "crash", Command: "echo remote-boom; exit 3", AutoStart: boolPtr(false)},
		}}
}

func boolPtr(b bool) *bool { return &b }

func sshService(t *testing.T) (*Service, config.RepoConfig, sources.SSHRunner) {
	t.Helper()
	host, repo := sshTestHost(t)
	cfg := &config.Config{Hosts: []config.HostConfig{host}, Repos: []config.RepoConfig{
		// A local project with the same label, to prove routing.
		{Label: "site", Path: t.TempDir()},
		repo,
	}}
	cfg.General.SessionName = "cockpit"
	// A unix socket path is limited to ~104 bytes, so the control directory
	// must be short; it is test-owned and removed on cleanup.
	control, err := os.MkdirTemp("/tmp", "cockpit-ssh-ctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(control) })
	remote := sources.SSHRunner{Host: host.Name, Tmux: host.Tmux, ControlDir: control, Timeout: 20 * time.Second}
	local := sources.ExecRunner{Timeout: 10 * time.Second, NoConfig: true, Socket: "cockpit-ssh-local-" + strings.ReplaceAll(t.Name(), "/", "-")}
	s := New(cfg, local)
	s.Remote = func(config.HostConfig) sources.Runner { return remote }
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = remote.Run(ctx, "kill-server")
		_, _ = local.Run(ctx, "kill-server")
	})
	// Start clean.
	_, _ = remote.Run(context.Background(), "kill-server")
	return s, repo, remote
}

func TestLiveSSHLifecycleAndStopPersistence(t *testing.T) {
	s, repo, remote := sshService(t)
	ctx := context.Background()

	// Inspecting a dormant remote project launches nothing.
	obs, err := s.Inspect(ctx, "mini/site")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if obs.SessionExists {
		t.Fatalf("remote session should not exist yet: %+v", obs)
	}
	if out, err := remote.Run(ctx, "list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Fatalf("inspection created something remotely: %q", out)
	}

	// Start one process: shell + that window only, remotely.
	res := s.Start(ctx, "mini/site", "worker")
	if res.Outcome != OutcomeStarted {
		t.Fatalf("start: %+v", res)
	}
	windows, err := sources.ListWindows(ctx, remote, "site")
	if err != nil || len(windows) != 2 || windows[1].Managed != "worker" {
		t.Fatalf("remote windows = %+v (%v)", windows, err)
	}
	local := s.Local
	if out, err := local.Run(ctx, "list-sessions"); err == nil && strings.TrimSpace(out) != "" {
		t.Errorf("a remote start must not touch the local server: %q", out)
	}

	// Concurrent starts of the sibling from two clients: one window.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Start(ctx, "mini/site", "dev") }()
	}
	wg.Wait()
	windows, _ = sources.ListWindows(ctx, remote, "site")
	devs := 0
	for _, w := range windows {
		if w.Managed == "dev" {
			devs++
		}
	}
	if devs != 1 {
		t.Fatalf("concurrent remote starts made %d dev windows: %+v", devs, windows)
	}

	// Stop persists across re-entry (Prepare = project entry) and a fresh
	// service (a restarted TUI/daemon on another client).
	if res := s.Stop(ctx, "mini/site", "dev"); res.Outcome != OutcomeStopped {
		t.Fatalf("stop: %+v", res)
	}
	if _, errs, err := s.Prepare(ctx, remote, repo); err != nil || len(errs) != 0 {
		t.Fatalf("prepare: %v %v", err, errs)
	}
	fresh := New(s.Cfg, s.Local)
	fresh.Remote = s.Remote
	fresh.Reconcile(ctx, repo)
	obs, _ = fresh.Inspect(ctx, "mini/site")
	m := byName(obs)
	if m["dev"].Outcome != sources.OutcomeStopped || m["worker"].Outcome != sources.OutcomeRunning {
		t.Fatalf("after re-entry: dev=%+v worker=%+v", m["dev"], m["worker"])
	}
	if res := fresh.Start(ctx, "mini/site", "dev"); res.Outcome != OutcomeStarted {
		t.Fatalf("resume: %+v", res)
	}

	// A crash keeps its output and code remotely.
	if res := fresh.Start(ctx, "mini/site", "crash"); res.Outcome != OutcomeStarted {
		t.Fatalf("crash start: %+v", res)
	}
	time.Sleep(500 * time.Millisecond)
	obs, _ = fresh.Inspect(ctx, "mini/site")
	if c := byName(obs)["crash"]; c.Outcome != sources.OutcomeExited || c.ExitCode == nil || *c.ExitCode != 3 {
		t.Fatalf("crash = %+v", c)
	}
	out, err := fresh.ReadOutput(ctx, "mini/site", "crash", 50)
	if err != nil || !strings.Contains(out.Text, "remote-boom") {
		t.Fatalf("remote output: %v %q", err, out.Text)
	}
}

func TestLiveSSHUnreachableHostPreventsMutationAndReconnectDoesNotDuplicate(t *testing.T) {
	s, _, remote := sshService(t)
	ctx := context.Background()
	if res := s.Start(ctx, "mini/site", "worker"); res.Outcome != OutcomeStarted {
		t.Fatalf("start: %+v", res)
	}

	// Drop the link: route the host through an alias ssh cannot resolve.
	dead := sources.SSHRunner{Host: "cockpit-no-such-host.invalid", Tmux: remote.Tmux, ControlDir: remote.ControlDir, Timeout: 15 * time.Second}
	s.Remote = func(config.HostConfig) sources.Runner { return dead }
	obs, err := s.Inspect(ctx, "mini/site")
	if err == nil {
		t.Fatalf("an unreachable host must be unavailable, got %+v", obs)
	}
	for _, act := range []func() Result{
		func() Result { return s.Start(ctx, "mini/site", "dev") },
		func() Result { return s.Stop(ctx, "mini/site", "worker") },
		func() Result { return s.Restart(ctx, "mini/site", "worker") },
	} {
		if res := act(); res.Outcome != OutcomeUnavailable {
			t.Errorf("mutation on an unreachable host: %+v", res)
		}
	}
	// The queue keeps last-known items as stale while the host is down.
	rep := sources.DeriveAttention(sources.AttentionInput{Hosts: []sources.HostReport{{
		Host: "mini", Outcome: sources.ObservationUnavailable, Err: "host unreachable",
		Processes: map[string]sources.ProcessObservation{"mini/site": {Session: "site", Processes: []sources.ProcessInfo{
			{Name: "crash", Configured: true, Outcome: sources.OutcomeExited, WindowID: "@9"},
		}}},
	}}, Now: time.Now()})
	if len(rep.Items) != 1 || rep.Items[0].Observation != sources.ObservationStale || rep.Unavailable != 1 {
		t.Errorf("stale retention: %+v", rep)
	}

	// Reconnect: the same request again creates nothing new.
	s.Remote = func(config.HostConfig) sources.Runner { return remote }
	if res := s.Start(ctx, "mini/site", "worker"); res.Outcome != OutcomeAlreadyRunning {
		t.Fatalf("after reconnect: %+v", res)
	}
	windows, _ := sources.ListWindows(ctx, remote, "site")
	workers := 0
	for _, w := range windows {
		if w.Managed == "worker" {
			workers++
		}
	}
	if workers != 1 {
		t.Errorf("reconnect duplicated the worker: %+v", windows)
	}
}

func TestLiveSSHBlockedRemotePaneIsQueuedAndAttachValidates(t *testing.T) {
	s, _, remote := sshService(t)
	ctx := context.Background()
	if res := s.Start(ctx, "mini/site", "worker"); res.Outcome != OutcomeStarted {
		t.Fatalf("start: %+v", res)
	}
	// Simulate the remote hook's pane report (the installed remote binary
	// predates pane reports; the record shape is what the hook writes).
	host, err := remote.RemoteNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := sources.ObserveHost(ctx, remote, "mini", host)
	if err != nil || len(obs.Panes) < 2 {
		t.Fatalf("observe: %v %+v", err, obs)
	}
	target := obs.Panes[1]
	if _, err := remote.Run(ctx, sources.SetPaneStatusArgs(target.PaneID, sources.AgentStatusNeedsInput, "inv-remote", 1, host)...); err != nil {
		t.Fatal(err)
	}
	rep := sources.ObserveHostReport(ctx, remote, remote, "mini", nil, host, time.Now())
	queue := sources.DeriveAttention(sources.AttentionInput{Hosts: []sources.HostReport{rep}, Now: time.Now()})
	if queue.Actionable() != 1 || queue.Items[0].Target.PaneID != target.PaneID || queue.Items[0].Target.Host != "mini" {
		t.Fatalf("queue = %+v", queue.Items)
	}

	// Make another pane active remotely, then attach: the reporting pane
	// is selected and the local view window attaches without creating.
	if _, err := remote.Run(ctx, "select-window", "-t", obs.Panes[0].WindowID); err != nil {
		t.Fatal(err)
	}
	at := sources.AttachTarget{Host: "mini", Generation: target.Generation, Session: "site", SessionID: target.SessionID, WindowID: target.WindowID, PaneID: target.PaneID}
	hc := config.HostConfig{Name: os.Getenv("COCKPIT_SSH_TEST_HOST"), Tmux: os.Getenv("COCKPIT_SSH_TEST_TMUX")}
	err = sources.AttachRemote(ctx, s.Local, remote, hc, at)
	// The private local server has no attached client, so the final
	// switch-client cannot succeed here; everything before it must have.
	if err != nil && !strings.Contains(err.Error(), "no current client") && !strings.Contains(err.Error(), "no client") {
		t.Fatalf("attach: %v", err)
	}
	cur, _ := remote.Run(ctx, "display-message", "-p", "-t", "site", "#{window_id}")
	if strings.TrimSpace(cur) != target.WindowID {
		t.Errorf("remote window not selected: %q want %s", cur, target.WindowID)
	}
	views, err := sources.ListWindows(ctx, s.Local, hc.Name)
	if err != nil || len(views) != 1 || views[0].Name != "site" {
		t.Errorf("local view = %+v (%v)", views, err)
	}
	if n, _ := remote.Run(ctx, "list-sessions", "-F", "#{session_name}"); strings.Count(strings.TrimSpace(n), "\n")+1 != 1 {
		t.Errorf("attach created a remote session: %q", n)
	}

	// The pane is gone: attach fails visibly, creates nothing.
	if _, err := remote.Run(ctx, "kill-pane", "-t", target.PaneID); err != nil {
		t.Fatal(err)
	}
	if err := sources.AttachRemote(ctx, s.Local, remote, hc, at); err != sources.ErrTargetGone {
		t.Errorf("want ErrTargetGone, got %v", err)
	}
}
