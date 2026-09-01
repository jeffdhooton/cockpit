package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/daemon"
	"github.com/spf13/cobra"
)

// startupGrace is how long `daemon start` waits before checking that the child
// survived. Long enough to catch a port that is already taken.
const startupGrace = 300 * time.Millisecond

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the tool server for agents",
	Long: "Serve cockpit's tool server so agents such as Claude Code and Codex can\n" +
		"inspect and drive your workspace. Runs in the foreground; use\n" +
		"`cockpit daemon start` to run it in the background.",
	RunE: runDaemon,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon in the background",
	RunE:  runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background daemon",
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report whether the daemon is running",
	RunE:  runDaemonStatus,
}

var daemonInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install a launch agent so the daemon starts at login (macOS)",
	RunE:  runDaemonInstall,
}

var daemonUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the launch agent (macOS)",
	RunE:  runDaemonUninstall,
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd, daemonStopCmd, daemonStatusCmd, daemonInstallCmd, daemonUninstallCmd)
	rootCmd.AddCommand(daemonCmd)
}

// loadDaemonConfig loads the config and reports the path it came from.
func loadDaemonConfig() (*config.Config, string, error) {
	path := getConfigPath()
	cfg, err := config.Load(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg, path, err := loadDaemonConfig()
	if err != nil {
		return err
	}
	if !cfg.Daemon.IsEnabled() {
		return fmt.Errorf("the daemon is disabled — set enabled = true under [daemon] in %s", path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return daemon.Run(ctx, cfg, path, version)
}

func runDaemonStart(cmd *cobra.Command, args []string) error {
	cfg, path, err := loadDaemonConfig()
	if err != nil {
		return err
	}

	pidPath := daemon.PidFilePath()
	if pid, err := daemon.ReadPidFile(pidPath); err == nil && daemon.IsRunning(pid) {
		fmt.Printf("cockpit daemon is already running (pid %d, port %d)\n", pid, cfg.Daemon.Port)
		return nil
	}
	if daemon.IsServing(cfg.Daemon.Port) {
		fmt.Printf("port %d is already serving — the launch agent may have started it\n", cfg.Daemon.Port)
		fmt.Println("run `cockpit daemon status`, or `cockpit daemon uninstall` to remove the launch agent")
		return nil
	}
	// Anything left here is a corpse from a previous run.
	_ = os.Remove(pidPath)

	bin, err := os.Executable()
	if err != nil {
		return err
	}

	logPath := daemon.LogFilePath()
	if err := os.MkdirAll(daemon.StateDir(), 0755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	child := exec.Command(bin, "daemon", "--config", path)
	child.Stdout = logFile
	child.Stderr = logFile
	// A new session, so the daemon outlives the terminal that launched it.
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		return err
	}

	// Give it a moment to fail loudly — a taken port is the common case.
	time.Sleep(startupGrace)
	if !daemon.IsRunning(child.Process.Pid) {
		return fmt.Errorf("daemon exited immediately — see %s", logPath)
	}

	if err := daemon.WritePidFile(pidPath, child.Process.Pid); err != nil {
		return err
	}

	fmt.Printf("cockpit daemon started (pid %d) on http://127.0.0.1:%d/mcp\n", child.Process.Pid, cfg.Daemon.Port)
	fmt.Printf("logs: %s\n", logPath)
	return nil
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadDaemonConfig()
	if err != nil {
		return err
	}

	pidPath := daemon.PidFilePath()
	pid, err := daemon.ReadPidFile(pidPath)
	if err != nil {
		if daemon.IsServing(cfg.Daemon.Port) {
			fmt.Printf("port %d is serving but no pidfile exists — the launch agent owns it\n", cfg.Daemon.Port)
			fmt.Println("run `cockpit daemon uninstall` to stop it")
			return nil
		}
		fmt.Println("cockpit daemon is not running")
		return nil
	}
	if !daemon.IsRunning(pid) {
		_ = os.Remove(pidPath)
		fmt.Println("cockpit daemon is not running (removed a stale pidfile)")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("could not stop pid %d: %w", pid, err)
	}
	_ = os.Remove(pidPath)

	fmt.Printf("cockpit daemon stopped (pid %d)\n", pid)
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadDaemonConfig()
	if err != nil {
		return err
	}

	pid, err := daemon.ReadPidFile(daemon.PidFilePath())
	switch {
	case err == nil && daemon.IsRunning(pid):
		fmt.Printf("cockpit daemon: running (pid %d)\n", pid)
	case daemon.IsServing(cfg.Daemon.Port):
		// No pidfile, but the port answers — a launch agent started it.
		fmt.Println("cockpit daemon: running (started by the launch agent)")
	default:
		fmt.Println("cockpit daemon: stopped")
		return nil
	}

	fmt.Printf("endpoint: http://127.0.0.1:%d/mcp\n", cfg.Daemon.Port)
	fmt.Printf("logs: %s\n", daemon.LogFilePath())
	return nil
}

func runDaemonInstall(cmd *cobra.Command, args []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launch agents are macOS only — run `cockpit daemon start` instead")
	}

	_, path, err := loadDaemonConfig()
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}

	plistPath := daemon.LaunchAgentPath()
	if plistPath == "" {
		return fmt.Errorf("cannot determine your home directory")
	}
	if err := os.MkdirAll(daemon.StateDir(), 0755); err != nil {
		return err
	}

	plist := daemon.LaunchAgentPlist(bin, path, daemon.LogFilePath())
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return err
	}

	// Reload rather than load, so reinstalling over an older plist works.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %s", out)
	}

	fmt.Printf("installed %s\n", plistPath)
	fmt.Println("the daemon will now start at login")
	return nil
}

func runDaemonUninstall(cmd *cobra.Command, args []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launch agents are macOS only")
	}

	plistPath := daemon.LaunchAgentPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		fmt.Println("no launch agent installed")
		return nil
	}

	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil {
		return err
	}

	fmt.Printf("removed %s\n", plistPath)
	return nil
}
