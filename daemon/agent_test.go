package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jhoot/cockpit/config"
)

// changingRunner returns each scripted output in turn, then repeats the last —
// an agent that boots, prints, and goes quiet.
type changingRunner struct {
	outputs []string
	calls   int
}

func (c *changingRunner) Run(_ context.Context, _ ...string) (string, error) {
	i := c.calls
	c.calls++
	if i >= len(c.outputs) {
		i = len(c.outputs) - 1
	}
	return c.outputs[i], nil
}

// alwaysChangingRunner never settles.
type alwaysChangingRunner struct{ n int }

func (a *alwaysChangingRunner) Run(_ context.Context, _ ...string) (string, error) {
	a.n++
	return fmt.Sprintf("output %d", a.n), nil
}

func TestWaitForSettleReturnsWhenOutputStops(t *testing.T) {
	r := &changingRunner{outputs: []string{"", "boot", "boot ready", "boot ready", "boot ready", "boot ready"}}

	ok := waitForSettle(context.Background(), r, "s:w", settleOptions{
		Poll:     time.Millisecond,
		Quiet:    3 * time.Millisecond,
		Deadline: time.Second,
		HeadStart: time.Millisecond,
	})

	if !ok {
		t.Error("output that stops changing should count as settled")
	}
}

func TestWaitForSettleGivesUpAtDeadline(t *testing.T) {
	r := &alwaysChangingRunner{}
	start := time.Now()

	ok := waitForSettle(context.Background(), r, "s:w", settleOptions{
		Poll:      time.Millisecond,
		Quiet:     time.Second,
		Deadline:  30 * time.Millisecond,
		HeadStart: time.Millisecond,
	})

	if ok {
		t.Error("constantly-changing output must not report settled")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waited %s — the deadline must be respected", elapsed)
	}
}

func TestWaitForSettleIgnoresEmptyOutput(t *testing.T) {
	r := &changingRunner{outputs: []string{"", "", "", ""}}

	ok := waitForSettle(context.Background(), r, "s:w", settleOptions{
		Poll:      time.Millisecond,
		Quiet:     2 * time.Millisecond,
		Deadline:  20 * time.Millisecond,
		HeadStart: time.Millisecond,
	})

	if ok {
		t.Error("a pane that never prints anything has not booted, it is just empty")
	}
}

func TestStartProcessIsANoOpWhenRunning(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t222\t0\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_start",
		map[string]any{"project": "app", "process": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.called("new-window")) != 0 {
		t.Errorf("a running process must not be launched twice: %v", f.calls)
	}

	var payload struct {
		Status string `json:"status"`
	}
	decodeInto(t, got, &payload)
	if payload.Status != "already running" {
		t.Errorf("status = %q", payload.Status)
	}
}

func TestStartProcessRespawnsADeadWindow(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t1\t222\t0\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	if _, err := tools.Call(context.Background(), "cockpit_start",
		map[string]any{"project": "app", "process": "dev"}); err != nil {
		t.Fatal(err)
	}
	if len(f.called("respawn-window")) != 1 {
		t.Errorf("a dead window should be reused, not duplicated: %v", f.calls)
	}
}

func TestStartProcessCreatesAMissingWindow(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "0\tshell\t0\t111\t1\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	if _, err := tools.Call(context.Background(), "cockpit_start",
		map[string]any{"project": "app", "process": "dev"}); err != nil {
		t.Fatal(err)
	}
	if len(f.called("new-window")) != 1 {
		t.Errorf("want a new-window call: %v", f.calls)
	}
}

func TestStopAndRestartTools(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t222\t0\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	if _, err := tools.Call(context.Background(), "cockpit_stop",
		map[string]any{"project": "app", "process": "dev"}); err != nil {
		t.Fatal(err)
	}
	if len(f.called("kill-window")) != 1 {
		t.Errorf("stop should kill the window: %v", f.calls)
	}

	g := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t222\t0\n"}}
	tools2 := testTools(t, g, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
	if _, err := tools2.Call(context.Background(), "cockpit_restart",
		map[string]any{"project": "app", "process": "dev"}); err != nil {
		t.Fatal(err)
	}
	if len(g.called("respawn-window")) != 1 {
		t.Errorf("restart should respawn the window: %v", g.calls)
	}
}

func TestWriteInputSendsLiteralThenEnter(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t222\t0\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	if _, err := tools.Call(context.Background(), "cockpit_write_input",
		map[string]any{"project": "app", "process": "dev", "input": "hello"}); err != nil {
		t.Fatal(err)
	}

	calls := f.called("send-keys")
	if len(calls) != 2 {
		t.Fatalf("want a literal write then Enter, got %v", calls)
	}
	if !slices.Contains(calls[0], "-l") || !slices.Contains(calls[0], "hello") {
		t.Errorf("first call = %v", calls[0])
	}
	if !slices.Contains(calls[1], "Enter") {
		t.Errorf("second call = %v", calls[1])
	}
}

func TestWriteInputSubmitFalseSkipsEnter(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t222\t0\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	if _, err := tools.Call(context.Background(), "cockpit_write_input",
		map[string]any{"project": "app", "process": "dev", "input": "partial", "submit": false}); err != nil {
		t.Fatal(err)
	}
	if len(f.called("send-keys")) != 1 {
		t.Errorf("submit = false must not press Enter: %v", f.calls)
	}
}

func TestWriteInputRequiresText(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t222\t0\n"}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	if _, err := tools.Call(context.Background(), "cockpit_write_input",
		map[string]any{"project": "app", "process": "dev"}); err == nil {
		t.Fatal("writing nothing is a mistake worth reporting")
	}
}

