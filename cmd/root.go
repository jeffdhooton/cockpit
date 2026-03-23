package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jhoot/cockpit/config"
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
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("cockpit TUI starting...")
		return nil
	},
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create default config file",
	RunE:  runInit,
}

var capCmd = &cobra.Command{
	Use:   "cap [thought]",
	Short: "Capture a thought to inbox",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			fmt.Printf("capture: %s\n", strings.Join(args, " "))
		} else {
			fmt.Println("capture: (interactive mode placeholder)")
		}
		return nil
	},
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
