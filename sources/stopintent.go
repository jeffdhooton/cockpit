package sources

import (
	"strconv"
	"strings"
	"time"
)

// A stop override records that the user stopped a configured process on
// purpose. It lives on the tmux session as a user option keyed by the process
// name, so it lasts exactly as long as the session, survives every cockpit
// restart, and is visible to every client of that server — including one
// reading over ssh. It holds no command and no secret: a version tag and the
// time it was set.
//
// Reconciliation skips an overridden process. Start clears the override as
// part of its launch, and Restart never leaves one behind. A session that is
// removed, a server that restarts, or a machine that reboots takes the
// override with it; auto_start in config remains the permanent policy.

const (
	stopOptionPrefix  = "@cockpit_stopped_"
	stopOptionVersion = "v1"
)

// StopOverride is one recorded stop intent.
type StopOverride struct {
	Process string
	Since   time.Time
	Version string
}

// StopOptionName derives the option key for a process. Names are already
// constrained to [A-Za-z0-9_-], which tmux accepts in an option name as is.
func StopOptionName(process string) string {
	return stopOptionPrefix + process
}

// SetStopOverrideArgs records the override on a session.
func SetStopOverrideArgs(session, process string, now time.Time) []string {
	return []string{"set-option", "-t", session, StopOptionName(process),
		stopOptionVersion + ":" + strconv.FormatInt(now.Unix(), 10)}
}

// ClearStopOverrideArgs removes the override. -q so a session that never had
// one does not error.
func ClearStopOverrideArgs(session, process string) []string {
	return []string{"set-option", "-q", "-u", "-t", session, StopOptionName(process)}
}

// ParseStopOverrides reads show-options output for the overrides it holds,
// keyed by process name. Unknown versions are kept, so a newer client's
// intent is still honoured as "stopped" rather than silently dropped.
func ParseStopOverrides(out string) map[string]StopOverride {
	overrides := map[string]StopOverride{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, stopOptionPrefix) {
			continue
		}
		name, value, _ := strings.Cut(strings.TrimPrefix(line, stopOptionPrefix), " ")
		value = strings.Trim(value, "\"'")
		version, stamp, _ := strings.Cut(value, ":")
		o := StopOverride{Process: name, Version: version}
		if epoch, err := strconv.ParseInt(stamp, 10, 64); err == nil {
			o.Since = time.Unix(epoch, 0)
		}
		overrides[name] = o
	}
	return overrides
}
