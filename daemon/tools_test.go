package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
)

// fakeRunner records every tmux call and returns scripted output keyed by the
// tmux verb. It mirrors the one in the sources package — Go test helpers do
// not cross package boundaries.
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

func (f *fakeRunner) called(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

func testTools(t *testing.T, r sources.Runner, repos ...config.RepoConfig) *Tools {
	t.Helper()
	cfg := &config.Config{Repos: repos}
	cfg.General.SessionName = "cockpit"
	return NewTools(cfg, "/tmp/config.toml", r, "1.0.0", 45679)
}

func devApp(procs ...config.ProcessConfig) config.RepoConfig {
	return config.RepoConfig{Label: "app", Path: "/r", Processes: procs}
}

// decodeInto marshals a tool result and decodes it into v, which is how a
// caller actually receives it over the wire.
func decodeInto(t *testing.T, result any, v any) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func TestDefinitionsAreWellFormed(t *testing.T) {
	defs := testTools(t, &fakeRunner{}).Definitions()

	if len(defs) != 14 {
		t.Errorf("want 14 tools, got %d", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if !strings.HasPrefix(d.Name, "cockpit_") {
			t.Errorf("tool %q must be namespaced", d.Name)
		}
		if seen[d.Name] {
			t.Errorf("duplicate tool %q", d.Name)
		}
		seen[d.Name] = true
		if d.Description == "" {
			t.Errorf("tool %q has no description", d.Name)
		}
		if d.InputSchema["type"] != "object" {
			t.Errorf("tool %q schema type = %v", d.Name, d.InputSchema["type"])
		}
		if _, ok := d.InputSchema["properties"]; !ok {
			t.Errorf("tool %q schema has no properties", d.Name)
		}
	}
}

func TestDefinitionsCoverHelmParity(t *testing.T) {
	defs := testTools(t, &fakeRunner{}).Definitions()
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}

	for _, want := range []string{
		"cockpit_list_projects", "cockpit_list_processes", "cockpit_read_output",
		"cockpit_start", "cockpit_stop", "cockpit_restart",
		"cockpit_signals", "cockpit_git_status", "cockpit_spawn_agent",
		"cockpit_write_input", "cockpit_whoami", "cockpit_status",
		"cockpit_capture", "cockpit_tasks",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %q", want)
		}
	}
}

func TestUnknownToolIsAnError(t *testing.T) {
	if _, err := testTools(t, &fakeRunner{}).Call(context.Background(), "cockpit_nope", nil); err == nil {
		t.Fatal("an unknown tool must error rather than silently succeed")
	}
}

func TestUnknownProjectIsAnError(t *testing.T) {
	tools := testTools(t, &fakeRunner{})
	_, err := tools.Call(context.Background(), "cockpit_list_processes", map[string]any{"project": "ghost"})
	if err == nil {
		t.Fatal("an unknown project must error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the error should name the project: %v", err)
	}
}

func TestListProcessesReportsState(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n",
	}}
	tools := testTools(t, f, devApp(
		config.ProcessConfig{Name: "dev", Command: "npm run dev"},
		config.ProcessConfig{Name: "test", Command: "npm test"},
	))

	got, err := tools.Call(context.Background(), "cockpit_list_processes", map[string]any{"project": "app"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Project   string                `json:"project"`
		Processes []sources.ProcessInfo `json:"processes"`
	}
	decodeInto(t, got, &payload)

	byName := map[string]sources.ProcessInfo{}
	for _, p := range payload.Processes {
		byName[p.Name] = p
	}
	if byName["dev"].State != sources.ProcessDead {
		t.Errorf("dev should be dead, got %v", byName["dev"].State)
	}
	if byName["test"].State != sources.ProcessNotStarted {
		t.Errorf("test should be not_started, got %v", byName["test"].State)
	}
}

