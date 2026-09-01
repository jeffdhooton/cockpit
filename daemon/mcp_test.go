package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubTools is a minimal ToolHandler that records what it was asked to do.
type stubTools struct {
	lastName string
	lastArgs map[string]any
	err      error
}

func (s *stubTools) Definitions() []ToolDefinition {
	return []ToolDefinition{{
		Name:        "cockpit_whoami",
		Description: "identify this cockpit",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}}
}

func (s *stubTools) Call(_ context.Context, name string, args map[string]any) (any, error) {
	s.lastName, s.lastArgs = name, args
	if s.err != nil {
		return nil, s.err
	}
	return map[string]any{"ok": true}, nil
}

func post(t *testing.T, h http.Handler, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()

	var decoded map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decoded)
	return res, decoded
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	h := NewServer(&stubTools{}, "1.2.3").Handler()

	for _, version := range []string{"2025-11-25", "2025-06-18", "2025-03-26"} {
		_, got := post(t, h, "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+version+`"}}`)
		result, ok := got["result"].(map[string]any)
		if !ok {
			t.Fatalf("no result for %s: %v", version, got)
		}
		if result["protocolVersion"] != version {
			t.Errorf("version = %v, want %s", result["protocolVersion"], version)
		}
	}
}

func TestInitializeReportsServerIdentity(t *testing.T) {
	h := NewServer(&stubTools{}, "1.2.3").Handler()
	_, got := post(t, h, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	result := got["result"].(map[string]any)
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "cockpit" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", info)
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("must advertise tool capability: %v", result["capabilities"])
	}
}

func TestInitializeFallsBackForUnknownVersion(t *testing.T) {
	h := NewServer(&stubTools{}, "1.2.3").Handler()
	_, got := post(t, h, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)

	result := got["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("an unknown version should fall back to the baseline, got %v", result["protocolVersion"])
	}
}

func TestRejectsWrongJSONRPCVersion(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	_, got := post(t, h, "/mcp", `{"jsonrpc":"1.0","id":1,"method":"initialize"}`)

	rpcErr, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("want an error, got %v", got)
	}
	if rpcErr["code"] != float64(-32600) {
		t.Errorf("code = %v, want -32600", rpcErr["code"])
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	_, got := post(t, h, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"nope/nope"}`)

	rpcErr, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("want an error, got %v", got)
	}
	if rpcErr["code"] != float64(-32601) {
		t.Errorf("code = %v, want -32601", rpcErr["code"])
	}
}

func TestNotificationIsAccepted(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Errorf("a notification takes no reply body, got %q", body)
	}
}

func TestToolsListReturnsDefinitions(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	_, got := post(t, h, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	tools := got["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(tools))
	}
	if tools[0].(map[string]any)["name"] != "cockpit_whoami" {
		t.Errorf("got %v", tools[0])
	}
}

func TestToolsCallForwardsNameAndArguments(t *testing.T) {
	stub := &stubTools{}
	h := NewServer(stub, "1").Handler()

	post(t, h, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{"project":"app"}}}`)

	if stub.lastName != "cockpit_whoami" {
		t.Errorf("name = %q", stub.lastName)
	}
	if stub.lastArgs["project"] != "app" {
		t.Errorf("args = %v", stub.lastArgs)
	}
}

func TestToolResultIsTextContent(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	_, got := post(t, h, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{}}}`)

	result := got["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" {
		t.Errorf("type = %v", content["type"])
	}
	if !strings.Contains(content["text"].(string), `"ok"`) {
		t.Errorf("text = %v", content["text"])
	}
}

func TestToolErrorIsSuccessEnvelopeWithIsError(t *testing.T) {
	h := NewServer(&stubTools{err: errors.New("boom")}, "1").Handler()
	_, got := post(t, h, "/mcp", `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{}}}`)

	if _, isErr := got["error"]; isErr {
		t.Fatal("a tool failure is a result the caller can read, not a protocol error")
	}
	result := got["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("want isError true, got %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "boom") {
		t.Errorf("error text = %q", text)
	}
}

func TestSessionHeaderIsPresent(t *testing.T) {
	s := NewServer(&stubTools{}, "1")
	res, _ := post(t, s.Handler(), "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if got := res.Header.Get("mcp-session-id"); got != s.SessionID {
		t.Errorf("session header = %q, want %q", got, s.SessionID)
	}
	if !strings.HasPrefix(s.SessionID, "cockpit-") {
		t.Errorf("session id = %q", s.SessionID)
	}
}

func TestBothRoutesAreServed(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	for _, path := range []string{"/", "/mcp"} {
		_, got := post(t, h, path, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if _, ok := got["result"]; !ok {
			t.Errorf("path %s did not answer: %v", path, got)
		}
	}
}

func TestGetIsNotAllowedAndDeleteIsAccepted(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", get.Code)
	}

	del := httptest.NewRecorder()
	h.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/mcp", nil))
	if del.Code != http.StatusAccepted {
		t.Errorf("DELETE status = %d, want 202", del.Code)
	}
}

func TestBrowserOriginIsRefused(t *testing.T) {
	// Loopback binding does not keep a web page out. Any page the user visits
	// can post here; only the reply is hidden from it, and the effect — a
	// send-keys into a live pane — would already have happened.
	stub := &stubTools{}
	h := NewServer(stub, "1").Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{}}}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if stub.lastName != "" {
		t.Errorf("the tool ran anyway: %q", stub.lastName)
	}
}

func TestLoopbackOriginIsAlsoRefused(t *testing.T) {
	// A page served from localhost is still a page. MCP clients send no Origin
	// at all, so its presence is the signal regardless of what it says.
	h := NewServer(&stubTools{}, "1").Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "http://127.0.0.1:3000")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestNonJSONContentTypeIsRefused(t *testing.T) {
	// text/plain is a CORS-simple content type: a browser sends it with no
	// preflight. Requiring JSON is what forces the preflight that fails.
	stub := &stubTools{}
	h := NewServer(stub, "1").Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{}}}`))
	req.Header.Set("content-type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
	if stub.lastName != "" {
		t.Errorf("the tool ran anyway: %q", stub.lastName)
	}
}

func TestContentTypeParametersAreAccepted(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("content-type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("a charset parameter is normal and must not be refused, status = %d", rec.Code)
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	huge := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` +
		strings.Repeat("x", maxRequestBytes) + `"}}`

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(huge))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestMalformedBodyIsParseError(t *testing.T) {
	h := NewServer(&stubTools{}, "1").Handler()
	_, got := post(t, h, "/mcp", `{not json`)

	rpcErr, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("want an error, got %v", got)
	}
	if rpcErr["code"] != float64(-32700) {
		t.Errorf("code = %v, want -32700", rpcErr["code"])
	}
}
