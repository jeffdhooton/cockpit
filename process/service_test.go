package process

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// scriptRunner answers tmux calls from a closure, so a test can change what
// list-windows says after a mutation and inject a failure at one exact step.
type scriptRunner struct {
	calls  [][]string
	answer func(args []string) (string, error)
}

func (s *scriptRunner) Run(_ context.Context, args ...string) (string, error) {
	s.calls = append(s.calls, args)
	if s.answer == nil {
		return "", nil
	}
	return s.answer(args)
}

func (s *scriptRunner) called(verb string) [][]string {
	var out [][]string
	for _, c := range s.calls {
		if len(c) > 0 && c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

// tmuxSim is a tiny in-memory model of one session's windows and options,
// enough to drive the service through its transactions.
type tmuxSim struct {
	exists  bool
	windows []string // list-windows lines
	options map[string]string
	lock    string
	fail    map[string]error // verb → error to inject once
}

func newSim() *tmuxSim {
	return &tmuxSim{options: map[string]string{}, fail: map[string]error{}}
}

func (t *tmuxSim) runner() *scriptRunner {
	return &scriptRunner{answer: t.answer}
}

func (t *tmuxSim) answer(args []string) (string, error) {
	verb := args[0]
	if err, ok := t.fail[verb]; ok {
		delete(t.fail, verb)
		return "", err
	}
	switch verb {
	case "if-shell":
		// Acquire or release: a naive but exclusive model.
		if strings.Contains(args[3], "-gu") {
			t.lock = ""
		} else if t.lock == "" {
			parts := strings.Fields(args[3])
			t.lock = parts[len(parts)-1]
		}
		return "", nil
	case "display-message":
		return t.lock + "\n", nil
	case "list-windows":
		if !t.exists {
			return "", errors.New("can't find session: app")
		}
		return strings.Join(t.windows, "\n") + "\n", nil
	case "show-options":
		var lines []string
		for k, v := range t.options {
			lines = append(lines, k+" "+v)
		}
		return strings.Join(lines, "\n"), nil
	case "set-option":
		if slices.Contains(args, "-u") {
			delete(t.options, args[len(args)-1])
		} else {
			t.options[args[len(args)-2]] = args[len(args)-1]
		}
		return "", nil
	case "has-session":
		if !t.exists {
			return "", errors.New("can't find session: app")
		}
		return "", nil
	case "new-session":
		t.exists = true
		t.windows = []string{"0|zsh|0|100|1||@0|%0|1||500|600"}
		return "", nil
	case "new-window":
		name := args[slices.Index(args, "-n")+1]
		id := len(t.windows)
		t.windows = append(t.windows, fmt.Sprintf("%d|%s|0|%d|0||@%d|%%%d|1|%s|500|600", id, name, 200+id, id, id, name))
		return "", nil
	case "kill-window":
		id := args[slices.Index(args, "-t")+1]
		t.windows = slices.DeleteFunc(t.windows, func(l string) bool { return strings.Contains(l, "|"+id+"|") })
		return "", nil
	case "respawn-window":
		id := args[slices.Index(args, "-t")+1]
		for i, l := range t.windows {
			if strings.Contains(l, "|"+id+"|") {
				parts := strings.Split(l, "|")
				parts[2] = "0"
				parts[5] = ""
				t.windows[i] = strings.Join(parts, "|")
			}
		}
		return "", nil
	}
	return "", nil
}

func appCfg() *config.Config {
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{Label: "app", Path: "/r", Processes: []config.ProcessConfig{
				{Name: "dev", Command: "npm run dev"},
				{Name: "worker", Command: "npm run worker"},
			}},
			{Label: "app", Host: "mini", Path: "~/r", Processes: []config.ProcessConfig{
				{Name: "dev", Command: "npm run dev"},
			}},
		},
		Hosts: []config.HostConfig{{Name: "mini", Tmux: "/opt/homebrew/bin/tmux"}},
	}
	cfg.General.SessionName = "cockpit"
	return cfg
}

