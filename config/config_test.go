package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValid(t *testing.T) {
	cfg, err := Load("../testdata/config.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.General.SessionName != "cockpit" {
		t.Errorf("session_name = %q, want %q", cfg.General.SessionName, "cockpit")
	}
	if cfg.General.RefreshInterval != 5 {
		t.Errorf("refresh_interval = %d, want 5", cfg.General.RefreshInterval)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("repos count = %d, want 2", len(cfg.Repos))
	}
	if cfg.Repos[0].Label != "repo-one" {
		t.Errorf("repos[0].label = %q, want %q", cfg.Repos[0].Label, "repo-one")
	}
	if cfg.Obsidian.VaultPath != "/tmp/testvault" {
		t.Errorf("vault_path = %q, want %q", cfg.Obsidian.VaultPath, "/tmp/testvault")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "no config found") {
		t.Errorf("error = %q, want to contain 'no config found'", err.Error())
	}
}

func TestLoadMalformedTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	os.WriteFile(path, []byte("[invalid toml\nthis is broken"), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("error = %q, want to contain 'parse error'", err.Error())
	}
}

func TestLoadMissingVaultPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[general]
session_name = "test"
[obsidian]
vault_path = ""
`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty vault_path")
	}
	if !strings.Contains(err.Error(), "vault_path is required") {
		t.Errorf("error = %q, want to contain 'vault_path is required'", err.Error())
	}
}

func TestLoadTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[obsidian]
vault_path = "~/myvault"
today_file = "~/myvault/today.md"
inbox_file = "~/myvault/inbox.md"
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(home, "myvault")
	if cfg.Obsidian.VaultPath != expected {
		t.Errorf("vault_path = %q, want %q", cfg.Obsidian.VaultPath, expected)
	}
}

func TestLoadDefaultValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[obsidian]
vault_path = "/tmp/vault"
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.General.SessionName != "cockpit" {
		t.Errorf("session_name = %q, want default 'cockpit'", cfg.General.SessionName)
	}
	if cfg.General.RefreshInterval != 5 {
		t.Errorf("refresh_interval = %d, want default 5", cfg.General.RefreshInterval)
	}
	if cfg.GitHub.RefreshInterval != 60 {
		t.Errorf("github.refresh_interval = %d, want default 60", cfg.GitHub.RefreshInterval)
	}
	if cfg.Signals.StaleSessionThreshold != "24h" {
		t.Errorf("stale_session_threshold = %q, want default '24h'", cfg.Signals.StaleSessionThreshold)
	}
}

func TestLoadInvalidStaleThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[obsidian]
vault_path = "/tmp/vault"
[signals]
stale_session_threshold = "notaduration"
`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid stale_session_threshold")
	}
	if !strings.Contains(err.Error(), "stale_session_threshold is invalid") {
		t.Errorf("error = %q, want to contain 'stale_session_threshold is invalid'", err.Error())
	}
}

func TestDefaultViewDefaultsToGrid(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.General.DefaultView != "grid" {
		t.Errorf("DefaultView = %q, want %q", cfg.General.DefaultView, "grid")
	}
}

func TestDefaultViewRejectsUnknownValue(t *testing.T) {
	cfg := &Config{
		General:  GeneralConfig{RefreshInterval: 5, DefaultView: "gird"},
		Obsidian: ObsidianConfig{VaultPath: "/tmp/vault"},
		Signals:  SignalsConfig{StaleSessionThreshold: "24h"},
	}
	if err := validate(cfg); err == nil {
		t.Error("validate should reject an unknown default_view (a silent typo is worse than an error)")
	}
}

func TestDefaultViewAcceptsDashboard(t *testing.T) {
	cfg := &Config{
		General:  GeneralConfig{RefreshInterval: 5, DefaultView: "dashboard"},
		Obsidian: ObsidianConfig{VaultPath: "/tmp/vault"},
		Signals:  SignalsConfig{StaleSessionThreshold: "24h"},
	}
	if err := validate(cfg); err != nil {
		t.Errorf("validate rejected dashboard: %v", err)
	}
}

// writeAndLoad writes a config body to a temp file and loads it, failing the
// test on any load error.
func writeAndLoad(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return cfg
}

// loadErr writes a config body to a temp file and returns the load error.
func loadErr(t *testing.T, body string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	return err
}

func TestLoadProcesses(t *testing.T) {
	cfg := writeAndLoad(t, `
[obsidian]
vault_path = "/tmp/vault"

[[repos]]
path = "/tmp/my-app"
label = "my-app"

  [[repos.processes]]
  name = "dev"
  command = "npm run dev"

  [[repos.processes]]
  name = "test"
  command = "npm test"
  auto_start = false
  working_dir = "packages/web"
  env = { PORT = "3000" }

    [repos.processes.status]
    ready = 'Local:\s+(\S+)'
`)
	procs := cfg.Repos[0].Processes
	if len(procs) != 2 {
		t.Fatalf("want 2 processes, got %d", len(procs))
	}
	if !procs[0].ShouldAutoStart() {
		t.Error("omitted auto_start should default to true")
	}
	if procs[1].ShouldAutoStart() {
		t.Error("auto_start = false should be false")
	}
	if got := procs[1].ResolvedWorkingDir("/tmp/my-app"); got != "/tmp/my-app/packages/web" {
		t.Errorf("working dir = %q", got)
	}
	if procs[1].Env["PORT"] != "3000" {
		t.Errorf("env not parsed: %v", procs[1].Env)
	}
	if procs[1].Status == nil || procs[1].Status.Ready != `Local:\s+(\S+)` {
		t.Errorf("status not parsed: %+v", procs[1].Status)
	}
}

