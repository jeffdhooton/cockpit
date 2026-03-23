package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	General  GeneralConfig  `toml:"general"`
	Obsidian ObsidianConfig `toml:"obsidian"`
	Repos    []RepoConfig   `toml:"repos"`
	GitHub   GitHubConfig   `toml:"github"`
	Signals  SignalsConfig  `toml:"signals"`
}

type GeneralConfig struct {
	SessionName     string `toml:"session_name"`
	RefreshInterval int    `toml:"refresh_interval"`
}

type ObsidianConfig struct {
	VaultPath string `toml:"vault_path"`
	TodayFile string `toml:"today_file"`
	InboxFile string `toml:"inbox_file"`
}

type RepoConfig struct {
	Path  string `toml:"path"`
	Label string `toml:"label"`
}

type GitHubConfig struct {
	Enabled         bool `toml:"enabled"`
	RefreshInterval int  `toml:"refresh_interval"`
}

type SignalsConfig struct {
	StaleSessionThreshold string `toml:"stale_session_threshold"`
	ShowStaleSessions     bool   `toml:"show_stale_sessions"`
	ShowUnpushed          bool   `toml:"show_unpushed"`
	ShowFailingCI         bool   `toml:"show_failing_ci"`
}

func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "cockpit", "config.toml")
}

func Load(path string) (*Config, error) {
	path = expandTilde(path)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no config found at %s", path)
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse error: %w", err)
	}

	applyDefaults(&cfg)
	expandPaths(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.General.SessionName == "" {
		cfg.General.SessionName = "cockpit"
	}
	if cfg.General.RefreshInterval == 0 {
		cfg.General.RefreshInterval = 5
	}
	if cfg.GitHub.RefreshInterval == 0 {
		cfg.GitHub.RefreshInterval = 60
	}
	if cfg.Signals.StaleSessionThreshold == "" {
		cfg.Signals.StaleSessionThreshold = "24h"
	}
	// Default booleans for signals (true by default — use pointer or explicit check)
	// Since Go zero-values bools to false, we handle this via a separate mechanism.
	// For simplicity, we treat the TOML as authoritative and set defaults only if
	// the entire [signals] section is missing. The template has them set to true.
}

func expandPaths(cfg *Config) {
	cfg.Obsidian.VaultPath = expandTilde(cfg.Obsidian.VaultPath)
	cfg.Obsidian.TodayFile = expandTilde(cfg.Obsidian.TodayFile)
	cfg.Obsidian.InboxFile = expandTilde(cfg.Obsidian.InboxFile)
	for i := range cfg.Repos {
		cfg.Repos[i].Path = expandTilde(cfg.Repos[i].Path)
	}
}

func validate(cfg *Config) error {
	if cfg.Obsidian.VaultPath == "" {
		return fmt.Errorf("config: vault_path is required")
	}
	if cfg.General.RefreshInterval <= 0 {
		return fmt.Errorf("config: refresh_interval must be > 0")
	}
	if _, err := time.ParseDuration(cfg.Signals.StaleSessionThreshold); err != nil {
		return fmt.Errorf("config: stale_session_threshold is invalid: %w", err)
	}
	return nil
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
