package sources

import (
	"context"
	"errors"
	"fmt"

	"github.com/jhoot/cockpit/config"
)

// ProcessState is what a configured process is currently doing.
type ProcessState string

const (
	ProcessRunning    ProcessState = "running"
	ProcessDead       ProcessState = "dead"
	ProcessNotStarted ProcessState = "not_started"
)

// ProcessInfo is the live state of one process window.
type ProcessInfo struct {
	Name        string       `json:"name"`
	Command     string       `json:"command,omitempty"`
	State       ProcessState `json:"state"`
	WindowIndex int          `json:"window_index"`
	PanePID     int          `json:"pane_pid,omitempty"`
	AutoStart   bool         `json:"auto_start"`
	Configured  bool         `json:"configured"`
}

// ListSessions returns every tmux session. No server running means no
// sessions, which is an answer rather than a failure.
func ListSessions(ctx context.Context, r Runner) ([]TmuxSession, error) {
	out, err := r.Run(ctx, ListSessionsArgs()...)
	if err != nil {
		// A missing binary means we cannot know. Anything else here is the
		// server not running, which is an answer.
		if errors.Is(err, ErrTmuxNotFound) {
			return nil, err
		}
		return nil, nil
	}
	return parseTmuxOutput(out)
}

// SessionExists reports whether a tmux session is present.
func SessionExists(ctx context.Context, r Runner, session string) bool {
	_, err := r.Run(ctx, HasSessionArgs(session)...)
	return err == nil
}

// ListWindows returns the windows in a session.
func ListWindows(ctx context.Context, r Runner, session string) ([]Window, error) {
	out, err := r.Run(ctx, ListWindowsArgs(session)...)
	if err != nil {
		return nil, err
	}
	return ParseWindows(out), nil
}

// EnsureSession creates the repo's session if it is missing, reporting whether
// it had to. The session's first window is a plain shell at the repo root.
func EnsureSession(ctx context.Context, r Runner, repo config.RepoConfig) (bool, error) {
	if SessionExists(ctx, r, repo.Label) {
		return false, nil
	}
	if _, err := r.Run(ctx, NewSessionArgs(repo.Label, repo.Path)...); err != nil {
		return false, err
	}
	return true, nil
}

// InspectProcesses reports the state of every configured process, plus any
// other window in the session that the config does not know about. A session
// that does not exist yet is not an error — nothing is running, which is a
// perfectly good answer.
func InspectProcesses(ctx context.Context, r Runner, repo config.RepoConfig) ([]ProcessInfo, error) {
	windows, err := ListWindows(ctx, r, repo.Label)
	if err != nil {
		// A session that does not exist yet is fine — nothing is running. A
		// missing tmux is not: every process would be reported as not started,
		// which is a confident lie.
		if errors.Is(err, ErrTmuxNotFound) {
			return nil, err
		}
		windows = nil
	}

	byName := make(map[string]Window, len(windows))
	for _, w := range windows {
		byName[w.Name] = w
	}

	infos := make([]ProcessInfo, 0, len(windows)+len(repo.Processes))
	configured := map[string]bool{}

	for _, p := range repo.Processes {
		configured[p.Name] = true
		info := ProcessInfo{
			Name:        p.Name,
			Command:     p.Command,
			State:       ProcessNotStarted,
			WindowIndex: -1,
			AutoStart:   p.ShouldAutoStart(),
			Configured:  true,
		}
		if w, ok := byName[p.Name]; ok {
			info.State = windowState(w)
			info.WindowIndex = w.Index
			info.PanePID = w.PanePID
		}
		infos = append(infos, info)
	}

	for _, w := range windows {
		if configured[w.Name] {
			continue
		}
		infos = append(infos, ProcessInfo{
			Name:        w.Name,
			State:       windowState(w),
			WindowIndex: w.Index,
			PanePID:     w.PanePID,
		})
	}
	return infos, nil
}

func windowState(w Window) ProcessState {
	if w.Dead {
		return ProcessDead
	}
	return ProcessRunning
}

// StartProcess launches a process in its own window.
func StartProcess(ctx context.Context, r Runner, repo config.RepoConfig, p config.ProcessConfig) error {
	if _, err := r.Run(ctx, NewWindowArgs(repo.Label, p, repo.Path)...); err != nil {
		return fmt.Errorf("start %s/%s: %w", repo.Label, p.Name, err)
	}
	// Best effort: on tmux versions without the option, the process still runs.
	_, _ = r.Run(ctx, RemainOnExitArgs(repo.Label, p.Name)...)
	return nil
}

// StopProcess removes a process's window.
func StopProcess(ctx context.Context, r Runner, session, name string) error {
	if _, err := r.Run(ctx, KillWindowArgs(session, name)...); err != nil {
		return fmt.Errorf("stop %s/%s: %w", session, name, err)
	}
	return nil
}

// RestartProcess relaunches a process in the window it already holds.
func RestartProcess(ctx context.Context, r Runner, repo config.RepoConfig, p config.ProcessConfig) error {
	if _, err := r.Run(ctx, RespawnWindowArgs(repo.Label, p.Name, p, repo.Path)...); err != nil {
		return fmt.Errorf("restart %s/%s: %w", repo.Label, p.Name, err)
	}
	_, _ = r.Run(ctx, RemainOnExitArgs(repo.Label, p.Name)...)
	return nil
}

// ReconcileProcesses brings the session in line with the config: every
// auto-start process ends up with a live window, dead windows are respawned
// rather than duplicated, and everything already running is left alone.
//
// Failures are collected rather than returned early. One broken process should
// not stop the others, and it should never block the jump that triggered it.
func ReconcileProcesses(ctx context.Context, r Runner, repo config.RepoConfig) []error {
	if len(repo.Processes) == 0 {
		return nil
	}

	windows, err := ListWindows(ctx, r, repo.Label)
	if err != nil {
		windows = nil
	}
	byName := make(map[string]Window, len(windows))
	for _, w := range windows {
		byName[w.Name] = w
	}

	var errs []error
	for _, p := range repo.Processes {
		if !p.ShouldAutoStart() {
			continue
		}
		w, exists := byName[p.Name]
		switch {
		case exists && !w.Dead:
			// Already up.
		case exists && w.Dead:
			if err := RestartProcess(ctx, r, repo, p); err != nil {
				errs = append(errs, err)
			}
		default:
			if err := StartProcess(ctx, r, repo, p); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}
