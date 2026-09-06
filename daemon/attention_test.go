package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

func TestStatusEndpointBindsAPaneReportToTheLivePane(t *testing.T) {
	s, r := statusServer(t)
	r.outputs = map[string]string{
		"list-panes": "$1|@2|%3|10|0|1|claude|/w|||||100|200|agent\n",
	}
	target := sources.PaneStatusTarget("100-200", "$1", "@2", "%3")
	token := StatusToken(s.StatusKey, target)

	rec := postStatus(t, s, token,
		`{"engine":"codex","hook_event_name":"PermissionRequest","target":"`+target+`","invocation":"sess-abc","seq":50}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var write []string
	for _, c := range r.calls {
		if c[0] == "set-option" {
			write = c
		}
	}
	joined := strings.Join(write, " ")
	for _, want := range []string{"-p -t %3 @cockpit_pane_status needs_input", "@cockpit_pane_invocation sess-abc", "@cockpit_pane_seq 50", "-t $1 @cockpit_status needs_input", "@cockpit_status_window agent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("write lacks %q: %s", want, joined)
		}
	}
}

func TestStatusEndpointRefusesAReplacedOrMissingPane(t *testing.T) {
	s, r := statusServer(t)
	target := sources.PaneStatusTarget("100-200", "$1", "@2", "%3")
	token := StatusToken(s.StatusKey, target)
	body := `{"engine":"claude","hook_event_name":"Stop","target":"` + target + `","invocation":"a","seq":1}`

	// Same pane id, newer server generation: a different pane.
	r.outputs = map[string]string{"list-panes": "$1|@2|%3|10|0|1|claude|/w|||||900|901|agent\n"}
	if rec := postStatus(t, s, token, body); rec.Code != http.StatusConflict {
		t.Errorf("replaced pane: status = %d", rec.Code)
	}
	// Gone entirely.
	r.outputs = map[string]string{"list-panes": ""}
	if rec := postStatus(t, s, token, body); rec.Code != http.StatusConflict {
		t.Errorf("missing pane: status = %d", rec.Code)
	}
	for _, c := range r.calls {
		if c[0] == "set-option" {
			t.Errorf("a refused report must write nothing: %v", c)
		}
	}
}

func TestStatusEndpointDropsALateReport(t *testing.T) {
	s, r := statusServer(t)
	// The pane already holds seq 90 from invocation "new".
	r.outputs = map[string]string{"list-panes": "$1|@2|%3|10|0|1|claude|/w|working|1700000000|new|90|100|200|agent\n"}
	target := sources.PaneStatusTarget("100-200", "$1", "@2", "%3")
	token := StatusToken(s.StatusKey, target)

	rec := postStatus(t, s, token,
		`{"engine":"claude","hook_event_name":"Notification","target":"`+target+`","invocation":"old","seq":40}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, c := range r.calls {
		if c[0] == "set-option" {
			t.Errorf("a late report from an earlier run must not recolour its replacement: %v", c)
		}
	}
}

func TestStatusEndpointStillAcceptsLegacyTargets(t *testing.T) {
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")
	rec := postStatus(t, s, token, `{"engine":"claude","hook_event_name":"Stop","target":"app:dev"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(r.calls) != 1 || r.calls[0][0] != "set-option" || !strings.Contains(strings.Join(r.calls[0], " "), "-t app @cockpit_status idle") {
		t.Errorf("legacy write = %v", r.calls)
	}
}

func TestAttentionToolReturnsRecordsAndCoverage(t *testing.T) {
	f := &fakeRunner{
		outputs: map[string]string{
			"list-sessions": "app|2|0|1700000000|||||$1|100|200\n",
			"list-panes":    "$1|@1|%1|10|0|1|claude|/w|needs_input|1700000000|inv|5|100|200|agent\n",
			"list-windows":  "1|dev|1|222|0|1|@1|%1|1|dev|100|200\n",
		},
	}
	tools := testTools(t, f, devApp(config.ProcessConfig{Name: "dev", Command: "x"}))
	tools.Cfg.Hosts = []config.HostConfig{{Name: "mini", Tmux: "/opt/homebrew/bin/tmux"}}
	tools.Svc.Remote = func(config.HostConfig) sources.Runner {
		return &fakeRunner{errs: map[string]error{"list-sessions": errors.New("host unreachable: ssh: connect timed out")}}
	}
	tools.Now = func() time.Time { return time.Unix(1_700_000_100, 0) }

	got, err := tools.Call(context.Background(), "cockpit_attention", nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		SchemaVersion int                     `json:"schema_version"`
		Actionable    int                     `json:"actionable"`
		Unavailable   int                     `json:"unavailable"`
		Items         []sources.AttentionItem `json:"items"`
		Coverage      []sources.Coverage      `json:"coverage"`
	}
	decodeInto(t, got, &payload)
	if payload.SchemaVersion != 1 {
		t.Errorf("schema_version = %d", payload.SchemaVersion)
	}
	if payload.Actionable != 2 || len(payload.Items) != 2 {
		t.Fatalf("items = %+v", payload.Items)
	}
	if payload.Items[0].Kind != sources.AttentionNeedsInput || payload.Items[0].Target.PaneID != "%1" {
		t.Errorf("first item = %+v", payload.Items[0])
	}
	if payload.Items[1].Kind != sources.AttentionProcessExited || payload.Items[1].Target.WindowID != "@1" {
		t.Errorf("second item = %+v", payload.Items[1])
	}
	if payload.Unavailable != 1 {
		t.Errorf("the unreachable host must be counted as unavailable coverage: %+v", payload.Coverage)
	}
	for _, verb := range []string{"new-window", "respawn-window", "kill-window", "new-session", "switch-client"} {
		if len(f.called(verb)) != 0 {
			t.Errorf("attention must be read-only, ran %s", verb)
		}
	}

	// The legacy envelope adapts the same facts.
	sig, err := tools.Call(context.Background(), "cockpit_signals", nil)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Signals []sources.Signal `json:"signals"`
	}
	decodeInto(t, sig, &legacy)
	if len(legacy.Signals) != 2 || legacy.Signals[0].Kind != sources.SignalBlockedAgent || legacy.Signals[1].Subject != "app/dev" {
		t.Errorf("signals = %+v", legacy.Signals)
	}
}
