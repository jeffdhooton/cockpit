package sources

import (
	"sort"
	"strconv"
	"strings"

	"github.com/jeffdhooton/cockpit/config"
)

// fieldSep separates format fields. It must be printable: tmux replaces
// non-printable characters such as tab with "_" whenever the environment has
// no UTF-8 locale, and a launch agent inherits no locale at all. A tab
// separator therefore parsed fine from a shell and silently produced nothing
// at login.
//
// Names may legitimately contain this character, so the parsers anchor on the
// fixed numeric fields around the name rather than on the field count.
const fieldSep = "|"

// windowFormat is the list-windows format string ParseWindows expects. The
// name sits second, as it always has, and the identity fields ride at the end
// so the parser can anchor on a fixed tail whatever the name contains.
const windowFormat = "#{window_index}|#{window_name}|#{pane_dead}|#{pane_pid}|#{window_active}|#{pane_dead_status}" +
	"|#{window_id}|#{pane_id}|#{window_panes}|#{@cockpit_managed}|#{pid}|#{start_time}"

// windowTrailingFields is how many fixed fields follow the name in
// windowFormat. Older fixtures carry three or four; those still parse, with
// no identity.
const windowTrailingFields = 10

// sessionFormat is the list-sessions format string parseTmuxOutput expects.
//
// The three @cockpit_* fields ride along on a call the TUI already makes every
// tick, which is what lets the per-session capture-pane loop go away rather
// than having anything added to it. tmux reports an option that was never set
// as empty rather than failing, so a session that has never reported still
// produces a well-formed line.
const sessionFormat = "#{session_name}|#{session_windows}|#{session_attached}|#{session_last_attached}|#{@cockpit_status}|#{@cockpit_status_at}|#{@cockpit_status_window}|#{@cockpit_view_of}" +
	"|#{session_id}|#{pid}|#{start_time}"

const (
	sessionTrailingFields       = 10
	legacySessionTrailingFields = 7
)

// managedOption marks a window cockpit launched for a configured process. Its
// value is the process name. A window that merely shares a process's name is
// not proof that its contents are that process; the mark is.
const managedOption = "@cockpit_managed"

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
	// HasDeadStatus is false on a tmux too old to report pane_dead_status, or
	// for a live pane. An exit code that was never observed is unknown, not
	// zero.
	HasDeadStatus bool
	// ID is the window's own identifier (@N) and PaneID the active pane's
	// (%N). Both survive renames and index changes for the life of the
	// server; Generation says which server issued them.
	ID         string
	PaneID     string
	Generation string
	// Panes counts the panes in the window. A configured process occupies
	// exactly one; more means someone split it and ownership is uncertain.
	Panes int
	// Managed is the process name cockpit marked this window with when it
	// launched it, or empty for a window it did not create.
	Managed string
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

// endTarget addresses the highest-numbered window of a session. A window
// appended after it with -a becomes the new {end}, which is how the marker
// commands chained onto new-window reach the window just created rather than
// the first one that happens to share its name.
func endTarget(session string) string {
	return session + ":{end}"
}

// NewWindowArgs builds the argv for launching a process as its own window,
// with remain-on-exit and the managed mark chained into the same tmux
// invocation.
//
// The chaining matters. A command that fails instantly — a missing binary, a
// bad flag — can exit before a follow-up set-window-option arrives, and tmux
// then closes the window and takes the error message with it. One invocation
// removes the round trip that loses the evidence. The window is appended
// after {end} so the chained commands can address it by position; a name
// would resolve to whichever window matched first.
func NewWindowArgs(session string, p config.ProcessConfig, repoPath string) []string {
	args := []string{"new-window", "-d", "-a", "-t", endTarget(session), "-n", p.Name,
		"-c", p.ResolvedWorkingDir(repoPath)}
	args = append(args, envArgs(p.Env)...)
	args = append(args, p.Command, ";")
	args = append(args, "set-window-option", "-t", endTarget(session), "remain-on-exit", "on", ";")
	return append(args, "set-window-option", "-t", endTarget(session), managedOption, p.Name)
}

// RemainOnExitFailedArgs builds the argv that keeps only *failed* commands
// readable, for a session cockpit created. Setting it before any process
// window exists removes the race entirely, and "failed" rather than "on" means
// the user's own shell still closes normally when they type exit.
func RemainOnExitFailedArgs(session string) []string {
	return []string{"set-option", "-t", session, "remain-on-exit", "failed"}
}

