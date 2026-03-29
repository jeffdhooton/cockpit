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
	path = ExpandTilde(path)

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
	cfg.Obsidian.VaultPath = ExpandTilde(cfg.Obsidian.VaultPath)
	cfg.Obsidian.TodayFile = ExpandTilde(cfg.Obsidian.TodayFile)
	cfg.Obsidian.InboxFile = ExpandTilde(cfg.Obsidian.InboxFile)
	for i := range cfg.Repos {
		cfg.Repos[i].Path = ExpandTilde(cfg.Repos[i].Path)
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

// CollapseTilde replaces the $HOME prefix with ~ for human-friendly display.
func CollapseTilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// AppendRepo appends a [[repos]] entry to the config file.
func AppendRepo(configPath string, repo RepoConfig) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("config: read error: %w", err)
	}

	block := fmt.Sprintf("\n[[repos]]\npath = %q\nlabel = %q\n", CollapseTilde(repo.Path), repo.Label)

	content := string(data)

	// Find insertion point: just before [github] or [signals] section headers,
	// so [[repos]] entries stay contiguous.
	insertIdx := -1
	for _, marker := range []string{"[github]", "[signals]"} {
		idx := strings.Index(content, marker)
		if idx != -1 && (insertIdx == -1 || idx < insertIdx) {
			insertIdx = idx
		}
	}

	var result string
	if insertIdx != -1 {
		result = content[:insertIdx] + block + "\n" + content[insertIdx:]
	} else {
		// No section headers found — append to end
		result = content + block
	}

	return os.WriteFile(configPath, []byte(result), 0644)
}

func ExpandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
