package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
	"github.com/spf13/cobra"
)

// hookMarker is the substring that identifies a cockpit hook in either
// engine's config. Idempotency keys on it: an entry that already carries it
// is left alone rather than duplicated.
const hookMarker = " hook status"

// claudeEvents and codexEvents are the events each engine's table maps. The
// installer writes exactly these; anything else would fire a hook the daemon
// ignores.
var (
	claudeEvents = []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop"}
	codexEvents  = []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop"}
)

// installReport says what install found after writing, because writing the
// config is not the same as installing the hook.
type installReport struct {
	Path    string
	Added   int  // entries written this run; 0 means already present
	Trusted bool // Codex only: a trust entry exists for at least one hook
	Skipped bool // the engine's config directory does not exist: it is not installed here
}

// engineAbsent reports whether an engine has never run on this machine. Its
// config directory is created on first launch, so a missing one means there
// is nothing to install into.
func engineAbsent(path string) bool {
	_, err := os.Stat(filepath.Dir(path))
	return errors.Is(err, fs.ErrNotExist)
}

var hookInstallHost string

var hookInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the status hook into Claude Code and Codex",
	Long: `Install the status hook into Claude Code and Codex on this machine, or,
with --host, on a configured remote host that has cockpit installed. The
remote install runs that machine's own cockpit, so its hooks post to its own
daemon and its status lands in its own tmux, which cockpit here already reads.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if hookInstallHost != "" {
			return runRemoteInstall(cmd, hookInstallHost)
		}
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(bin); err == nil {
			bin = resolved
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		claude, err := installClaudeHooks(filepath.Join(home, ".claude", "settings.json"), bin)
		if err != nil {
			return fmt.Errorf("claude: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "claude  %s\n", describe(claude))

		codex, err := installCodexHooks(filepath.Join(home, ".codex", "config.toml"), bin)
		if err != nil {
			return fmt.Errorf("codex: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "codex   %s\n", describe(codex))
		if !codex.Skipped && !codex.Trusted {
			fmt.Fprint(cmd.OutOrStdout(), `