func TestListProjectsSummarises(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-sessions": "app\t2\t1\t1700000000\n",
		"list-windows":  "0\tshell\t0\t111\t1\n1\tdev\t0\t222\t0\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "npm run dev"}))

	got, err := tools.Call(context.Background(), "cockpit_list_projects", nil)
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Projects []struct {
			Label            string `json:"label"`
			SessionRunning   bool   `json:"session_running"`
			ProcessesRunning int    `json:"processes_running"`
			ProcessesTotal   int    `json:"processes_total"`
		} `json:"projects"`
	}
	decodeInto(t, got, &payload)

	if len(payload.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(payload.Projects))
	}
	p := payload.Projects[0]
	if p.Label != "app" || !p.SessionRunning {
		t.Errorf("got %+v", p)
	}
	if p.ProcessesRunning != 1 || p.ProcessesTotal != 1 {
		t.Errorf("process counts = %d/%d", p.ProcessesRunning, p.ProcessesTotal)
	}
}

func TestReadOutputRequestsRequestedLines(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"capture-pane": "line one\nline two\n",
		"list-windows": "1\tdev\t0\t222\t0\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "dev", "lines": float64(50)})
	if err != nil {
		t.Fatal(err)
	}

	call := f.called("capture-pane")[0]
	if !slices.Contains(call, "-50") {
		t.Errorf("want a 50-line scrollback request, got %v", call)
	}

	var payload struct {
		Output string `json:"output"`
	}
	decodeInto(t, got, &payload)
	if !strings.Contains(payload.Output, "line two") {
		t.Errorf("output = %q", payload.Output)
	}
}

func TestReadOutputDefaultsAndClampsLines(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"default", map[string]any{"project": "app", "process": "dev"}, "-100"},
		{"clamped", map[string]any{"project": "app", "process": "dev", "lines": float64(999999)}, "-10000"},
		{"floor", map[string]any{"project": "app", "process": "dev", "lines": float64(-5)}, "-100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{outputs: map[string]string{"list-windows": "1\tdev\t0\t2\t0\n"}}
			tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
			if _, err := tools.Call(context.Background(), "cockpit_read_output", tc.args); err != nil {
				t.Fatal(err)
			}
			if call := f.called("capture-pane")[0]; !slices.Contains(call, tc.want) {
				t.Errorf("want %s in %v", tc.want, call)
			}
		})
	}
}

func TestReadOutputAcceptsAnAdHocWindow(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tagent-ab12\t0\t222\t0\n",
		"capture-pane": "hello from the agent\n",
	}}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "agent-ab12"})
	if err != nil {
		t.Fatalf("a spawned agent's window is readable even though config never named it: %v", err)
	}

	var payload struct {
		Output string `json:"output"`
	}
	decodeInto(t, got, &payload)
	if !strings.Contains(payload.Output, "hello from the agent") {
		t.Errorf("output = %q", payload.Output)
	}
}

func TestReadOutputRejectsAWindowThatDoesNotExist(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-windows": "0\tshell\t0\t1\t1\n"}}
	tools := testTools(t, f, devApp())

	if _, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "ghost"}); err == nil {
		t.Fatal("a window that is neither configured nor open must error")
	}
}

func TestWhoamiIdentifiesTheDaemon(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"list-sessions": "app\t1\t0\t1700000000\n"}}
	tools := testTools(t, f, devApp())

	got, err := tools.Call(context.Background(), "cockpit_whoami", nil)
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Version     string   `json:"version"`
		Port        int      `json:"port"`
		ConfigPath  string   `json:"config_path"`
		PID         int      `json:"pid"`
		SessionName string   `json:"session_name"`
		Sessions    []string `json:"sessions"`
	}
	decodeInto(t, got, &payload)

	if payload.Version != "1.0.0" || payload.Port != 45679 {
		t.Errorf("got %+v", payload)
	}
	if payload.ConfigPath != "/tmp/config.toml" {
		t.Errorf("config path = %q", payload.ConfigPath)
	}
	if payload.PID == 0 {
		t.Error("whoami should report the daemon's own process id")
	}
	if !slices.Contains(payload.Sessions, "app") {
		t.Errorf("sessions = %v", payload.Sessions)
	}
}

