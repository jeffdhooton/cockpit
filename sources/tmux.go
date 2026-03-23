package sources

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GetTmuxSessions returns all tmux sessions via the tmux CLI.
func GetTmuxSessions(ctx context.Context) ([]TmuxSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "list-sessions", "-F",
		"#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_last_attached}")
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
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}

		windows, _ := strconv.Atoi(parts[1])
		attached := parts[2] == "1"
		epoch, _ := strconv.ParseInt(parts[3], 10, 64)
		lastUsed := time.Unix(epoch, 0)

		sessions = append(sessions, TmuxSession{
			Name:     parts[0],
			Windows:  windows,
			Attached: attached,
			LastUsed: lastUsed,
		})
	}
	return sessions, nil
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
