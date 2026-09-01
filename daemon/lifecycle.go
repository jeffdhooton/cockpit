package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
)

// LaunchAgentLabel identifies the macOS launch agent.
const LaunchAgentLabel = "com.jeffdhooton.cockpit.daemon"

// shutdownGrace is how long in-flight requests get to finish on stop.
const shutdownGrace = 3 * time.Second

// StateDir is where the daemon keeps its pid and log files: beside the config,
// so everything cockpit owns lives in one directory.
func StateDir() string {
	return filepath.Dir(config.DefaultConfigPath())
}

// PidFilePath is where the running daemon records its process id.
func PidFilePath() string { return filepath.Join(StateDir(), "daemon.pid") }

// LogFilePath is where a backgrounded daemon writes its output.
func LogFilePath() string { return filepath.Join(StateDir(), "daemon.log") }

// WritePidFile records a process id, creating the state directory if this is
// the first run.
func WritePidFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}

// ReadPidFile reads a recorded process id.
func ReadPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("pidfile %s does not contain a process id", path)
	}
	return pid, nil
}

// IsRunning reports whether a process id is live. Signal 0 checks for the
// process without disturbing it.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// IsServing reports whether something is already listening on the daemon's
// port. A daemon started by launchd writes no pidfile, so this is the only way
// to notice it is up.
func IsServing(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// listen binds the loopback interface. The daemon is never reachable from off
// the machine.
func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Serve runs the tool server on an existing listener until the context is
// cancelled.
func Serve(ctx context.Context, ln net.Listener, tools ToolHandler, version string, statusKey []byte, runner sources.Runner) error {
	srv := &http.Server{Handler: newServerWithStatus(tools, version, statusKey, runner).Handler()}

	errs := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}

// Run binds the configured port and serves until the context is cancelled.
func Run(ctx context.Context, cfg *config.Config, configPath, version string) error {
	ln, err := listen(cfg.Daemon.Port)
	if err != nil {
		return fmt.Errorf("cannot bind port %d: %w", cfg.Daemon.Port, err)
	}

	// Preflight: one cheap question before serving. A daemon that cannot find
	// tmux answers every query with an empty world, which reads as "nothing is
	// running" rather than "I cannot see". Resolve the absolute path now and
	// use it, so a thin PATH cannot silently blind the daemon later.
	runner := sources.DefaultRunner()
	tmuxPath, err := sources.ResolveTmux()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cockpit daemon: %v\n", err)
		fmt.Fprintf(os.Stderr, "cockpit daemon: PATH=%s\n", os.Getenv("PATH"))
		fmt.Fprintln(os.Stderr, "cockpit daemon: serving anyway; tmux-backed tools will report this error rather than an empty result")
	} else {
		runner = sources.ExecRunner{Binary: tmuxPath}
		fmt.Fprintf(os.Stderr, "cockpit daemon: using tmux at %s\n", tmuxPath)
	}

	// The status key is not required to serve tools. A daemon that cannot read
	// or create it still answers every existing query; only the hook endpoint
	// goes dark, and it says so with a 503 rather than accepting an unproven
	// caller.
	statusKey, err := LoadOrCreateStatusKey(config.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cockpit daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "cockpit daemon: serving anyway; hook status will be refused")
	}

	tools := NewTools(cfg, configPath, runner, version, cfg.Daemon.Port)
	fmt.Fprintf(os.Stderr, "cockpit daemon listening on http://127.0.0.1:%d/mcp\n", cfg.Daemon.Port)
	return Serve(ctx, ln, tools, version, statusKey, runner)
}

// fallbackPath is used when the installing shell has no PATH worth copying. It
// names the usual package-manager locations, because launchd's own
// /usr/bin:/bin:/usr/sbin:/sbin contains no tmux on a typical Mac.
const fallbackPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// LaunchAgentOptions is what the plist needs to render.
type LaunchAgentOptions struct {
	BinPath    string
	ConfigPath string
	LogPath    string
	// Path is the PATH the daemon runs with. launchd supplies a bare one that
	// cannot find a Homebrew tmux, so the installing shell's PATH is copied in.
	Path string
}

// LaunchAgentPlist renders the macOS launch agent that keeps the daemon up
// across logins.
func LaunchAgentPlist(o LaunchAgentOptions) string {
	path := o.Path
	if path == "" {
		path = fallbackPath
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
		<string>--config</string>
		<string>%s</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, LaunchAgentLabel, o.BinPath, o.ConfigPath, path, o.LogPath, o.LogPath)
}

// LaunchAgentPath is where the plist is installed.
func LaunchAgentPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}