func TestStatusMatchesConfiguredPatterns(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\n",
		"capture-pane": "booting\nLocal:   http://localhost:5173\nerror: something broke\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{
		Name:    "dev",
		Command: "npm run dev",
		Status: &config.StatusPatterns{
			Ready: `Local:\s+(\S+)`,
			Error: `error`,
		},
	}))

	got, err := tools.Call(context.Background(), "cockpit_status", map[string]any{"project": "app", "process": "dev"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Processes []struct {
			Process string `json:"process"`
			Events  []struct {
				Type   string `json:"type"`
				Line   string `json:"line"`
				Source string `json:"source"`
			} `json:"events"`
		} `json:"processes"`
	}
	decodeInto(t, got, &payload)

	if len(payload.Processes) != 1 {
		t.Fatalf("want one process, got %+v", payload.Processes)
	}
	events := payload.Processes[0].Events
	if len(events) != 2 {
		t.Fatalf("want a ready and an error event, got %+v", events)
	}
	if events[0].Type != "error" {
		t.Errorf("events must be newest first, got %+v", events)
	}
	if events[0].Source != "scrollback" {
		t.Errorf("events must declare they came from scrollback, got %q", events[0].Source)
	}
}

func TestStatusIsEmptyWithoutPatterns(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\n",
		"capture-pane": "lots of output\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_status", map[string]any{"project": "app"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Processes []struct {
			Events []any `json:"events"`
		} `json:"processes"`
	}
	decodeInto(t, got, &payload)
	if len(payload.Processes) != 1 || len(payload.Processes[0].Events) != 0 {
		t.Errorf("a process with no patterns has no events: %+v", payload)
	}
}

func TestStatusRespectsLimit(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\n",
		"capture-pane": "error one\nerror two\nerror three\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{
		Name:    "dev",
		Command: "x",
		Status:  &config.StatusPatterns{Error: `error`},
	}))

	got, err := tools.Call(context.Background(), "cockpit_status",
		map[string]any{"project": "app", "process": "dev", "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Processes []struct {
			Events []any `json:"events"`
		} `json:"processes"`
	}
	decodeInto(t, got, &payload)
	if len(payload.Processes[0].Events) != 2 {
		t.Errorf("limit ignored: %+v", payload.Processes[0].Events)
	}
}

func TestGitStatusRejectsUnknownProject(t *testing.T) {
	tools := testTools(t, &fakeRunner{}, devApp())
	if _, err := tools.Call(context.Background(), "cockpit_git_status",
		map[string]any{"project": "ghost"}); err == nil {
		t.Fatal("an unknown project must error")
	}
}

func TestSignalsIncludesDeadProcesses(t *testing.T) {
	f := &fakeRunner{
		outputs: map[string]string{
			"list-windows":  "1\tdev\t1\t222\t0\n",
			"list-sessions": "",
		},
		errs: map[string]error{"list-sessions": errors.New("no server")},
	}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_signals", nil)
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Signals []sources.Signal `json:"signals"`
	}
	decodeInto(t, got, &payload)

	if len(payload.Signals) != 1 {
		t.Fatalf("want one signal, got %+v", payload.Signals)
	}
	if payload.Signals[0].Subject != "app/dev" {
		t.Errorf("got %+v", payload.Signals[0])
	}
}