func svc(local sources.Runner, remote sources.Runner) *Service {
	s := New(appCfg(), local)
	s.Remote = func(config.HostConfig) sources.Runner { return remote }
	return s
}

func TestResolveNeverCrossesHosts(t *testing.T) {
	local, remote := &scriptRunner{}, &scriptRunner{}
	s := svc(local, remote)

	if _, r, err := s.Resolve("app"); err != nil || r != local {
		t.Errorf("bare label must be the local project: %v", err)
	}
	if _, r, err := s.Resolve("mini/app"); err != nil || r != remote {
		t.Errorf("qualified key must be the remote runner: %v", err)
	}
	if _, _, err := s.Resolve("ghost"); !errors.Is(err, ErrAmbiguousProject) {
		t.Errorf("unknown bare label must not resolve: %v", err)
	}
	if _, _, err := s.Resolve("mini/ghost"); err == nil {
		t.Error("unknown remote project must not resolve")
	}
	s.Cfg.Repos = s.Cfg.Repos[1:] // only the remote app remains
	if _, _, err := s.Resolve("app"); err == nil {
		t.Error("a bare label must never be reinterpreted as a remote project")
	}
}

func TestStartCreatesOnlyTheShellAndTheOneWindow(t *testing.T) {
	sim := newSim()
	r := sim.runner()
	s := svc(r, nil)

	res := s.Start(context.Background(), "app", "worker")
	if res.Outcome != OutcomeStarted {
		t.Fatalf("got %+v", res)
	}
	if len(r.called("new-session")) != 1 || len(r.called("new-window")) != 1 {
		t.Errorf("want one session and one window, calls: %v", r.calls)
	}
	if !slices.Contains(r.called("new-window")[0], "worker") {
		t.Errorf("the wrong process was launched: %v", r.called("new-window"))
	}
	if got := res.Observation; got == nil || len(got.Processes) < 2 {
		t.Fatalf("a re-read must follow the action: %+v", res)
	}
	// dev, the sibling auto-start process, was not launched.
	for _, p := range res.Observation.Processes {
		if p.Name == "dev" && p.Outcome != sources.OutcomeNotStarted {
			t.Errorf("starting one row must not launch its siblings: %+v", p)
		}
	}
	if sim.lock != "" {
		t.Error("lock not released")
	}
}

func TestStartIsANoOpWhenRunning(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"0|zsh|0|100|1||@0|%0|1||500|600", "1|dev|0|200|0||@1|%1|1|dev|500|600"}
	r := sim.runner()
	res := svc(r, nil).Start(context.Background(), "app", "dev")
	if res.Outcome != OutcomeAlreadyRunning || len(r.called("new-window")) != 0 {
		t.Errorf("got %+v calls %v", res, r.calls)
	}
}

func TestStartRefusesAnUnmanagedNameCollision(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"0|zsh|0|100|1||@0|%0|1||500|600", "1|dev|0|200|0||@1|%1|1||500|600"}
	r := sim.runner()
	res := svc(r, nil).Start(context.Background(), "app", "dev")
	if res.Outcome != OutcomeBlocked || !strings.Contains(res.Message, "adopt") {
		t.Errorf("got %+v", res)
	}
	if len(r.called("new-window")) != 0 || len(r.called("respawn-window")) != 0 {
		t.Errorf("a collision must launch nothing: %v", r.calls)
	}
}

func TestStopRecordsIntentBeforeKilling(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"0|zsh|0|100|1||@0|%0|1||500|600", "1|dev|0|200|0||@1|%1|1|dev|500|600"}
	r := sim.runner()
	res := svc(r, nil).Stop(context.Background(), "app", "dev")
	if res.Outcome != OutcomeStopped {
		t.Fatalf("got %+v", res)
	}
	var order []string
	for _, c := range r.calls {
		if c[0] == "set-option" && strings.Contains(strings.Join(c, " "), "@cockpit_stopped_dev") {
			order = append(order, "override")
		}
		if c[0] == "kill-window" {
			order = append(order, "kill")
			if !slices.Contains(c, "@1") {
				t.Errorf("kill must address the window id: %v", c)
			}
		}
	}
	if strings.Join(order, ",") != "override,kill" {
		t.Errorf("stop intent must be recorded before the kill, got %v", order)
	}
	if res.Observation == nil {
		t.Fatal("no re-read")
	}
	for _, p := range res.Observation.Processes {
		if p.Name == "dev" && (p.Outcome != sources.OutcomeStopped || p.DesiredState != sources.DesiredStopped) {
			t.Errorf("dev after stop = %+v", p)
		}
	}
}

