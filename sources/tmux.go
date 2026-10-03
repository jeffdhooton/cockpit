package sources

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes a tmux command and returns its stdout. Everything that
// touches tmux goes through this, so callers can be tested without a server.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ErrTmuxNotFound means the tmux binary could not be located. It is
// deliberately distinct from "no tmux server is running": the first means we
// cannot know anything, the second is an answer.
var ErrTmuxNotFound = errors.New("tmux not found in PATH")

// ErrNoServer means tmux answered that no server is listening on the socket.
// It is a verified empty world: no sessions, no windows, nothing running. Any
// other failure to read is unknown state, and the two must never be confused,
// because "verified absent" is the only reading that may permit creation.
var ErrNoServer = errors.New("no tmux server running")

// ErrNoSession means tmux answered that the named session does not exist. Like
// ErrNoServer it is an answer rather than a failure to read.
var ErrNoSession = errors.New("no such tmux session")

// classifyTmuxMessage turns tmux's stderr into a sentinel where the message is
// one of the two verified absences. Everything else stays an opaque failure.
func classifyTmuxMessage(verb, msg string) error {
	switch {
	case noServerMessage(msg):
		return fmt.Errorf("tmux %s: %s: %w", verb, msg, ErrNoServer)
	case noSessionMessage(msg):
		return fmt.Errorf("tmux %s: %s: %w", verb, msg, ErrNoSession)
	}
	return fmt.Errorf("tmux %s: %s", verb, msg)
}

func noServerMessage(msg string) bool {
	return strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") && strings.Contains(msg, "No such file or directory"))
}

func noSessionMessage(msg string) bool {
	return strings.Contains(msg, "can't find session") || strings.Contains(msg, "no such session")
}

// IsNoServer reports whether an error is tmux saying no server is running —
// by sentinel from a classifying runner, or by message from one that passes
// tmux's text through unchanged, such as a fake or a remote shell.
func IsNoServer(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrNoServer) || noServerMessage(err.Error())
}

// IsNoSession reports whether an error is tmux saying the session does not
// exist. See IsNoServer for why the message is consulted.
func IsNoSession(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrNoSession) || noSessionMessage(err.Error())
}

// IsVerifiedAbsence reports whether an error is one of the two answers that
// prove a target does not exist, as opposed to a failure to look.
func IsVerifiedAbsence(err error) bool {
	return IsNoServer(err) || IsNoSession(err)
}

// ExecRunner runs tmux as a subprocess.
type ExecRunner struct {
	// Binary is the tmux executable, "tmux" when empty. Set it to an absolute
	// path where PATH is not trustworthy — a launch agent gets a bare
	// /usr/bin:/bin:/usr/sbin:/sbin that excludes Homebrew.
	Binary  string
	Timeout time.Duration
	// Socket names a private tmux server (-L). Empty means the default
	// server. Tests and isolated checks use it so nothing they do can touch
	// the user's sessions. NoConfig starts that server without the user's
	// tmux.conf, so base-index and friends cannot surprise a test.
	Socket   string
	NoConfig bool
}

// args prepends the socket selection, if any, to a tmux argv.
func (r ExecRunner) args(args []string) []string {
	if r.Socket == "" {
		return args
	}
	prefix := []string{"-L", r.Socket}
	if r.NoConfig {
		prefix = append(prefix, "-f", "/dev/null")
	}
	return append(prefix, args...)
}

func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	binary := r.Binary
	if binary == "" {
		binary = "tmux"
	}

	out, err := exec.CommandContext(ctx, binary, r.args(args)...).Output()
	if err != nil {
		verb := "tmux"
		if len(args) > 0 {
			verb = args[0]
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("tmux %s: %w", verb, ErrTmuxNotFound)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", classifyTmuxMessage(verb, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("tmux %s: %w", verb, err)
	}
	return string(out), nil
}

