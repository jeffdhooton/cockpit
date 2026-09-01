package sources

import (
	"sort"
	"strconv"
	"strings"

	"github.com/jhoot/cockpit/config"
)

// windowFormat is the list-windows format string ParseWindows expects.
// fieldSep separates format fields. It must be printable: tmux replaces
// non-printable characters such as tab with "_" whenever the environment has
// no UTF-8 locale, and a launch agent inherits no locale at all. A tab
// separator therefore parsed fine from a shell and silently produced nothing
// at login.
//
// Names may legitimately contain this character, so the parsers anchor on the
// fixed numeric fields around the name rather than on the field count.
const fieldSep = "|"

const windowFormat = "#{window_index}|#{window_name}|#{pane_dead}|#{pane_pid}|#{window_active}|#{pane_dead_status}"

// sessionFormat is the list-sessions format string parseTmuxOutput expects.
const sessionFormat = "#{session_name}|#{session_windows}|#{session_attached}|#{session_last_attached}"

// Window is one tmux window in a session.
type Window struct {
	Index   int
	Name    string
	Dead    bool
	PanePID int
	Active  bool
	// DeadStatus is the exit status of a dead pane's command. It is what turns
	// "it died" into "it could not find the command".
	DeadStatus int
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

// SelectFirstWindowArgs builds the argv for focusing a session's first window
// — the user's shell. It asks tmux for the lowest-numbered window rather than
// index 0, because base-index 1 is a common setting and there is no window 0
// under it.
func SelectFirstWindowArgs(session string) []string {
	return []string{"select-window", "-t", session + ":{start}"}
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
		parts := strings.Split(strings.TrimSpace(line), fieldSep)
		if len(parts) < 5 {
			continue
		}

		// Layout is index | name | dead | pid | active | dead_status, and the
		// name may contain the separator. Anchor on the fixed fields at each
		// end and treat everything between as the name.
		trailing := 4
		if len(parts) == 5 {
			// Older tmux without pane_dead_status.
			trailing = 3
		}
		nameEnd := len(parts) - trailing
		if nameEnd < 1 {
			continue
		}

		index, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(parts[nameEnd+1])
		deadStatus := 0
		if trailing == 4 {
			deadStatus, _ = strconv.Atoi(parts[nameEnd+3])
		}

		windows = append(windows, Window{
			Index:      index,
			Name:       strings.Join(parts[1:nameEnd], fieldSep),
			Dead:       parts[nameEnd] == "1",
			PanePID:    pid,
			Active:     parts[nameEnd+2] == "1",
			DeadStatus: deadStatus,
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
