package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

func TestWorkspacesToolReturnsTheTreeReadOnly(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-sessions": "app|2|0|1700000000|||||$1|100|200\n",
		"list-panes":    "$1|@1|%1|10|0|1|claude|/w/app|needs_input|1700000000|inv|5|100|200|agent\n$1|@2|%2|11|0|0|zsh|/w/app||||0|100|200|zsh\n",
		"list-windows":  "1|dev|1|222|0|1|@3|%3|1|dev|100|200\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
	tools.Now = func() time.Time { return time.Unix(1_700_000_100, 0) }

	got, err := tools.Call(context.Background(), "cockpit_workspaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		SchemaVersion int                `json:"schema_version"`
		Hosts         []sources.HostView `json:"hosts"`
		Coverage      []sources.Coverage `json:"coverage"`
	}
	decodeInto(t, got, &payload)
	if payload.SchemaVersion != 1 || len(payload.Hosts) != 1 || len(payload.Hosts[0].Sessions) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	s := payload.Hosts[0].Sessions[0]
	if s.Key != "app" || s.Status != "needs_input" || s.Agents != 1 || len(s.Panes) != 2 || s.Panes[0].Kind != sources.PaneAgent || s.Panes[1].Kind != sources.PaneShell {
		t.Errorf("session = %+v", s)
	}
	for _, verb := range []string{"new-window", "respawn-window", "kill-window", "new-session", "switch-client", "send-keys", "set-option"} {
		if len(f.called(verb)) != 0 {
			t.Errorf("workspaces must be read-only, ran %s", verb)
		}
	}

	// Equivalence: the same input derived directly gives the same tree.
	direct := sources.BuildWorkspace(tools.collectInput(context.Background()))
	if len(direct.Hosts[0].Sessions) != 1 || direct.Hosts[0].Sessions[0].Panes[0].PaneID != s.Panes[0].PaneID {
		t.Errorf("direct = %+v", direct)
	}
}

func TestReadOutputAcceptsAPaneID(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-windows": "1|dev|0|222|0||@1|%1|1|dev|100|200\n",
		"capture-pane": "hello\n",
	}}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
	got, err := tools.Call(context.Background(), "cockpit_read_output", map[string]any{"project": "app", "pane_id": "%1"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Output string `json:"output"`
		PaneID string `json:"pane_id"`
	}
	decodeInto(t, got, &payload)
	if payload.Output != "hello" || payload.PaneID != "%1" {
		t.Errorf("payload = %+v", payload)
	}
	calls := f.called("capture-pane")
	if len(calls) != 1 || !slices.Contains(calls[0], "%1") {
		t.Errorf("capture must address the pane id: %v", calls)
	}
	if _, err := tools.Call(context.Background(), "cockpit_read_output", map[string]any{"project": "app", "pane_id": "%9"}); err == nil {
		t.Error("a pane that is not in the project must be refused")
	}
}
