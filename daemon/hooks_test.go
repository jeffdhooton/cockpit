package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/sources"
)

func TestEventTablesMapBothEngines(t *testing.T) {
	cases := []struct {
		engine, event string
		want          sources.AgentStatus
	}{
		{"claude", "UserPromptSubmit", sources.AgentStatusWorking},
		{"claude", "PreToolUse", sources.AgentStatusWorking},
		{"claude", "Notification", sources.AgentStatusNeedsInput},
		{"claude", "Stop", sources.AgentStatusIdle},
		{"codex", "UserPromptSubmit", sources.AgentStatusWorking},
		{"codex", "PreToolUse", sources.AgentStatusWorking},
		{"codex", "PermissionRequest", sources.AgentStatusNeedsInput},
		{"codex", "Stop", sources.AgentStatusIdle},
	}
	for _, c := range cases {
		got, ok := stateFor(c.engine, c.event)
		if !ok || got != c.want {
			t.Errorf("%s/%s = %v (ok=%v), want %v", c.engine, c.event, got, ok, c.want)
		}
	}
}

func TestUnknownEventInventsNoState(t *testing.T) {
	if _, ok := stateFor("claude", "PermissionRequest"); ok {
		t.Error("a Codex event name must not resolve under Claude's table")
	}
	if _, ok := stateFor("codex", "Notification"); ok {
		t.Error("a Claude event name must not resolve under Codex's table")
	}
	if _, ok := stateFor("claude", "Banana"); ok {
		t.Error("an unrecognised event must set no state")
	}
	if _, ok := stateFor("nonsuch", "Stop"); ok {
		t.Error("an unknown engine must set no state")
	}
}

// statusRunner records the tmux argv the endpoint produces.
type statusRunner struct{ calls [][]string }

func (r *statusRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func statusServer(t *testing.T) (*Server, *statusRunner) {
	t.Helper()
	r := &statusRunner{}
	s := NewServer(&stubTools{}, "1")
	s.StatusKey = []byte("0123456789abcdef0123456789abcdef")
	s.Runner = r
	return s, r
}

func postStatus(t *testing.T, s *Server, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/status", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("x-cockpit-status-token", token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestStatusEndpointWritesTheReportedState(t *testing.T) {
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token,
		`{"engine":"codex","hook_event_name":"PermissionRequest","target":"app:dev"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if len(r.calls) != 1 {
		t.Fatalf("want one tmux call, got %v", r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "needs_input") || !strings.Contains(joined, "app") {
		t.Errorf("argv did not record the state: %v", r.calls[0])
	}
	if !strings.Contains(joined, "dev") {
		t.Errorf("argv did not record the reporting window: %v", r.calls[0])
	}
}

func TestStatusEndpointRejectsABadToken(t *testing.T) {
	s, r := statusServer(t)

	rec := postStatus(t, s, "deadbeef",
		`{"engine":"claude","hook_event_name":"Stop","target":"app:dev"}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if len(r.calls) != 0 {
		t.Errorf("tmux was written anyway: %v", r.calls)
	}
}

func TestStatusEndpointRejectsATokenForAnotherTarget(t *testing.T) {
	// A token authorises exactly one target. Otherwise any hook could write
	// any session's status.
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "other:dev")

	rec := postStatus(t, s, token,
		`{"engine":"claude","hook_event_name":"Stop","target":"app:dev"}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if len(r.calls) != 0 {
		t.Errorf("tmux was written anyway: %v", r.calls)
	}
}

func TestStatusEndpointIgnoresAnUnknownEvent(t *testing.T) {
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token,
		`{"engine":"claude","hook_event_name":"PreCompact","target":"app:dev"}`)

	if rec.Code != http.StatusNoContent {
		t.Errorf("an ignorable event is not an error, status = %d", rec.Code)
	}
	if len(r.calls) != 0 {
		t.Errorf("no state should have been written: %v", r.calls)
	}
}

func TestStatusEndpointDropsUnknownFields(t *testing.T) {
	// Claude's Stop event can carry an entire assistant message, and none of
	// it belongs in a tmux option.
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token, `{"engine":"claude","hook_event_name":"Stop",`+
		`"target":"app:dev","transcript":"`+strings.Repeat("x", 5000)+`"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(strings.Join(r.calls[0], " "), "xxxx") {
		t.Error("an unknown field reached the tmux option")
	}
}

func TestStatusEndpointRefusesAnOversizedBody(t *testing.T) {
	s, _ := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token, `{"engine":"claude","hook_event_name":"Stop",`+
		`"target":"app:dev","pad":"`+strings.Repeat("x", maxStatusBytes)+`"}`)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want the body refused", rec.Code)
	}
}

func TestStatusEndpointRefusesABrowser(t *testing.T) {
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	req := httptest.NewRequest(http.MethodPost, "/hooks/status",
		strings.NewReader(`{"engine":"claude","hook_event_name":"Stop","target":"app:dev"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-cockpit-status-token", token)
	req.Header.Set("origin", "https://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a write route must not skip the guard, status = %d", rec.Code)
	}
	if len(r.calls) != 0 {
		t.Errorf("tmux was written from a browser request: %v", r.calls)
	}
}

func TestStatusEndpointRequiresATarget(t *testing.T) {
	s, _ := statusServer(t)
	token := StatusToken(s.StatusKey, "")

	rec := postStatus(t, s, token, `{"engine":"claude","hook_event_name":"Stop"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