func TestSpawnAgentCreatesAWindow(t *testing.T) {
	f := &fakeRunner{}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_spawn_agent",
		map[string]any{"command": "claude", "name": "helper", "project": "app"})
	if err != nil {
		t.Fatal(err)
	}

	created := f.called("new-window")
	if len(created) != 1 {
		t.Fatalf("want one new-window call, got %v", f.calls)
	}
	if !slices.Contains(created[0], "helper") || !slices.Contains(created[0], "claude") {
		t.Errorf("window argv = %v", created[0])
	}

	var payload struct {
		Project string `json:"project"`
		Process string `json:"process"`
		Target  string `json:"target"`
	}
	decodeInto(t, got, &payload)
	if payload.Process != "helper" || payload.Target != "app:helper" {
		t.Errorf("got %+v", payload)
	}
}

func TestSpawnAgentGeneratesAName(t *testing.T) {
	f := &fakeRunner{}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_spawn_agent",
		map[string]any{"command": "claude", "project": "app"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Process string `json:"process"`
	}
	decodeInto(t, got, &payload)
	if !strings.HasPrefix(payload.Process, "agent-") {
		t.Errorf("generated name = %q", payload.Process)
	}
}

func TestSpawnAgentSanitisesASuppliedName(t *testing.T) {
	f := &fakeRunner{}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_spawn_agent",
		map[string]any{"command": "claude", "name": "my agent; rm -rf /", "project": "app"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Process string `json:"process"`
	}
	decodeInto(t, got, &payload)
	if strings.ContainsAny(payload.Process, " ;/") {
		t.Errorf("a window name must never carry shell punctuation: %q", payload.Process)
	}
}

func TestSpawnAgentDefaultsToTheFirstProject(t *testing.T) {
	f := &fakeRunner{}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_spawn_agent", map[string]any{"command": "claude"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Project string `json:"project"`
	}
	decodeInto(t, got, &payload)
	if payload.Project != "app" {
		t.Errorf("project = %q", payload.Project)
	}
}

func TestSpawnAgentRequiresACommand(t *testing.T) {
	tools := testTools(t, &fakeRunner{}, devApp())
	if _, err := tools.Call(context.Background(), "cockpit_spawn_agent", map[string]any{}); err == nil {
		t.Fatal("spawning nothing must error")
	}
}

func TestSpawnAgentWithoutAPromptDoesNotWait(t *testing.T) {
	f := &fakeRunner{}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_spawn_agent",
		map[string]any{"command": "claude", "project": "app"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		PromptDelivery string `json:"prompt_delivery"`
	}
	decodeInto(t, got, &payload)
	if payload.PromptDelivery != "" {
		t.Errorf("no prompt means no delivery to report, got %q", payload.PromptDelivery)
	}
}

func TestCaptureLandsWhereTheCommandLinePutsIt(t *testing.T) {
	// `cockpit cap` and the TUI's c key both append to today_file, so the tool
	// must too — a capture that lands somewhere else is a capture you lose.
	today := filepath.Join(t.TempDir(), "today.md")
	tools := testTools(t, &fakeRunner{})
	tools.Cfg.Obsidian.TodayFile = today
	tools.Cfg.Obsidian.InboxFile = filepath.Join(t.TempDir(), "inbox.md")

	if _, err := tools.Call(context.Background(), "cockpit_capture",
		map[string]any{"text": "fix the auth bug"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(today)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fix the auth bug") {
		t.Errorf("today = %q", data)
	}
	if _, err := os.Stat(tools.Cfg.Obsidian.InboxFile); err == nil {
		t.Error("the capture should not have touched inbox_file")
	}
}

func TestCaptureRequiresText(t *testing.T) {
	tools := testTools(t, &fakeRunner{})
	tools.Cfg.Obsidian.TodayFile = filepath.Join(t.TempDir(), "today.md")

	if _, err := tools.Call(context.Background(), "cockpit_capture", map[string]any{}); err == nil {
		t.Fatal("capturing nothing must error")
	}
}

func TestTasksReadsAndToggles(t *testing.T) {
	today := filepath.Join(t.TempDir(), "today.md")
	if err := os.WriteFile(today, []byte("- [ ] write the plan\n- [x] read the spec\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tools := testTools(t, &fakeRunner{})
	tools.Cfg.Obsidian.TodayFile = today

	got, err := tools.Call(context.Background(), "cockpit_tasks", nil)
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Tasks []struct {
			Text string `json:"text"`
			Done bool   `json:"done"`
			Line int    `json:"line"`
		} `json:"tasks"`
	}
	decodeInto(t, got, &payload)
	if len(payload.Tasks) != 2 {
		t.Fatalf("want 2 tasks, got %+v", payload.Tasks)
	}

	if _, err := tools.Call(context.Background(), "cockpit_tasks",
		map[string]any{"toggle_line": float64(1)}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(today)
	if !strings.Contains(string(data), "- [x] write the plan") {
		t.Errorf("toggle did not take: %q", data)
	}
}

func TestTasksRejectsABadLine(t *testing.T) {
	today := filepath.Join(t.TempDir(), "today.md")
	os.WriteFile(today, []byte("- [ ] one\n"), 0644)
	tools := testTools(t, &fakeRunner{})
	tools.Cfg.Obsidian.TodayFile = today

	if _, err := tools.Call(context.Background(), "cockpit_tasks",
		map[string]any{"toggle_line": float64(99)}); err == nil {
		t.Fatal("toggling a line that is not there must error")
	}
}
