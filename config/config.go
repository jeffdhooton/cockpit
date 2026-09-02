package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultDaemonPort is the loopback port the tool server binds by default.
const DefaultDaemonPort = 45679

// validProcessName constrains process names to what tmux accepts as a window
// name without quoting.
var validProcessName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validHostName shares the label grammar, so host/label is unambiguous.
var validHostName = validProcessName

type Config struct {
	General  GeneralConfig  `toml:"general"`
	Obsidian ObsidianConfig `toml:"obsidian"`
	Repos    []RepoConfig   `toml:"repos"`
	GitHub   GitHubConfig   `toml:"github"`
	Signals  SignalsConfig  `toml:"signals"`
	Daemon   DaemonConfig   `toml:"daemon"`
	Hosts    []HostConfig   `toml:"hosts"`
	Hermes   []HermesConfig `toml:"hermes"`
}

type GeneralConfig struct {
	SessionName     string `toml:"session_name"`
	RefreshInterval int    `toml:"refresh_interval"`
	DefaultView     string `toml:"default_view"` // "grid" (default) or "dashboard"
}

type ObsidianConfig struct {
	// VaultPath is accepted for older configs but nothing reads it. The two
	// files below are what cockpit opens, as absolute paths.
	VaultPath string `toml:"vault_path"`
	TodayFile string `toml:"today_file"`
	InboxFile string `toml:"inbox_file"`
}

// Enabled reports whether there is anywhere to read tasks from or capture
// to. A daemon-only machine has no vault, and that is fine: the panels show
// nothing and capture says why.
func (o ObsidianConfig) Enabled() bool {
	return o.TodayFile != "" || o.InboxFile != ""
}

type RepoConfig struct {
	Path  string `toml:"path"`
	Label string `toml:"label"`
	// Host names a [[hosts]] entry. Empty means this machine. A remote
	// path is left exactly as written, because ~ belongs to the remote user.
	Host      string          `toml:"host"`
	Processes []ProcessConfig `toml:"processes"`
}

// Key identifies the repo across hosts: host/label remotely, label locally.
// validLabel forbids "/", so the qualified form cannot collide with a bare one.
func (r RepoConfig) Key() string {
	if r.Host == "" {
		return r.Label
	}
	return r.Host + "/" + r.Label
}

// HostConfig is a machine cockpit reaches over SSH. Name is an ssh config
// alias, resolved by ssh itself so Include, Match, and per-host identity
// settings are honoured for free.
type HostConfig struct {
	Name string `toml:"name"`
	// Tmux is the absolute path of tmux on that machine. Required: a bare
	// ssh command gets the non-login PATH, which on a Mac excludes Homebrew.
	Tmux string `toml:"tmux"`
	// Cockpit is the absolute path of cockpit on that machine, when installed.
	// It enables `cockpit hook install --host`.
	Cockpit string `toml:"cockpit"`
}

// HermesConfig is a Hermes dashboard whose status endpoint answers without a
// token. Cockpit reads gateway health from it. When Host names a [[hosts]]
// entry, Enter on the tile opens a shell on that machine; otherwise the tile
// is read-only.
type HermesConfig struct {
	Label           string `toml:"label"`
	URL             string `toml:"url"`
	Host            string `toml:"host"`
	RefreshInterval int    `toml:"refresh_interval"`
}

// Host looks up a declared host by name.
func (c *Config) Host(name string) (HostConfig, bool) {
	for _, h := range c.Hosts {
		if h.Name == name {
			return h, true
		}
	}
	return HostConfig{}, false
}

// ProcessConfig declares a background process for a repo. Each one launches as
// its own tmux window in the repo's session.
type ProcessConfig struct {
	Name       string            `toml:"name"`
	Command    string            `toml:"command"`
	AutoStart  *bool             `toml:"auto_start"`
	WorkingDir string            `toml:"working_dir"`
	Env        map[string]string `toml:"env"`
	Status     *StatusPatterns   `toml:"status"`
}

// StatusPatterns holds optional regexps used to classify a process's output.
type StatusPatterns struct {
	Ready      string `toml:"ready"`
	Compiling  string `toml:"compiling"`
	Error      string `toml:"error"`
	Restarting string `toml:"restarting"`
}

// All returns every non-empty pattern keyed by its status label.
func (s *StatusPatterns) All() map[string]string {
	if s == nil {
		return nil
	}
	out := map[string]string{}
	for label, expr := range map[string]string{
		"ready":      s.Ready,
		"compiling":  s.Compiling,
		"error":      s.Error,
		"restarting": s.Restarting,
	} {
		if expr != "" {
			out[label] = expr
		}
	}
	return out
}

// ShouldAutoStart reports whether the process launches on jump. Omitting
// auto_start means yes — the common case is a process you always want up.
func (p ProcessConfig) ShouldAutoStart() bool {
	return p.AutoStart == nil || *p.AutoStart
}

// ResolvedWorkingDir returns the directory the process runs in. A relative
// working_dir is joined to the repo path; an empty one is the repo itself.
func (p ProcessConfig) ResolvedWorkingDir(repoPath string) string {
	if p.WorkingDir == "" {
		return repoPath
	}
	dir := ExpandTilde(p.WorkingDir)
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(repoPath, dir)
}

// Process returns the named process from this repo.
func (r RepoConfig) Process(name string) (ProcessConfig, bool) {
	for _, p := range r.Processes {
		if p.Name == name {
			return p, true
		}
	}
	return ProcessConfig{}, false
}

