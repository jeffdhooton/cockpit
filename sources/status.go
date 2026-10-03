package sources

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status lives in tmux itself rather than in the daemon. The lifetime is
// exactly right — it lasts as long as the session and dies with it, so there
// is no stale entry for a session that ended last Tuesday — and it survives a
// daemon restart, because the daemon was never the owner.
const (
	statusOption       = "@cockpit_status"
	statusAtOption     = "@cockpit_status_at"
	statusWindowOption = "@cockpit_status_window"

	// statusStaleAfter bounds how long a reported status is trusted. A crashed
	// agent stops sending events, and a "working" option would otherwise
	// persist forever.
	statusStaleAfter = 10 * time.Minute
)

// SetStatusArgs builds the argv that records a status on a session, alongside
// the time it arrived and the window that reported it.
//
// All three land in one tmux invocation. A follow-up call can arrive after the
// session is gone, which would leave a status with no timestamp beside it —
// and a status that cannot be aged out reads as permanently fresh.
func SetStatusArgs(session string, st AgentStatus, window string, now time.Time) []string {
	return []string{
		"set-option", "-t", session, statusOption, statusName(st), ";",
		"set-option", "-t", session, statusAtOption, strconv.FormatInt(now.Unix(), 10), ";",
		"set-option", "-t", session, statusWindowOption, window,
	}
}

// ClearStatusArgs builds the argv that removes a session's status, returning it
// to the inferred path.
func ClearStatusArgs(session string) []string {
	return []string{"set-option", "-t", session, "-u", statusOption}
}

// StatusFromOptions turns three raw tmux option values into a status, reporting
// whether it may be trusted as reported rather than inferred.
//
// It fails closed on every uncertainty: unset, unparseable, stale, inherited
// from a sibling window, or a name belonging to no engine's table. The display
// must never present a guess as a fact, and the only way to guarantee that is
// for this function to refuse anything it cannot vouch for.
func StatusFromOptions(raw, at, window, wantWindow string, now time.Time) (AgentStatus, bool) {
	st, ok := statusFromName(raw)
	if !ok {
		return AgentStatusUnknown, false
	}

	// Window options inherit from the session when unset, so a window with no
	// status of its own reports the session's. A recorded window that is not
	// this one means the value was inherited, not reported here.
	if window != "" && wantWindow != "" && window != wantWindow {
		return AgentStatusUnknown, false
	}

	epoch, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return AgentStatusUnknown, false
	}
	if now.Sub(time.Unix(epoch, 0)) > statusStaleAfter {
		return AgentStatusUnknown, false
	}
	return st, true
}

// statusName renders a status for storage. Unknown deliberately renders empty,
// which is also how tmux reports an option that was never set.
func statusName(st AgentStatus) string {
	switch st {
	case AgentStatusIdle:
		return "idle"
	case AgentStatusWorking:
		return "working"
	case AgentStatusNeedsInput:
		return "needs_input"
	default:
		return ""
	}
}

func statusFromName(s string) (AgentStatus, bool) {
	switch s {
	case "idle":
		return AgentStatusIdle, true
	case "working":
		return AgentStatusWorking, true
	case "needs_input":
		return AgentStatusNeedsInput, true
	default:
		return AgentStatusUnknown, false
	}
}

// StatusTarget is where a hook report lands. The pane form carries the
// identities a report must be bound to; the legacy form is a session and
// window name, which any hook in the session may overwrite.
type StatusTarget struct {
	Legacy     bool
	Session    string // legacy: session name
	Window     string // legacy: window name
	Generation string
	SessionID  string
	WindowID   string
	PaneID     string
}

var (
	sessionIDPattern = regexp.MustCompile(`^\$[0-9]+$`)
	windowIDPattern  = regexp.MustCompile(`^@[0-9]+$`)
	paneIDPattern    = regexp.MustCompile(`^%[0-9]+$`)
)

// PaneStatusTarget renders the pane form: pane:<generation>:<$sid>:<@wid>:<%pid>.
func PaneStatusTarget(generation, sessionID, windowID, paneID string) string {
	return "pane:" + generation + ":" + sessionID + ":" + windowID + ":" + paneID
}

// ParseStatusTarget reads either form. A string that is not a well-formed
// pane target is a legacy one.
func ParseStatusTarget(s string) StatusTarget {
	parts := strings.Split(s, ":")
	if len(parts) == 5 && parts[0] == "pane" && parts[1] != "" &&
		sessionIDPattern.MatchString(parts[2]) && windowIDPattern.MatchString(parts[3]) && paneIDPattern.MatchString(parts[4]) {
		return StatusTarget{Generation: parts[1], SessionID: parts[2], WindowID: parts[3], PaneID: parts[4]}
	}
	session, window, _ := strings.Cut(s, ":")
	return StatusTarget{Legacy: true, Session: session, Window: window}
}
