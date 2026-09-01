package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/daemon"
	"github.com/spf13/cobra"
)

// hookTimeout bounds the whole round trip. The agent is blocked while this
// runs, so the ceiling is low and applies to the connection and the reply
// together.
const hookTimeout = 500 * time.Millisecond

// hookStdinLimit bounds what is read from the agent. Claude's Stop event can
// carry an entire assistant message; only the event name is wanted.
const hookStdinLimit = 1 << 20

var hookEngine string

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Report agent lifecycle events to cockpit",
	Hidden: true,
}

var hookStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Record an agent status event read from stdin",
	Long: `Reads a hook event from stdin and posts it to the daemon.

Installed into Claude Code and Codex by "cockpit hook install". It exits 0
unconditionally: a hook that fails, fails the agent that called it, and no
status is worth that.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Every failure below is swallowed on purpose, including a missing
		// config. The hook must be safe to leave installed globally forever.
		port := 0
		if cfg, err := config.Load(getConfigPath()); err == nil {
			port = cfg.Daemon.Port
		}
		return runHookStatus(os.Stdin, hookEngine, resolveTarget(), port, config.Dir())
	},
}

// resolveTarget finds the tmux target this hook is running inside.
//
// Prefer the environment cockpit injects into processes it launched, then ask
// tmux directly — which covers every session started by hand, and that is
// most of them and most of the value.
func resolveTarget() string {
	if t := os.Getenv("COCKPIT_STATUS_TARGET"); t != "" {
		return t
	}
	// Outside tmux there is nothing to report on, and display-message would
	// fail anyway. Checking first keeps the no-op path free of a subprocess.
	if os.Getenv("TMUX") == "" {
		return ""
	}

	tmux, err := lookTmux()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, tmux, "display-message", "-p",
		"#{session_name}:#{window_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// lookTmux resolves tmux by absolute path. The hook inherits whatever
// environment the agent hands it, and a launch agent's PATH is a bare
// /usr/bin:/bin:/usr/sbin:/sbin with no Homebrew in it.
func lookTmux() (string, error) {
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("tmux")
}

// runHookStatus posts one event and never reports failure. Only the event
// name leaves this process; nothing else from the payload is forwarded.
func runHookStatus(stdin io.Reader, engine, target string, port int, keyDir string) error {
	if target == "" || port == 0 {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(stdin, hookStdinLimit))
	if err != nil {
		return nil
	}
	var event struct {
		Event string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &event); err != nil || event.Event == "" {
		return nil
	}

	key, err := daemon.LoadOrCreateStatusKey(keyDir)
	if err != nil {
		return nil
	}

	body, err := json.Marshal(map[string]string{
		"engine":          engine,
		"hook_event_name": event.Event,
		"target":          target,
	})
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/hooks/status", port), bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-cockpit-status-token", daemon.StatusToken(key, target))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	_ = resp.Body.Close()
	return nil
}
