package cmd

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jhoot/cockpit/daemon"
)

// hookPort returns the port an httptest server is listening on, so the hook
// can be pointed at it the same way it is pointed at the daemon.
func hookPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestHookStatusExitsZeroWithNoDaemon(t *testing.T) {
	// A hook that fails, fails the agent that called it. This is the single
	// most important property in the design.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing is listening here now

	err = runHookStatus(strings.NewReader(`{"hook_event_name":"Stop"}`), "claude", "app:dev", port, t.TempDir())
	if err != nil {
		t.Fatalf("a down daemon must not surface as an error: %v", err)
	}
}

func TestHookStatusExitsZeroOnMalformedInput(t *testing.T) {
	for _, in := range []string{"", "{not json", "null", "[]", `{"hook_event_name":""}`} {
		if err := runHookStatus(strings.NewReader(in), "claude", "app:dev", 1, t.TempDir()); err != nil {
			t.Errorf("input %q produced an error: %v", in, err)
		}
	}
}

func TestHookStatusExitsZeroWithNoTarget(t *testing.T) {
	if err := runHookStatus(bytes.NewReader(nil), "claude", "", 1, t.TempDir()); err != nil {
		t.Errorf("no resolvable target must be a silent no-op: %v", err)
	}
}

func TestHookStatusPostsASignedEvent(t *testing.T) {
	keyDir := t.TempDir()
	key, err := daemon.LoadOrCreateStatusKey(keyDir)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		token string
		body  map[string]string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.token = r.Header.Get("x-cockpit-status-token")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err = runHookStatus(strings.NewReader(`{"hook_event_name":"PermissionRequest","transcript":"secret"}`),
		"codex", "app:dev", hookPort(t, srv), keyDir)
	if err != nil {
		t.Fatal(err)
	}

	if got.token != daemon.StatusToken(key, "app:dev") {
		t.Errorf("token = %q, want the one derived for app:dev", got.token)
	}
	if got.body["engine"] != "codex" || got.body["hook_event_name"] != "PermissionRequest" || got.body["target"] != "app:dev" {
		t.Errorf("body = %v", got.body)
	}
	if _, leaked := got.body["transcript"]; leaked {
		t.Error("the hook forwarded a field it was not asked to")
	}
}

func TestHookStatusGivesUpOnAHangingDaemon(t *testing.T) {
	// The agent is blocked while this runs. A daemon that accepts the
	// connection and never answers must not hold it hostage.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	err := runHookStatus(strings.NewReader(`{"hook_event_name":"Stop"}`), "claude", "app:dev", hookPort(t, srv), t.TempDir())
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("a timeout must not surface as an error: %v", err)
	}
	if elapsed > 2*hookTimeout {
		t.Errorf("took %v, want the %v timeout to hold", elapsed, hookTimeout)
	}
}
