package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
	"github.com/jeffdhooton/cockpit/tui"
	"github.com/spf13/cobra"
)

var (
	cfgPath string
	version = "dev"
)

func SetVersion(v string) {
	version = v
}

var rootCmd = &cobra.Command{
	Use:   "cockpit",
	Short: "tmux-native Terminal Command Center",
	RunE:  runRoot,
	// A command that fails at runtime should show its error, not bury it under
	// the full help text. Usage still prints for genuine usage mistakes.
	// Errors are printed once, by Execute, so an explicit exit code and its
	// message are not echoed twice.
	SilenceUsage:  true,
	SilenceErrors: true,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create default config file",
	RunE:  runInit,
}

var capCmd = &cobra.Command{
	Use:   "cap [task]",
	Short: "Capture a task to today's list",
	RunE:  runCap,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("cockpit v%s\n", version)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", "", "config file path (default ~/.config/cockpit/config.toml)")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(capCmd)
	rootCmd.AddCommand(versionCmd)

	hookStatusCmd.Flags().StringVar(&hookEngine, "engine", "claude", "reporting engine: claude or codex")
	hookCmd.AddCommand(hookStatusCmd)
	hookInstallCmd.Flags().StringVar(&hookInstallHost, "host", "", "install on a configured remote host instead of this machine")
	hookCmd.AddCommand(hookInstallCmd)
	rootCmd.AddCommand(hookCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, "cockpit: "+ee.msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		os.Exit(1)
	}
}

// capture appends a thought to today's file, or says plainly that there is
// no such file rather than failing on an empty path.
func capture(cfg *config.Config, text string) error {
	if cfg.Obsidian.TodayFile == "" {
		return fmt.Errorf("no today_file configured under [obsidian], nowhere to capture to")
	}
	if err := sources.AppendInbox(cfg.Obsidian.TodayFile, text); err != nil {
		return fmt.Errorf("failed to capture: %w", err)
	}
	return nil
}

func getConfigPath() string {
	if cfgPath != "" {
		return cfgPath
	}
	return config.DefaultConfigPath()
}

func runRoot(cmd *cobra.Command, args []string) error {
	// Check tmux is installed
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is required but not found in PATH")
	}

	// Load config
	path := getConfigPath()
	cfg, err := config.Load(path)
	if err != nil {
		if strings.Contains(err.Error(), "no config found") {
			fmt.Println("No config found. Run `cockpit init` to create one.")
			return nil
		}
		return err
	}

	// tmux bootstrap
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv != "" {
		// Already inside tmux — check if this is the cockpit session
		currentSession, _ := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
		if strings.TrimSpace(string(currentSession)) == cfg.General.SessionName {
			// We're in the cockpit session — run TUI
			return runTUI(cfg, path)
		}
		// In a different session — switch to cockpit session
		if err := exec.Command("tmux", "switch-client", "-t", cfg.General.SessionName).Run(); err != nil {
			// Session doesn't exist, create it
			cockpitBin, _ := os.Executable()
			if err := exec.Command("tmux", "new-session", "-d", "-s", cfg.General.SessionName, cockpitBin).Run(); err != nil {
				return fmt.Errorf("failed to create cockpit session: %w", err)
			}
			return exec.Command("tmux", "switch-client", "-t", cfg.General.SessionName).Run()
		}
		return nil
	}

	// Not inside tmux — check if session exists, then attach or create
	if err := exec.Command("tmux", "has-session", "-t", cfg.General.SessionName).Run(); err == nil {
		// Session exists — just attach
		attachCmd := exec.Command("tmux", "attach-session", "-t", cfg.General.SessionName)
		attachCmd.Stdin = os.Stdin
		attachCmd.Stdout = os.Stdout
		attachCmd.Stderr = os.Stderr
		return attachCmd.Run()
	}

	// Session doesn't exist — create and attach
	cockpitBin, _ := os.Executable()
	tmuxCmd := exec.Command("tmux", "new-session", "-s", cfg.General.SessionName, cockpitBin)
	tmuxCmd.Stdin = os.Stdin
	tmuxCmd.Stdout = os.Stdout
	tmuxCmd.Stderr = os.Stderr
	return tmuxCmd.Run()
}

func runTUI(cfg *config.Config, configPath string) error {
	m := tui.NewModel(cfg, configPath)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func runCap(cmd *cobra.Command, args []string) error {
	path := getConfigPath()
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(args) > 0 {
		text := strings.Join(args, " ")
		if err := capture(cfg, text); err != nil {
			return err
		}
		fmt.Printf("Captured: %s\n", text)
		return nil
	}

	// Interactive mode
	fmt.Println("Capture mode (empty line to exit):")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		text := scanner.Text()
		if text == "" {
			break
		}
		if err := capture(cfg, text); err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		fmt.Printf("Captured: %s\n", text)
	}
	return nil
}
