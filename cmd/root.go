package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
	"github.com/jhoot/cockpit/tui"
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
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create default config file",
	RunE:  runInit,
}

var capCmd = &cobra.Command{
	Use:   "cap [thought]",
	Short: "Capture a thought to inbox",
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
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func getConfigPath() string {
	if cfgPath != "" {
		return cfgPath
	}
	return config.DefaultConfigPath()
}

func runInit(cmd *cobra.Command, args []string) error {
	path := getConfigPath()
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Config already exists at %s. Remove it first to regenerate.\n", path)
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(getConfigTemplate()), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("Config created at %s — edit it to add your repos and vault path.\n", path)
	return nil
}

// getConfigTemplate returns the config template string.
// This is injected from the main package via SetConfigTemplate.
var getConfigTemplate func() string

func SetConfigTemplate(fn func() string) {
	getConfigTemplate = fn
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
			return runTUI(cfg)
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

	// Not inside tmux — create session and attach
	cockpitBin, _ := os.Executable()
	tmuxCmd := exec.Command("tmux", "new-session", "-s", cfg.General.SessionName, cockpitBin)
	tmuxCmd.Stdin = os.Stdin
	tmuxCmd.Stdout = os.Stdout
	tmuxCmd.Stderr = os.Stderr
	return tmuxCmd.Run()
}

func runTUI(cfg *config.Config) error {
	m := tui.NewModel(cfg)
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
		if err := sources.AppendInbox(cfg.Obsidian.InboxFile, text); err != nil {
			return fmt.Errorf("failed to capture: %w", err)
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
		if err := sources.AppendInbox(cfg.Obsidian.InboxFile, text); err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		fmt.Printf("Captured: %s\n", text)
	}
	return nil
}
