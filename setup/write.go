package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

// ErrChanged means the file changed under the wizard between preview and
// write. The caller shows the new diff and asks again; nothing is written.
var ErrChanged = errors.New("the config changed since it was reviewed")

// ErrUnsafeMerge means the existing file could not be merged with
// confidence. The caller shows the snippet and leaves the file untouched.
var ErrUnsafeMerge = errors.New("cannot merge into the existing config safely")

// Fingerprint hashes file content; empty for a missing file.
func Fingerprint(raw []byte) string {
	if raw == nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Entry is one [[repos]] block to write.
type Entry struct {
	Label string
	Path  string
}

// ValidateEntries checks label grammar, uniqueness among the new entries and
// against existing labels, and the reserved session name.
func ValidateEntries(entries []Entry, existing []string, sessionName string) error {
	seen := map[string]bool{}
	for _, l := range existing {
		seen[l] = true
	}
	for _, e := range entries {
		switch {
		case e.Label == "":
			return fmt.Errorf("a label is required for %s", e.Path)
		case !labelPattern(e.Label):
			return fmt.Errorf("label %q must be letters, digits, hyphens or underscores", e.Label)
		case e.Label == sessionName:
			return fmt.Errorf("label %q is reserved for cockpit's own session", e.Label)
		case seen[e.Label]:
			return fmt.Errorf("label %q is already in use", e.Label)
		case e.Path == "":
			return fmt.Errorf("a path is required for %s", e.Label)
		}
		seen[e.Label] = true
	}
	return nil
}

func labelPattern(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}

// Snippet renders the [[repos]] blocks.
func Snippet(entries []Entry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "\n[[repos]]\npath = %q\nlabel = %q\n", config.CollapseTilde(e.Path), e.Label)
	}
	return b.String()
}

// MinimalConfig is a fresh config: the grid, refresh defaults, the selected
// repos, optional integrations disabled, examples commented out. No vault
// path, no Hermes URL, no process command is active.
func MinimalConfig(entries []Entry) string {
	var b strings.Builder
	b.WriteString(`# Cockpit configuration — created by cockpit init
# Cockpit uses your existing tmux. Optional integrations are off; enable
# them when you want them. Run cockpit doctor to check this file.

[general]
# Name of the tmux session cockpit runs in
session_name = "cockpit"
# How often to refresh local sources (tmux, git) in seconds
refresh_interval = 5
# Startup view: "grid" or "dashboard"
default_view = "grid"
`)
	if len(entries) > 0 {
		b.WriteString("\n# Projects. Each [[repos]] entry is one repository; the label becomes the\n# tmux session name.\n")
		b.WriteString(Snippet(entries))
	} else {
		b.WriteString(`
# Projects. Add one [[repos]] entry per repository; the label becomes the
# tmux session name. Existing tmux sessions show on the grid without any.
# [[repos]]
# path = "~/workspace/my-project"
# label = "my-project"
`)
	}
	b.WriteString(`
# Background processes a project declares start as tmux windows when you
# enter it. Uncomment inside a [[repos]] entry to use.
#   [[repos.processes]]
#   name = "dev"
#   command = "npm run dev"
#   auto_start = true

[signals]
stale_session_threshold = "24h"
show_stale_sessions = true
show_unpushed = true
show_failing_ci = true

# Optional: Obsidian task files (absolute paths). Leave out on a machine
# with no vault.
# [obsidian]
# today_file = "~/vault/today.md"
# inbox_file = "~/vault/inbox.md"

# Optional: GitHub PR and CI status via the gh CLI.
[github]
enabled = false
refresh_interval = 60

# Optional: the local tool server for coding agents (cockpit daemon start).
[daemon]
enabled = false
port = 45679

# Optional: a machine reached over ssh, and repos on it.
# [[hosts]]
# name = "mini"
# tmux = "/opt/homebrew/bin/tmux"
#
# [[repos]]
# host = "mini"
# path = "~/workspace/docket"
# label = "docket"

# Optional: a Hermes dashboard.
# [[hermes]]
# label = "hermes"
# url = "http://127.0.0.1:9119"
`)
	return b.String()
}

// Merge inserts the entries into an existing document as text, before the
// first of [github]/[signals]/[daemon]/[[hosts]]/[[hermes]] so [[repos]]
// blocks stay contiguous, preserving every other line and comment. It then
// proves the result parses and validates; if it does not, ErrUnsafeMerge.
func Merge(existing []byte, entries []Entry) ([]byte, error) {
	content := string(existing)
	block := Snippet(entries)
	insert := -1
	for _, marker := range []string{"\n[github]", "\n[signals]", "\n[daemon]", "\n[[hosts]]", "\n[[hermes]]"} {
		if idx := strings.Index(content, marker); idx >= 0 && (insert < 0 || idx < insert) {
			insert = idx
		}
	}
	var out string
	if insert >= 0 {
		out = content[:insert] + block + content[insert:]
	} else {
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		out = content + block
	}
	cfg, _, err := config.Parse([]byte(out))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsafeMerge, err)
	}
	// Every requested entry must come back, unchanged.
	for _, e := range entries {
		if _, ok := cfg.Repo(e.Label); !ok {
			return nil, fmt.Errorf("%w: entry %q did not survive the merge", ErrUnsafeMerge, e.Label)
		}
	}
	return []byte(out), nil
}

// Diff renders a small unified-style diff of two documents.
func Diff(before, after []byte) string {
	a := strings.Split(strings.TrimRight(string(before), "\n"), "\n")
	b := strings.Split(strings.TrimRight(string(after), "\n"), "\n")
	if len(before) == 0 {
		a = nil
	}
	// Longest common subsequence over lines; documents are small.
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out bytes.Buffer
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			fmt.Fprintf(&out, "  %s\n", a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			fmt.Fprintf(&out, "- %s\n", a[i])
			i++
		default:
			fmt.Fprintf(&out, "+ %s\n", b[j])
			j++
		}
	}
	for ; i < n; i++ {
		fmt.Fprintf(&out, "- %s\n", a[i])
	}
	for ; j < m; j++ {
		fmt.Fprintf(&out, "+ %s\n", b[j])
	}
	return out.String()
}

// WriteAtomic writes content to path via a temp file and rename, after
// re-reading the current content and checking it still matches the reviewed
// fingerprint. An existing file is backed up first.
func WriteAtomic(path string, content []byte, reviewed string, now time.Time) (backup string, err error) {
	current, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if Fingerprint(current) != reviewed {
			return "", ErrChanged
		}
	case errors.Is(rerr, fs.ErrNotExist):
		if reviewed != "" {
			return "", ErrChanged
		}
	default:
		return "", rerr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if current != nil {
		backup = fmt.Sprintf("%s.bak-%s", path, now.Format("20060102-150405"))
		if err := os.WriteFile(backup, current, 0o600); err != nil {
			return "", err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return backup, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return backup, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return backup, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return backup, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return backup, err
	}
	return backup, nil
}

// ExistingLabels lists the labels a document already declares.
func ExistingLabels(raw []byte) []string {
	cfg, _, err := config.Parse(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range cfg.Repos {
		out = append(out, r.Label)
	}
	sort.Strings(out)
	return out
}