func TestCollapseBlankRuns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single blank kept", "a\n\nb", "a\n\nb"},
		{"run collapsed", "a\n\n\n\n\n\nb", "a\n\nb"},
		{"leading blanks dropped", "\n\n\na", "a"},
		{"trailing blanks dropped", "a\n\n\n", "a"},
		{"whitespace counts as blank", "a\n   \n\t\n\nb", "a\n\nb"},
		{"no blanks untouched", "a\nb\nc", "a\nb\nc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collapseBlankRuns(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestReadOutputCollapsesPanePadding(t *testing.T) {
	// tmux pads a pane to its full height, so a one-line crash comes back
	// buried in blank lines the caller has to pay for.
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t1\t222\t0\n",
		"capture-pane": "fatal: on fire\n" + strings.Repeat("\n", 40) + "Pane is dead\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "dev"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Output string `json:"output"`
	}
	decodeInto(t, got, &payload)

	if strings.Contains(payload.Output, "\n\n\n") {
		t.Errorf("pane padding should collapse, got %q", payload.Output)
	}
	for _, want := range []string{"fatal: on fire", "Pane is dead"} {
		if !strings.Contains(payload.Output, want) {
			t.Errorf("collapsing must not drop content: %q missing from %q", want, payload.Output)
		}
	}
}

func TestReadOutputReportsWhatItDropped(t *testing.T) {
	// Forty lines of tmux pane padding get collapsed away. Saying so turns a
	// silently edited transcript into an honest one.
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\t\n",
		"capture-pane": "first\n" + strings.Repeat("\n", 40) + "last\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "dev"})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		LinesReturned     int  `json:"lines_returned"`
		BlankLinesRemoved int  `json:"blank_lines_removed"`
		Truncated         bool `json:"truncated"`
	}
	decodeInto(t, got, &payload)

	if payload.LinesReturned != 3 {
		t.Errorf("lines_returned = %d, want 3 (first, one blank, last)", payload.LinesReturned)
	}
	if payload.BlankLinesRemoved != 39 {
		t.Errorf("blank_lines_removed = %d, want 39", payload.BlankLinesRemoved)
	}
	if payload.Truncated {
		t.Error("42 captured lines is under the 100-line default, so nothing was cut")
	}
}

func TestReadOutputReportsTruncation(t *testing.T) {
	// The capture filled the requested scrollback, so older output exists that
	// the caller did not see.
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\t\n",
		"capture-pane": strings.Repeat("line\n", 10),
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))

	got, err := tools.Call(context.Background(), "cockpit_read_output",
		map[string]any{"project": "app", "process": "dev", "lines": float64(10)})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Truncated bool `json:"truncated"`
	}
	decodeInto(t, got, &payload)
	if !payload.Truncated {
		t.Error("hitting the requested line count means there may be more above")
	}
}

func TestStatusReportsOmittedEvents(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\t\n",
		"capture-pane": "error one\nerror two\nerror three\nerror four\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{
		Name:    "dev",
		Command: "x",
		Status:  &config.StatusPatterns{Error: `error`},
	}))

	got, err := tools.Call(context.Background(), "cockpit_status",
		map[string]any{"project": "app", "process": "dev", "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Processes []struct {
			Events  []any `json:"events"`
			Omitted int   `json:"omitted"`
		} `json:"processes"`
	}
	decodeInto(t, got, &payload)

	if len(payload.Processes[0].Events) != 2 {
		t.Fatalf("want 2 events, got %d", len(payload.Processes[0].Events))
	}
	if payload.Processes[0].Omitted != 2 {
		t.Errorf("omitted = %d, want 2 — the caller should know it saw a window, not the whole story",
			payload.Processes[0].Omitted)
	}
}

func TestStatusOmittedIsZeroWhenEverythingFits(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1\tdev\t0\t222\t0\t\n",
		"capture-pane": "error one\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{
		Name: "dev", Command: "x", Status: &config.StatusPatterns{Error: `error`},
	}))

	got, err := tools.Call(context.Background(), "cockpit_status", map[string]any{"project": "app"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Processes []struct {
			Omitted int `json:"omitted"`
		} `json:"processes"`
	}
	decodeInto(t, got, &payload)
	if payload.Processes[0].Omitted != 0 {
		t.Errorf("omitted = %d, want 0", payload.Processes[0].Omitted)
	}
}