func TestStopDoesNotKillWhenIntentCannotBeRecorded(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"1|dev|0|200|0||@1|%1|1|dev|500|600"}
	sim.fail["set-option"] = errors.New("disk full")
	r := sim.runner()
	res := svc(r, nil).Stop(context.Background(), "app", "dev")
	if res.Outcome != OutcomeBlocked || len(r.called("kill-window")) != 0 {
		t.Errorf("without a recorded intent nothing may be killed: %+v %v", res, r.calls)
	}
}

func TestStopReportsPartialWhenKillFailsAfterIntent(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"1|dev|0|200|0||@1|%1|1|dev|500|600"}
	sim.fail["kill-window"] = errors.New("boom")
	r := sim.runner()
	res := svc(r, nil).Stop(context.Background(), "app", "dev")
	if res.Outcome != OutcomePartial || !strings.Contains(res.Message, "may still be running") {
		t.Errorf("got %+v", res)
	}
	if _, ok := sim.options["@cockpit_stopped_dev"]; !ok {
		t.Error("the recorded intent must remain")
	}
}

func TestStopRefusesSplitAndUnmanagedWindows(t *testing.T) {
	for _, line := range []string{
		"1|dev|0|200|0||@1|%1|2|dev|500|600", // split
		"1|dev|0|200|0||@1|%1|1||500|600",    // unmanaged
	} {
		sim := newSim()
		sim.exists = true
		sim.windows = []string{line}
		r := sim.runner()
		res := svc(r, nil).Stop(context.Background(), "app", "dev")
		if res.Outcome != OutcomeBlocked || len(r.called("kill-window")) != 0 {
			t.Errorf("%s: got %+v calls %v", line, res, r.calls)
		}
	}
}

func TestRestartClearsOverrideAndRespawnsByID(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"1|dev|1|200|0|1|@1|%1|1|dev|500|600"}
	sim.options["@cockpit_stopped_dev"] = "v1:1"
	r := sim.runner()
	res := svc(r, nil).Restart(context.Background(), "app", "dev")
	if res.Outcome != OutcomeRestarted {
		t.Fatalf("got %+v", res)
	}
	if _, ok := sim.options["@cockpit_stopped_dev"]; ok {
		t.Error("restart must not leave a stop override behind")
	}
	if got := r.called("respawn-window"); len(got) != 1 || !slices.Contains(got[0], "@1") {
		t.Errorf("respawn by id, got %v", got)
	}
}

func TestLostResponseAfterDispatchIsUnknownNotRetried(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"1|dev|0|200|0||@1|%1|1|dev|500|600"}
	sim.fail["kill-window"] = fmt.Errorf("%w: connection closed", sources.ErrHostUnreachable)
	r := sim.runner()
	res := svc(r, nil).Stop(context.Background(), "app", "dev")
	if res.Outcome != OutcomeUnknown || !strings.Contains(res.Message, "may have run") {
		t.Errorf("got %+v", res)
	}
	if len(r.called("kill-window")) != 1 {
		t.Errorf("a lost response must not be retried: %v", r.called("kill-window"))
	}
	if res.Err() == nil {
		t.Error("an unknown outcome must surface as an error to error-only callers")
	}
}