// DaemonConfig configures the local tool server for agents.
type DaemonConfig struct {
	Enabled *bool `toml:"enabled"`
	Port    int   `toml:"port"`
}

// IsEnabled reports whether the daemon should serve. Omitting the setting
// means yes.
func (d DaemonConfig) IsEnabled() bool {
	return d.Enabled == nil || *d.Enabled
}

// Repo returns the configured repo with the given label.
// RepoOn looks up a repo by host and label. Two hosts may each have a
// "docket"; matching on label alone would hand a local session the remote
// repo's config.
func (c *Config) RepoOn(host, label string) (RepoConfig, bool) {
	for _, r := range c.Repos {
		if r.Host == host && r.Label == label {
			return r, true
		}
	}
	return RepoConfig{}, false
}

// Repo looks up a local repo by label.
func (c *Config) Repo(label string) (RepoConfig, bool) {
	return c.RepoOn("", label)
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
	dir := Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// Dir is cockpit's configuration directory. The status key lives here beside
// config.toml, and the hook and the daemon each resolve it independently, so
// they need one answer rather than two spellings of it.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "cockpit")
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
	if cfg.General.DefaultView == "" {
		cfg.General.DefaultView = "grid"
	}
	if cfg.GitHub.RefreshInterval == 0 {
		cfg.GitHub.RefreshInterval = 60
	}
	if cfg.Signals.StaleSessionThreshold == "" {
		cfg.Signals.StaleSessionThreshold = "24h"
	}
	if cfg.Daemon.Port == 0 {
		cfg.Daemon.Port = DefaultDaemonPort
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
		if cfg.Repos[i].Host != "" {
			continue
		}
		cfg.Repos[i].Path = ExpandTilde(cfg.Repos[i].Path)
		for j := range cfg.Repos[i].Processes {
			p := &cfg.Repos[i].Processes[j]
			if p.WorkingDir != "" {
				p.WorkingDir = ExpandTilde(p.WorkingDir)
			}
		}
	}
}

func validate(cfg *Config) error {
	if cfg.General.RefreshInterval <= 0 {
		return fmt.Errorf("config: refresh_interval must be > 0")
	}
	if _, err := time.ParseDuration(cfg.Signals.StaleSessionThreshold); err != nil {
		return fmt.Errorf("config: stale_session_threshold is invalid: %w", err)
	}
	switch cfg.General.DefaultView {
	case "", "grid", "dashboard":
	default:
		return fmt.Errorf("config: default_view must be \"grid\" or \"dashboard\", got %q", cfg.General.DefaultView)
	}
	if err := validateProcesses(cfg); err != nil {
		return err
	}
	if err := validateHosts(cfg); err != nil {
		return err
	}
	if err := validateHermes(cfg); err != nil {
		return err
	}
	return nil
}

func validateHermes(cfg *Config) error {
	for i := range cfg.Hermes {
		h := &cfg.Hermes[i]
		if !validHostName.MatchString(h.Label) {
			return fmt.Errorf("config: hermes %q: label must be alphanumeric, hyphens, or underscores", h.Label)
		}
		u, err := url.Parse(h.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: hermes %q: url must be http or https, got %q", h.Label, h.URL)
		}
		if h.RefreshInterval <= 0 {
			h.RefreshInterval = 30
		}
	}
	return nil
}

// validateHosts fails at load rather than at the first poll. A bad host
// would otherwise show up as an empty grid with no explanation.
func validateHosts(cfg *Config) error {
	seen := map[string]bool{}
	for _, h := range cfg.Hosts {
		if !validHostName.MatchString(h.Name) {
			return fmt.Errorf("config: host %q: name must be alphanumeric, hyphens, or underscores", h.Name)
		}
		if seen[h.Name] {
			return fmt.Errorf("config: duplicate host %q", h.Name)
		}
		seen[h.Name] = true
		if !strings.HasPrefix(h.Tmux, "/") {
			return fmt.Errorf("config: host %q: tmux must be an absolute path, got %q", h.Name, h.Tmux)
		}
	}
	for _, r := range cfg.Repos {
		if r.Host != "" && !seen[r.Host] {
			return fmt.Errorf("config: repo %q names host %q, which is not declared under [[hosts]]", r.Label, r.Host)
		}
	}
	for _, h := range cfg.Hermes {
		if h.Host != "" && !seen[h.Host] {
			return fmt.Errorf("config: hermes %q names host %q, which is not declared under [[hosts]]", h.Label, h.Host)
		}
	}
	return nil
}

func validateProcesses(cfg *Config) error {
	for _, repo := range cfg.Repos {
		seen := map[string]bool{}
		for _, p := range repo.Processes {
			if p.Name == "" {
				return fmt.Errorf("config: repo %q has a process with no name", repo.Label)
			}
			if !validProcessName.MatchString(p.Name) {
				return fmt.Errorf("config: repo %q process %q: name must be alphanumeric, hyphens, or underscores", repo.Label, p.Name)
			}
			if seen[p.Name] {
				return fmt.Errorf("config: repo %q has duplicate process %q", repo.Label, p.Name)
			}
			seen[p.Name] = true

			if strings.TrimSpace(p.Command) == "" {
				return fmt.Errorf("config: repo %q process %q: command is required", repo.Label, p.Name)
			}
			for label, expr := range p.Status.All() {
				if _, err := regexp.Compile(expr); err != nil {
					return fmt.Errorf("config: repo %q process %q: status.%s is not a valid regexp: %w", repo.Label, p.Name, label, err)
				}
			}
		}
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
