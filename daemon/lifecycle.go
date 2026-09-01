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

// listen binds the loopback interface. The daemon is never reachable from off
// the machine.
func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Serve runs the tool server on an existing listener until the context is
// cancelled.
func Serve(ctx context.Context, ln net.Listener, tools ToolHandler, version string) error {
	srv := &http.Server{Handler: NewServer(tools, version).Handler()}

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

	tools := NewTools(cfg, configPath, sources.DefaultRunner(), version, cfg.Daemon.Port)
	fmt.Fprintf(os.Stderr, "cockpit daemon listening on http://127.0.0.1:%d/mcp\n", cfg.Daemon.Port)
	return Serve(ctx, ln, tools, version)
}

// LaunchAgentPlist renders the macOS launch agent that keeps the daemon up
// across logins.
func LaunchAgentPlist(binPath, configPath, logPath string) string {
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
`, LaunchAgentLabel, binPath, configPath, logPath, logPath)
}

// LaunchAgentPath is where the plist is installed.
func LaunchAgentPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}