func TestUnreadableWindowsBlockEveryMutation(t *testing.T) {
	sim := newSim()
	sim.exists = true
	r := &scriptRunner{answer: func(args []string) (string, error) {
		if args[0] == "list-windows" {
			return "", errors.New("permission denied")
		}
		return sim.answer(args)
	}}
	s := svc(r, nil)
	for _, act := range []func() Result{
		func() Result { return s.Start(context.Background(), "app", "dev") },
		func() Result { return s.Stop(context.Background(), "app", "dev") },
		func() Result { return s.Restart(context.Background(), "app", "dev") },
	} {
		res := act()
		if res.Outcome != OutcomeUnavailable {
			t.Errorf("got %+v", res)
		}
	}
	for _, verb := range []string{"new-window", "new-session", "kill-window", "respawn-window"} {
		if len(r.called(verb)) != 0 {
			t.Errorf("%s ran on an unreadable session", verb)
		}
	}
}

func TestRemoteProjectUsesRemoteRunnerOnly(t *testing.T) {
	local := &scriptRunner{}
	sim := newSim()
	remote := sim.runner()
	res := svc(local, remote).Start(context.Background(), "mini/app", "dev")
	if res.Outcome != OutcomeStarted {
		t.Fatalf("got %+v", res)
	}
	if len(local.calls) != 0 {
		t.Errorf("a remote project must never touch the local runner: %v", local.calls)
	}
}

func TestUnreachableHostLaunchesNothing(t *testing.T) {
	local := &scriptRunner{}
	remote := &scriptRunner{answer: func([]string) (string, error) {
		return "", fmt.Errorf("%w: timeout", sources.ErrHostUnreachable)
	}}
	res := svc(local, remote).Start(context.Background(), "mini/app", "dev")
	if res.Outcome != OutcomeUnavailable {
		t.Errorf("got %+v", res)
	}
	if len(local.calls) != 0 || len(remote.called("new-window")) != 0 {
		t.Errorf("nothing may launch anywhere: local %v remote %v", local.calls, remote.calls)
	}
}

func TestAdoptMarksWithoutLaunching(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"1|dev|0|200|0||@1|%1|1||500|600"}
	r := sim.runner()
	res := svc(r, nil).Adopt(context.Background(), "app", "dev", "@1")
	if res.Outcome != OutcomeAdopted {
		t.Fatalf("got %+v", res)
	}
	if got := r.called("set-window-option"); len(got) != 1 || !slices.Contains(got[0], "@cockpit_managed") {
		t.Errorf("adoption must only set the mark: %v", got)
	}
	for _, verb := range []string{"new-window", "respawn-window", "kill-window"} {
		if len(r.called(verb)) != 0 {
			t.Errorf("adoption ran %s", verb)
		}
	}
	stale := svc(sim.runner(), nil).Adopt(context.Background(), "app", "dev", "@7")
	if stale.Outcome != OutcomeBlocked {
		t.Errorf("a stale reviewed id must be refused: %+v", stale)
	}
}

func TestBusyLockIsUnavailableNotALaunch(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.lock = "someone-else"
	r := sim.runner()
	s := svc(r, nil)
	clock := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { clock = clock.Add(3 * time.Second); return clock }
	res := s.Start(context.Background(), "app", "dev")
	if res.Outcome != OutcomeUnavailable || len(r.called("new-window")) != 0 {
		t.Errorf("got %+v calls %v", res, r.calls)
	}
}

func TestReadOutputStripsControlSequences(t *testing.T) {
	sim := newSim()
	sim.exists = true
	sim.windows = []string{"1|dev|0|200|0||@1|%1|1|dev|500|600"}
	r := &scriptRunner{answer: func(args []string) (string, error) {
		if args[0] == "capture-pane" {
			if !slices.Contains(args, "%1") {
				t.Errorf("capture must address the pane id: %v", args)
			}
			return "\x1b[31mred\x1b[0m line\n\x1b]0;title\x07next\n", nil
		}
		return sim.answer(args)
	}}
	out, err := svc(r, nil).ReadOutput(context.Background(), "app", "dev", 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "red line\nnext\n" || out.Lines != DefaultOutputLines {
		t.Errorf("got %+v", out)
	}
	if _, err := svc(sim.runner(), nil).ReadOutput(context.Background(), "app", "worker", 10); err == nil {
		t.Error("an absent window must be an error, not empty output")
	}
}
