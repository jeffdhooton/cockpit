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

// ExecRunner runs tmux as a subprocess.
type ExecRunner struct {
	// Binary is the tmux executable, "tmux" when empty. Set it to an absolute
	// path where PATH is not trustworthy — a launch agent gets a bare
	// /usr/bin:/bin:/usr/sbin:/sbin that excludes Homebrew.
	Binary  string
	Timeout time.Duration
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

	out, err := exec.CommandContext(ctx, binary, args...).Output()
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
			return "", fmt.Errorf("tmux %s: %s", verb, strings.TrimSpace(string(ee.Stderr)))
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

// GetTmuxSessions returns all tmux sessions via the tmux CLI.
func GetTmuxSessions(ctx context.Context) ([]TmuxSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", sessionFormat)
	out, err := cmd.Output()
	if err != nil {
		// tmux server not running — not an error, just no sessions
		return nil, nil
	}
	return parseTmuxOutput(string(out))
}

// parseTmuxOutput parses the tab-delimited output of tmux list-sessions.
func parseTmuxOutput(output string) ([]TmuxSession, error) {
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
		if len(parts) < 4 {
			continue
		}

		// Layout is name | windows | attached | last_attached, and the name may
		// contain the separator, so anchor on the three numeric fields at the
		// end and treat everything before them as the name.
		nameEnd := len(parts) - 3
		windows, _ := strconv.Atoi(parts[nameEnd])
		attached := parts[nameEnd+1] == "1"
		epoch, _ := strconv.ParseInt(parts[nameEnd+2], 10, 64)
		lastUsed := time.Unix(epoch, 0)

		sessions = append(sessions, TmuxSession{
			Name:     strings.Join(parts[:nameEnd], fieldSep),
			Windows:  windows,
			Attached: attached,
			LastUsed: lastUsed,
		})
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