func TestValidateProcesses(t *testing.T) {
	const head = `
[obsidian]
vault_path = "/tmp/vault"

[[repos]]
path = "/tmp/my-app"
label = "my-app"
`
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty name",
			body: head + "\n  [[repos.processes]]\n  name = \"\"\n  command = \"x\"\n",
			want: "no name",
		},
		{
			name: "name with a space",
			body: head + "\n  [[repos.processes]]\n  name = \"dev server\"\n  command = \"x\"\n",
			want: "dev server",
		},
		{
			name: "duplicate names",
			body: head + "\n  [[repos.processes]]\n  name = \"dev\"\n  command = \"x\"\n\n  [[repos.processes]]\n  name = \"dev\"\n  command = \"y\"\n",
			want: "duplicate process",
		},
		{
			name: "empty command",
			body: head + "\n  [[repos.processes]]\n  name = \"dev\"\n  command = \"   \"\n",
			want: "command is required",
		},
		{
			name: "invalid status regexp",
			body: head + "\n  [[repos.processes]]\n  name = \"dev\"\n  command = \"x\"\n\n    [repos.processes.status]\n    ready = \"(unclosed\"\n",
			want: "not a valid regexp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := loadErr(t, tc.body)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestDaemonDefaults(t *testing.T) {
	cfg := writeAndLoad(t, `
[obsidian]
vault_path = "/tmp/vault"
`)
	if !cfg.Daemon.IsEnabled() {
		t.Error("daemon should be enabled by default")
	}
	if cfg.Daemon.Port != 45679 {
		t.Errorf("daemon port = %d, want default 45679", cfg.Daemon.Port)
	}
}

func TestDaemonExplicitlyDisabled(t *testing.T) {
	cfg := writeAndLoad(t, `
[obsidian]
vault_path = "/tmp/vault"

[daemon]
enabled = false
port = 5000
`)
	if cfg.Daemon.IsEnabled() {
		t.Error("enabled = false should disable the daemon")
	}
	if cfg.Daemon.Port != 5000 {
		t.Errorf("daemon port = %d, want 5000", cfg.Daemon.Port)
	}
}

func TestResolvedWorkingDir(t *testing.T) {
	cases := []struct {
		name     string
		workDir  string
		repoPath string
		want     string
	}{
		{"empty falls back to repo", "", "/tmp/app", "/tmp/app"},
		{"absolute used as-is", "/opt/elsewhere", "/tmp/app", "/opt/elsewhere"},
		{"relative joins the repo", "web", "/tmp/app", "/tmp/app/web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ProcessConfig{WorkingDir: tc.workDir}
			if got := p.ResolvedWorkingDir(tc.repoPath); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestRepoAndProcessLookup(t *testing.T) {
	cfg := writeAndLoad(t, `
[obsidian]
vault_path = "/tmp/vault"

[[repos]]
path = "/tmp/my-app"
label = "my-app"

  [[repos.processes]]
  name = "dev"
  command = "npm run dev"
`)
	repo, ok := cfg.Repo("my-app")
	if !ok || repo.Path != "/tmp/my-app" {
		t.Fatalf("Repo lookup failed: %+v %v", repo, ok)
	}
	if _, ok := cfg.Repo("ghost"); ok {
		t.Error("unknown label should not resolve")
	}
	proc, ok := repo.Process("dev")
	if !ok || proc.Command != "npm run dev" {
		t.Fatalf("Process lookup failed: %+v %v", proc, ok)
	}
	if _, ok := repo.Process("ghost"); ok {
		t.Error("unknown process should not resolve")
	}
}

func TestProcessWorkingDirTildeExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	cfg := writeAndLoad(t, `
[obsidian]
vault_path = "/tmp/vault"

[[repos]]
path = "/tmp/my-app"
label = "my-app"

  [[repos.processes]]
  name = "dev"
  command = "x"
  working_dir = "~/elsewhere"
`)
	want := filepath.Join(home, "elsewhere")
	if got := cfg.Repos[0].Processes[0].ResolvedWorkingDir("/tmp/my-app"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDirIsTheParentOfTheConfigFile(t *testing.T) {
	// The status key lives beside config.toml, and both the hook and the
	// daemon resolve it independently. They must agree on where that is.
	dir := Dir()
	if dir == "" {
		t.Fatal("config dir must resolve")
	}
	if got := DefaultConfigPath(); filepath.Dir(got) != dir {
		t.Errorf("config path %q is not inside dir %q", got, dir)
	}
}