Codex hooks land untrusted and do not fire until you approve them. Trust is
pinned to a hash of the hook configuration, so editing the command untrusts
it again. Run codex and approve the cockpit hooks, or their status will stay
inferred.
`)
		}
		return nil
	},
}

// runRemoteInstall runs `cockpit hook install` on a remote host over ssh and
// relays its output verbatim, trust caveat included.
func runRemoteInstall(cmd *cobra.Command, name string) error {
	cfg, err := config.Load(getConfigPath())
	if err != nil {
		return err
	}
	host, ok := cfg.Host(name)
	if !ok {
		return fmt.Errorf("host %q is not declared under [[hosts]]", name)
	}
	script, err := remoteInstallScript(host)
	if err != nil {
		return err
	}
	r := sources.SSHRunner{Host: host.Name, Tmux: host.Tmux, Timeout: 30 * time.Second}
	out, err := r.RunShell(cmd.Context(), script)
	fmt.Fprint(cmd.OutOrStdout(), out)
	if err != nil {
		return fmt.Errorf("%s: %w", host.Name, err)
	}
	return nil
}

// remoteInstallScript is the shell command that runs the remote binary's own
// installer. A host with no cockpit path cannot report status, and the
// message says exactly what to add.
func remoteInstallScript(host config.HostConfig) (string, error) {
	if host.Cockpit == "" {
		return "", fmt.Errorf("host %q has no cockpit path: install cockpit there and set cockpit = \"~/.local/bin/cockpit\" under [[hosts]]", host.Name)
	}
	return sources.QuoteRemotePath(host.Cockpit) + " 'hook' 'install'", nil
}

func describe(r installReport) string {
	switch {
	case r.Skipped:
		return "not installed on this machine, skipped"
	case r.Added == 0:
		return r.Path + "  already installed"
	default:
		return fmt.Sprintf("%s  added %d hooks", r.Path, r.Added)
	}
}

// backup copies path aside with a timestamp, the way register-mcp.sh does. A
// missing file needs no backup.
func backup(path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	return os.WriteFile(fmt.Sprintf("%s.bak-%s", path, stamp), raw, 0o600)
}

// installClaudeHooks merges the status hook into ~/.claude/settings.json.
//
// The file is a JSON object that may hold other hooks, and the shape is a
// map of event name to a list of matcher groups, each with its own hooks
// list. One new group per event is appended beside whatever is there.
func installClaudeHooks(path, bin string) (installReport, error) {
	report := installReport{Path: path}
	if engineAbsent(path) {
		report.Skipped = true
		return report, nil
	}

	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First run on a fresh machine: start from an empty object.
	case err != nil:
		return report, err
	default:
		// A file that does not parse might be mid-edit or hand-broken.
		// Rewriting it from an empty map would discard everything in it.
		if err := json.Unmarshal(raw, &settings); err != nil {
			return report, fmt.Errorf("%s does not parse, leaving it alone: %w", path, err)
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	command := bin + hookMarker

	for _, event := range claudeEvents {
		groups, _ := hooks[event].([]any)
		if claudeHasHook(groups, command) {
			continue
		}
		groups = append(groups, map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": command,
				"timeout": 5,
			}},
		})
		hooks[event] = groups
		report.Added++
	}
	if report.Added == 0 {
		return report, nil
	}
	settings["hooks"] = hooks

	if err := backup(path); err != nil {
		return report, err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return report, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return report, err
	}
	return report, os.WriteFile(path, append(out, '\n'), 0o600)
}

func claudeHasHook(groups []any, command string) bool {
	for _, g := range groups {
		group, _ := g.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			if c, _ := entry["command"].(string); c == command {
				return true
			}
		}
	}
	return false
}

// installCodexHooks appends the status hook to ~/.codex/config.toml.
//
// The file is edited as text, never decoded and re-encoded: it carries
// comments and is partly managed by another tool, and a round trip through a
// TOML encoder would flatten both. The block is placed before any [tui]
// header, because a shell wrapper on this machine deletes everything from
// that header to the end of the file on every run.
func installCodexHooks(path, bin string) (installReport, error) {
	report := installReport{Path: path}
	if engineAbsent(path) {
		report.Skipped = true
		return report, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return report, err
	}
	command := bin + hookMarker + " --engine codex"

	if !strings.Contains(string(raw), command) {
		var block bytes.Buffer
		block.WriteString("\n# cockpit status hooks, added by `cockpit hook install`\n")
		for _, event := range codexEvents {
			fmt.Fprintf(&block, "[[hooks.%s]]\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %q\ntimeout = 2\nasync = true\n\n",
				event, event, command)
		}
		report.Added = len(codexEvents)

		content := string(raw)
		if idx := strings.Index(content, "\n[tui]"); idx >= 0 {
			content = content[:idx+1] + block.String() + content[idx+1:]
		} else if strings.HasPrefix(content, "[tui]") {
			content = block.String() + content
		} else {
			content += block.String()
		}

		// Prove the result parses before it replaces the user's file. An
		// installer that leaves Codex unable to start is worse than no hook.
		var check map[string]any
		if _, err := toml.Decode(content, &check); err != nil {
			return report, fmt.Errorf("the merged config would not parse, leaving %s alone: %w", path, err)
		}

		if err := backup(path); err != nil {
			return report, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return report, err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return report, err
		}
		raw = []byte(content)
	}

	report.Trusted = codexTrusts(raw)
	return report, nil
}

// codexTrusts reports whether Codex has recorded a trust entry for any of the
// events cockpit installs. Trust lives under [hooks.state."<file>:<event>:i:j"]
// with a trusted_hash, and is granted inside Codex rather than by editing the
// file, so this is read-only.
func codexTrusts(raw []byte) bool {
	var parsed struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
			} `toml:"state"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(string(raw), &parsed); err != nil {
		return false
	}
	for key, state := range parsed.Hooks.State {
		if state.TrustedHash == "" {
			continue
		}
		for _, event := range codexEvents {
			if strings.Contains(key, ":"+snake(event)+":") {
				return true
			}
		}
	}
	return false
}

// snake renders a hook event name the way Codex keys its trust state:
// PermissionRequest becomes permission_request.
func snake(event string) string {
	var b strings.Builder
	for i, r := range event {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r | 0x20)
	}
	return b.String()
}