// ResolveTmux locates the tmux binary once, so callers can fail loudly at
// startup instead of silently reporting an empty world on every query.
func ResolveTmux() (string, error) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTmuxNotFound, err)
	}
	return path, nil
}

// DefaultRunner returns the Runner used outside tests.
func DefaultRunner() Runner { return ExecRunner{} }

// GetTmuxSessions returns all tmux sessions via the tmux CLI. No server
// running is an answer — no sessions — while any other failure is returned,
// because an unreadable server is not an empty one.
func GetTmuxSessions(ctx context.Context) ([]TmuxSession, error) {
	return ListSessions(ctx, DefaultRunner())
}

// parseTmuxOutput parses list-sessions output for one host. The clock is a
// parameter because a remote host's status timestamps are on its clock, and
// staleness must be judged against that one.
func parseTmuxOutput(output, host string, now time.Time) ([]TmuxSession, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil
	}

	var sessions []TmuxSession
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, fieldSep)
		if len(parts) < 8 {
			continue
		}

		// Layout is name | windows | attached | last_attached | status |
		// status_at | status_window | view_of | session_id | pid | start_time,
		// and the name may contain the separator, so anchor on the fixed
		// fields at the end and treat everything before them as the name. A
		// line with fewer fields came from an older format without the three
		// identity fields; it still parses, with no identity.
		trailing := sessionTrailingFields
		if len(parts) < sessionTrailingFields+1 {
			trailing = legacySessionTrailingFields
		}
		nameEnd := len(parts) - trailing
		if nameEnd < 1 {
			continue
		}
		windows, _ := strconv.Atoi(parts[nameEnd])
		attached := parts[nameEnd+1] == "1"
		epoch, _ := strconv.ParseInt(parts[nameEnd+2], 10, 64)
		lastUsed := time.Unix(epoch, 0)

		// The recorded window is passed as both the stored and the wanted
		// value: at session level the option is authoritative by definition,
		// and the inheritance check only matters when reading one window.
		reportedWindow := parts[nameEnd+5]
		status, reported := StatusFromOptions(
			parts[nameEnd+3], parts[nameEnd+4], reportedWindow, reportedWindow, now)

		sess := TmuxSession{
			Name:           strings.Join(parts[:nameEnd], fieldSep),
			Windows:        windows,
			Attached:       attached,
			LastUsed:       lastUsed,
			Status:         status,
			StatusReported: reported,
			Host:           host,
			ViewOf:         parts[nameEnd+6],
		}
		if reported {
			sess.StatusSource = StatusSourceSession
		}
		if trailing == sessionTrailingFields {
			sess.ID = parts[nameEnd+7]
			sess.Generation = serverGeneration(parts[nameEnd+8], parts[nameEnd+9])
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// AgentStatus represents the reported or inferred state of a coding agent
// running in a tmux session. Both Claude Code and Codex report into it, which
// is why it is not named for either.
type AgentStatus int

const (
	AgentStatusUnknown AgentStatus = iota
	AgentStatusIdle                // the turn ended
	AgentStatusWorking             // acting: a prompt landed or a tool started
	// AgentStatusNeedsInput is the state the pane-hash guess cannot see at
	// all. An agent blocked on a permission prompt looks exactly like an idle
	// one from outside, and it is the one worth walking across the room for.
	AgentStatusNeedsInput
)

// CapturePaneContent returns the full visible pane content for hashing/comparison.
// Lighter than CapturePane — no line limiting, just trims trailing blanks.
func CapturePaneContent(ctx context.Context, sessionName string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", sessionName, "-p")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n \t"), nil
}

// CapturePane returns the visible content of the active pane in a tmux session.
func CapturePane(ctx context.Context, sessionName string, maxLines int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", sessionName, "-p")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Trim trailing blank lines, then limit to maxLines
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")

	// Find last non-empty line
	last := len(lines) - 1
	for last > 0 && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	lines = lines[:last+1]

	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n"), nil
}
