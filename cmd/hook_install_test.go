package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/jhoot/cockpit/config"
)

const bin = "/usr/local/bin/cockpit"

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	matches, _ := filepath.Glob(path + ".bak-*")
	return matches
}

// --- Claude Code: ~/.claude/settings.json ---

func TestInstallClaudePreservesExistingHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"scry hook pre-git"}]}]}}`)

	if _, err := installClaudeHooks(path, bin); err != nil {
		t.Fatal(err)
	}

	raw := read(t, path)
	if !strings.Contains(raw, "scry hook pre-git") {
		t.Error("an unrelated hook was destroyed")
	}
	if !strings.Contains(raw, `"model": "opus"`) {
		t.Error("an unrelated setting was destroyed")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("settings are no longer valid JSON: %v", err)
	}
	hooks := parsed["hooks"].(map[string]any)
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop"} {
		if !strings.Contains(mustJSON(hooks[event]), bin+" hook status") {
			t.Errorf("%s did not get the cockpit hook: %v", event, hooks[event])
		}
	}
	if groups := hooks["PreToolUse"].([]any); len(groups) != 2 {
		t.Errorf("PreToolUse should keep the scry group and gain one, got %d groups", len(groups))
	}
}

func TestInstallClaudeIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, `{}`)

	if _, err := installClaudeHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if _, err := installClaudeHooks(path, bin); err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(read(t, path), "hook status"); n != 4 {
		t.Errorf("want one entry per mapped event, got %d after two installs", n)
	}
}

func TestInstallClaudeCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	if _, err := installClaudeHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "hook status") {
		t.Error("a missing settings file should be created")
	}
}

func TestInstallClaudeWritesABackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, `{"a":1}`)

	if _, err := installClaudeHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if got := backups(t, path); len(got) != 1 {
		t.Errorf("want one backup, got %v", got)
	}
}

func TestInstallClaudeRefusesToClobberInvalidJSON(t *testing.T) {
	// A file that does not parse might be mid-edit or hand-broken. Rewriting
	// it from an empty map would silently discard everything in it.
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, `{"hooks": [broken`)

	if _, err := installClaudeHooks(path, bin); err == nil {
		t.Fatal("an unparseable settings file must be reported, not replaced")
	}
	if read(t, path) != `{"hooks": [broken` {
		t.Error("the broken file was modified")
	}
}

// --- Codex: ~/.codex/config.toml ---

func TestInstallCodexAppendsParseableHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "# managed by hand\nmodel = \"gpt-5\"\n\n[mcp_servers.cockpit]\nurl = \"http://127.0.0.1:45679/mcp\"\n")

	if _, err := installCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}

	raw := read(t, path)
	if !strings.HasPrefix(raw, "# managed by hand\n") {
		t.Error("the user's comments must survive: this file is edited in place, never re-encoded")
	}
	var parsed struct {
		Model string `toml:"model"`
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `toml:"type"`
				Command string `toml:"command"`
				Async   bool   `toml:"async"`
			} `toml:"hooks"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(raw, &parsed); err != nil {
		t.Fatalf("config no longer parses: %v\n%s", err, raw)
	}
	if parsed.Model != "gpt-5" {
		t.Error("an unrelated setting was lost")
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop"} {
		groups := parsed.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Errorf("%s: want one group with one hook, got %+v", event, groups)
			continue
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || !strings.Contains(h.Command, "--engine codex") || !h.Async {
			t.Errorf("%s hook = %+v", event, h)
		}
	}
}

func TestInstallCodexIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "model = \"gpt-5\"\n")

	if _, err := installCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if _, err := installCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(read(t, path), "--engine codex"); n != 4 {
		t.Errorf("want one entry per mapped event, got %d after two installs", n)
	}
}

func TestInstallCodexLandsBeforeTheTuiSection(t *testing.T) {
	// Jeff's shell wraps codex in a function that deletes everything from a
	// [tui] header to the end of the file on every run. A block appended
	// after it would be installed and then silently erased.
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "model = \"gpt-5\"\n\n[tui]\ntheme = \"dark\"\n")

	if _, err := installCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}

	raw := read(t, path)
	if strings.Index(raw, "--engine codex") > strings.Index(raw, "[tui]") {
		t.Errorf("hooks landed after [tui] and will be erased:\n%s", raw)
	}
	if !strings.Contains(raw, "theme = \"dark\"") {
		t.Error("the tui section was damaged")
	}
	var parsed map[string]any
	if _, err := toml.Decode(raw, &parsed); err != nil {
		t.Fatalf("config no longer parses: %v", err)
	}
}

func TestInstallCodexWritesABackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "model = \"gpt-5\"\n")

	if _, err := installCodexHooks(path, bin); err != nil {
		t.Fatal(err)
	}
	if got := backups(t, path); len(got) != 1 {
		t.Errorf("want one backup, got %v", got)
	}
}

func TestInstallCodexReportsTrust(t *testing.T) {
	// Writing the config is not installing the hook. Codex leaves a new hook
	// untrusted until approved, so the report must say which it found.
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "model = \"gpt-5\"\n")

	report, err := installCodexHooks(path, bin)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trusted {
		t.Error("a freshly written hook cannot already be trusted")
	}

	// A recorded trust entry for one of our events flips the report.
	write(t, path, read(t, path)+"\n[hooks.state.\""+path+":stop:0:0\"]\nenabled = true\ntrusted_hash = \"sha256:abc\"\n")
	report, err = installCodexHooks(path, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Trusted {
		t.Error("a recorded trust entry should be reported as trusted")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRemoteInstallScriptRunsTheRemoteBinary(t *testing.T) {
	h := config.HostConfig{Name: "mini", Tmux: "/opt/homebrew/bin/tmux", Cockpit: "~/.local/bin/cockpit"}
	got, err := remoteInstallScript(h)
	if err != nil {
		t.Fatal(err)
	}
	if got != "~/'.local/bin/cockpit' 'hook' 'install'" {
		t.Errorf("script = %s", got)
	}
}

func TestRemoteInstallNeedsTheRemoteBinaryPath(t *testing.T) {
	h := config.HostConfig{Name: "mini", Tmux: "/opt/homebrew/bin/tmux"}
	if _, err := remoteInstallScript(h); err == nil || !strings.Contains(err.Error(), "cockpit") {
		t.Errorf("a host with no cockpit path must say so plainly, got %v", err)
	}
}

func TestInstallClaudeSkipsAMachineWithoutClaude(t *testing.T) {
	// No ~/.claude directory means Claude Code has never run here. Creating
	// its settings file for it would be tidy-looking clutter.
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")

	report, err := installClaudeHooks(path, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Skipped {
		t.Error("want the engine reported as absent")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("settings.json was created for an engine that is not installed")
	}
}

func TestInstallCodexSkipsAMachineWithoutCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".codex", "config.toml")

	report, err := installCodexHooks(path, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Skipped {
		t.Error("want the engine reported as absent")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("config.toml was created for an engine that is not installed")
	}
}
