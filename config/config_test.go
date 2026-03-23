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
