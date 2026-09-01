package sources

import (
	"sort"
	"strconv"
	"strings"

	"github.com/jhoot/cockpit/config"
)

// windowFormat is the list-windows format string ParseWindows expects.
const windowFormat = "#{window_index}\t#{window_name}\t#{pane_dead}\t#{pane_pid}\t#{window_active}"

// sessionFormat is the list-sessions format string parseTmuxOutput expects.
const sessionFormat = "#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_last_attached}"

// Window is one tmux window in a session.
type Window struct {
	Index   int
	Name    string
	Dead    bool
	PanePID int
	Active  bool
}

// Target builds a tmux target string for a window inside a session.
func Target(session, window string) string {
	return session + ":" + window
}

// NewSessionArgs builds the argv for creating a detached session whose first
// window is a plain shell at dir.
func NewSessionArgs(session, dir string) []string {
	return []string{"new-session", "-d", "-s", session, "-c", dir}
}

// NewWindowArgs builds the argv for launching a process as its own window.
func NewWindowArgs(session string, p config.ProcessConfig, repoPath string) []string {
	args := []string{"new-window", "-d", "-t", session + ":", "-n", p.Name,
		"-c", p.ResolvedWorkingDir(repoPath)}
	args = append(args, envArgs(p.Env)...)
	return append(args, p.Command)
}

// RespawnWindowArgs builds the argv for restarting a process in the window it
// already occupies, killing whatever is there first.
func RespawnWindowArgs(session, window string, p config.ProcessConfig, repoPath string) []string {
	args := []string{"respawn-window", "-k", "-t", Target(session, window),
		"-c", p.ResolvedWorkingDir(repoPath)}
	args = append(args, envArgs(p.Env)...)
	return append(args, p.Command)
}

// KillWindowArgs builds the argv for removing a window entirely.
func KillWindowArgs(session, window string) []string {
	return []string{"kill-window", "-t", Target(session, window)}
}

// RemainOnExitArgs builds the argv that keeps a window's dead pane readable
// after its command exits, so a crash leaves an error you can still see.
func RemainOnExitArgs(session, window string) []string {
	return []string{"set-window-option", "-t", Target(session, window), "remain-on-exit", "on"}
}

// SelectWindowArgs builds the argv for focusing a window by index.
func SelectWindowArgs(session string, index int) []string {
	return []string{"select-window", "-t", session + ":" + strconv.Itoa(index)}
}

// ListWindowsArgs builds the argv for listing a session's windows in the
// format ParseWindows reads.
func ListWindowsArgs(session string) []string {
	return []string{"list-windows", "-t", session, "-F", windowFormat}
}

// ListSessionsArgs builds the argv for listing every session in the format
// parseTmuxOutput reads.
func ListSessionsArgs() []string {
	return []string{"list-sessions", "-F", sessionFormat}
}

// HasSessionArgs builds the argv for testing whether a session exists.
func HasSessionArgs(session string) []string {
	return []string{"has-session", "-t", session}
}

// CapturePaneArgs builds the argv for reading a pane's contents. A lines count
// above zero reaches back into scrollback; zero captures only what is visible.
func CapturePaneArgs(target string, lines int) []string {
	args := []string{"capture-pane", "-p", "-t", target}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	return args
}

// SendKeysLiteralArgs builds the argv for typing text into a pane verbatim,
// without interpreting it as key names.
func SendKeysLiteralArgs(target, text string) []string {
	return []string{"send-keys", "-t", target, "-l", text}
}

// SendKeysEnterArgs builds the argv for pressing Enter in a pane.
func SendKeysEnterArgs(target string) []string {
	return []string{"send-keys", "-t", target, "Enter"}
}

// ParseWindows reads the tab-delimited output of a list-windows call.
func ParseWindows(out string) []Window {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}

	var windows []Window
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) < 5 {
			continue
		}
		index, _ := strconv.Atoi(parts[0])
		pid, _ := strconv.Atoi(parts[3])
		windows = append(windows, Window{
			Index:   index,
			Name:    parts[1],
			Dead:    parts[2] == "1",
			PanePID: pid,
			Active:  parts[4] == "1",
		})
	}
	return windows
}

// envArgs renders an env map as sorted -e flags so argv is deterministic.
func envArgs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	return args
}
