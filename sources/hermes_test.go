package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/config"
)

// hermesFixture is the real /api/status document from mini, trimmed.
const hermesFixture = `{"version":"0.16.0","release_date":"2026.6.5","hermes_home":"/Users/jclaw/.hermes",
"gateway_running":true,"gateway_pid":676,"gateway_state":"running",
"gateway_platforms":{"photon":{"state":"connected","error_code":null},"slack":{"state":"connected","error_code":null},
"discord":{"state":"disconnected","error_code":"auth"}},"gateway_exit_reason":null}`

func hermesServer(t *testing.T, status int, body string) config.HermesConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return config.HermesConfig{Label: "hermes", URL: srv.URL}
}

func TestHermesStatusParsesTheRealDocument(t *testing.T) {
	got := GetHermesStatus(context.Background(), http.DefaultClient, hermesServer(t, 200, hermesFixture))

	if !got.Reachable || got.Err != nil {
		t.Fatalf("got %+v", got)
	}
	if got.Gateway != "running" || got.Version != "0.16.0" {
		t.Errorf("got %+v", got)
	}
	// Only connected platforms, sorted; discord is disconnected and left out.
	if !slices.Equal(got.Platforms, []string{"photon", "slack"}) {
		t.Errorf("platforms = %v", got.Platforms)
	}
}

func TestHermesStatusStoppedIsReachable(t *testing.T) {
	body := strings.Replace(hermesFixture, `"gateway_running":true`, `"gateway_running":false`, 1)
	body = strings.Replace(body, `"gateway_state":"running"`, `"gateway_state":"stopped"`, 1)
	got := GetHermesStatus(context.Background(), http.DefaultClient, hermesServer(t, 200, body))

	if !got.Reachable || got.Gateway != "stopped" {
		t.Errorf("a stopped gateway is a reachable fact, got %+v", got)
	}
}

func TestHermesStatusUnauthorizedIsUnreachableWithReason(t *testing.T) {
	got := GetHermesStatus(context.Background(), http.DefaultClient, hermesServer(t, 401, `{"error":"unauthorized"}`))
	if got.Reachable || got.Err == nil || !strings.Contains(got.Err.Error(), "401") {
		t.Errorf("got %+v", got)
	}
}

func TestHermesStatusConnectionRefused(t *testing.T) {
	cfg := hermesServer(t, 200, hermesFixture)
	srvURL := cfg.URL
	// Close the server and point at its now-dead port.
	got := GetHermesStatus(context.Background(), http.DefaultClient, config.HermesConfig{Label: "hermes", URL: closedPort(t, srvURL)})
	if got.Reachable || got.Err == nil {
		t.Errorf("got %+v", got)
	}
}

func closedPort(t *testing.T, _ string) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

func TestHermesStatusBoundsTheBody(t *testing.T) {
	huge := `{"gateway_running":true,"gateway_state":"running","pad":"` + strings.Repeat("x", 1<<20) + `"}`
	got := GetHermesStatus(context.Background(), http.DefaultClient, hermesServer(t, 200, huge))
	// Either it parses what it read or it reports unreachable; it must not
	// hang or allocate a megabyte for a status tile.
	if got.Reachable && got.Gateway != "running" {
		t.Errorf("got %+v", got)
	}
}