// RespawnWindowArgs builds the argv for restarting a process in the window it
// already occupies, killing whatever is there first. The window is addressed
// by name for compatibility; RespawnWindowByIDArgs is the safe form.
func RespawnWindowArgs(session, window string, p config.ProcessConfig, repoPath string) []string {
	return respawnArgs(Target(session, window), p, repoPath)
}

// RespawnWindowByIDArgs restarts a process in a window addressed by its
// tmux identifier, so a renamed or reordered window cannot redirect the kill.
func RespawnWindowByIDArgs(windowID string, p config.ProcessConfig, repoPath string) []string {
	return respawnArgs(windowID, p, repoPath)
}

func respawnArgs(target string, p config.ProcessConfig, repoPath string) []string {
	args := []string{"respawn-window", "-k", "-t", target, "-c", p.ResolvedWorkingDir(repoPath)}
	args = append(args, envArgs(p.Env)...)
	args = append(args, p.Command, ";")
	args = append(args, "set-window-option", "-t", target, "remain-on-exit", "on", ";")
	return append(args, "set-window-option", "-t", target, managedOption, p.Name)
}

// KillWindowArgs builds the argv for removing a window entirely.
func KillWindowArgs(session, window string) []string {
	return []string{"kill-window", "-t", Target(session, window)}
}

// KillWindowByIDArgs removes a window addressed by its tmux identifier.
func KillWindowByIDArgs(windowID string) []string {
	return []string{"kill-window", "-t", windowID}
}

// MarkManagedArgs records the managed mark on an existing window. It is what
// adoption writes, and nothing else: no command runs.
func MarkManagedArgs(windowID, process string) []string {
	return []string{"set-window-option", "-t", windowID, managedOption, process, ";",
		"set-window-option", "-t", windowID, "remain-on-exit", "on"}
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

// CapturePaneVisibleArgs reads only what a pane currently shows: no
// scrollback, which is what a preview is and all a preview may cost.
func CapturePaneVisibleArgs(paneID string) []string {
	return []string{"capture-pane", "-p", "-t", paneID}
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

// ShowSessionOptionsArgs lists every option set on a session, which is how
// the per-process stop overrides are read back.
func ShowSessionOptionsArgs(session string) []string {
	return []string{"show-options", "-t", session}
}

// ParseWindows reads the delimited output of a list-windows call.
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

		// Layout is index | name | dead | pid | active | dead_status |
		// window_id | pane_id | panes | managed | server pid | start_time,
		// and the name may contain the separator. Anchor on the fixed fields
		// at each end and treat everything between as the name. Shorter
		// lines are older fixtures: the four-field tail of a tmux that knew
		// pane_dead_status, or the three-field tail of one that did not.
		trailing := windowTrailingFields
		switch {
		case len(parts) >= windowTrailingFields+2:
		case len(parts) == 5:
			trailing = 3
		default:
			trailing = 4
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
		w := Window{
			Index:   index,
			Name:    strings.Join(parts[1:nameEnd], fieldSep),
			Dead:    parts[nameEnd] == "1",
			PanePID: pid,
			Active:  parts[nameEnd+2] == "1",
			Panes:   1,
		}
		if trailing >= 4 && w.Dead {
			// tmux reports pane_dead_status only once the pane has died, and
			// as empty before then. An empty field on a dead pane is a code
			// that was never observed, which is not the same as zero.
			if code, err := strconv.Atoi(parts[nameEnd+3]); err == nil {
				w.DeadStatus = code
				w.HasDeadStatus = true
			}
		}
		if trailing == windowTrailingFields {
			w.ID = parts[nameEnd+4]
			w.PaneID = parts[nameEnd+5]
			if n, err := strconv.Atoi(parts[nameEnd+6]); err == nil && n > 0 {
				w.Panes = n
			}
			w.Managed = parts[nameEnd+7]
			w.Generation = serverGeneration(parts[nameEnd+8], parts[nameEnd+9])
		}
		windows = append(windows, w)
	}
	return windows
}

// serverGeneration identifies one tmux server instance from its pid and the
// time it started. Pane and window ids restart from zero on a new server, so
// an id is only meaningful alongside the generation that issued it.
func serverGeneration(pid, startTime string) string {
	if pid == "" {
		return ""
	}
	if startTime == "" {
		return pid
	}
	return pid + "-" + startTime
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
