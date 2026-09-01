package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jhoot/cockpit/config"
)

func TestPidFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")

	if err := WritePidFile(path, 4242); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPidFile(path)
	if err != nil || got != 4242 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestReadPidFileMissing(t *testing.T) {
	if _, err := ReadPidFile(filepath.Join(t.TempDir(), "absent.pid")); err == nil {
		t.Fatal("a missing pidfile must error rather than report pid zero")
	}
}

func TestReadPidFileGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := os.WriteFile(path, []byte("not-a-pid"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPidFile(path); err == nil {
		t.Fatal("a garbage pidfile must error")
	}
}

func TestWritePidFileCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "daemon.pid")
	if err := WritePidFile(path, 1); err != nil {
		t.Fatalf("a first run has no state directory yet: %v", err)
	}
}

func TestIsRunning(t *testing.T) {
	if !IsRunning(os.Getpid()) {
		t.Error("the current process is running")
	}
	if IsRunning(0) {
		t.Error("pid 0 is not a running cockpit daemon")
	}
	if IsRunning(999999) {
		t.Error("an absent pid should not report as running")
	}
}

func TestLaunchAgentPlistContainsPaths(t *testing.T) {
	plist := LaunchAgentPlist(LaunchAgentOptions{
		BinPath:    "/usr/local/bin/cockpit",
		ConfigPath: "/home/j/.config/cockpit/config.toml",
		LogPath:    "/home/j/.config/cockpit/daemon.log",
	})

	for _, want := range []string{
		LaunchAgentLabel,
		"/usr/local/bin/cockpit",
		"/home/j/.config/cockpit/config.toml",
		"/home/j/.config/cockpit/daemon.log",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestStatePathsLiveBesideTheConfig(t *testing.T) {
	dir := filepath.Dir(config.DefaultConfigPath())
	for _, path := range []string{PidFilePath(), LogFilePath()} {
		if filepath.Dir(path) != dir {
			t.Errorf("%s should sit beside the config in %s", path, dir)
		}
	}
}

func TestServeAnswersToolsList(t *testing.T) {
	cfg := &config.Config{}
	cfg.General.SessionName = "cockpit"
	tools := NewTools(cfg, "/tmp/config.toml", &fakeRunner{}, "9.9.9", 0)

	ln, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, ln, tools, "9.9.9") }()

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/mcp", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("the served endpoint should answer: %v", err)
	}
	defer res.Body.Close()

	var decoded struct {
		Result struct {
			Tools []ToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Result.Tools) != 14 {
		t.Errorf("want 14 tools over the wire, got %d", len(decoded.Result.Tools))
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	cfg := &config.Config{}
	tools := NewTools(cfg, "/tmp/config.toml", &fakeRunner{}, "1", 0)

	ln, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, tools, "1") }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a cancelled context is a clean shutdown, not a failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop when its context was cancelled")
	}
}

func TestIsServingProbesThePort(t *testing.T) {
	// A daemon started by launchd writes no pidfile, so the pidfile alone
	// cannot answer "is it running?" — probing the port can.
	cfg := &config.Config{}
	tools := NewTools(cfg, "/tmp/config.toml", &fakeRunner{}, "1", 0)

	ln, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, ln, tools, "1") }()

	// Give the listener a moment to accept.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !IsServing(port) {
		time.Sleep(20 * time.Millisecond)
	}

	if !IsServing(port) {
		t.Error("a served port should report as serving")
	}

	free, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	if IsServing(freePort) {
		t.Error("a closed port should not report as serving")
	}
}

func TestLaunchAgentPlistCarriesPath(t *testing.T) {
	// launchd hands an agent a bare /usr/bin:/bin:/usr/sbin:/sbin, which does
	// not include Homebrew — so a daemon started at login could not find tmux
	// and reported an empty world instead of an error.
	plist := LaunchAgentPlist(LaunchAgentOptions{
		BinPath:    "/usr/local/bin/cockpit",
		ConfigPath: "/home/j/.config/cockpit/config.toml",
		LogPath:    "/home/j/.config/cockpit/daemon.log",
		Path:       "/opt/homebrew/bin:/usr/bin:/bin",
	})

	if !strings.Contains(plist, "<key>EnvironmentVariables</key>") {
		t.Error("plist must set an environment")
	}
	if !strings.Contains(plist, "/opt/homebrew/bin:/usr/bin:/bin") {
		t.Errorf("plist must carry the PATH that could actually find tmux:\n%s", plist)
	}
}

func TestLaunchAgentPlistFallsBackToASanePath(t *testing.T) {
	plist := LaunchAgentPlist(LaunchAgentOptions{
		BinPath: "/usr/local/bin/cockpit", ConfigPath: "/c.toml", LogPath: "/l.log",
	})
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if !strings.Contains(plist, want) {
			t.Errorf("an empty PATH should fall back to the common install locations, missing %q", want)
		}
	}
}
