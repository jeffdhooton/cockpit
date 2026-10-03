package sources

import (
	"context"
	"fmt"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

// ProcessState is the legacy three-value state MCP callers already read.
type ProcessState string

const (
	ProcessRunning    ProcessState = "running"
	ProcessDead       ProcessState = "dead"
	ProcessNotStarted ProcessState = "not_started"
)

// ProcessOutcome is the finer observation the TUI displays. It separates a
// command that finished from one that crashed, and a process the user stopped
// from one that was never started.
type ProcessOutcome string

const (
	OutcomeRunning    ProcessOutcome = "running"
	OutcomeExited     ProcessOutcome = "exited"    // nonzero or unknown code
	OutcomeCompleted  ProcessOutcome = "completed" // exit code 0
	OutcomeNotStarted ProcessOutcome = "not_started"
	OutcomeStopped    ProcessOutcome = "stopped" // override, no live pane
	OutcomeUnknown    ProcessOutcome = "unknown"
)

// Desired states, from the stop override.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
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

	// DesiredState is what the user last asked for: stopped while a stop
	// override exists, running otherwise. Only configured processes have one.
	DesiredState string `json:"desired_state,omitempty"`
	// ExitCode is the retained pane's exit status, when tmux observed one.
	ExitCode *int `json:"exit_code,omitempty"`
	// Outcome is the observation the display states are built from.
	Outcome ProcessOutcome `json:"outcome"`
	// Display is the human reading of Outcome plus its qualifiers.
	Display string `json:"display"`

	// Identity. A window name is not proof of contents; these are.
	WindowID   string `json:"window_id,omitempty"`
	PaneID     string `json:"pane_id,omitempty"`
	Generation string `json:"generation,omitempty"`
	Panes      int    `json:"panes,omitempty"`
	// Managed is true when the window carries cockpit's mark for this
	// process. Adoptable is true for an unmarked window that merely shares
	// the name: it can be inspected, and adopted on explicit request, but
	// not stopped or restarted. Split is true when the window has more than
	// one pane, so a kill would take other work with it. Ambiguous is true
	// when more than one window claims the process.
	Managed   bool `json:"managed"`
	Adoptable bool `json:"adoptable,omitempty"`
	Split     bool `json:"split,omitempty"`
	Ambiguous bool `json:"ambiguous,omitempty"`
	// StoppedAt is when the stop override was recorded.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}

// Controllable reports whether Stop and Restart may target this process:
// a managed, unambiguous, unsplit window with a live or retained pane.
func (p ProcessInfo) Controllable() bool {
	return p.Configured && p.Managed && !p.Split && !p.Ambiguous && p.WindowID != ""
}

// ProcessObservation is one successful read of a project's process windows.
type ProcessObservation struct {
	Project       string        `json:"project"`
	Host          string        `json:"host,omitempty"`
	Session       string        `json:"session"`
	SessionExists bool          `json:"session_exists"`
	Generation    string        `json:"generation,omitempty"`
	Processes     []ProcessInfo `json:"processes"`
	ObservedAt    time.Time     `json:"observed_at"`
}

// ListSessions returns every tmux session. No server running means no
// sessions, which is an answer rather than a failure.
func ListSessions(ctx context.Context, r Runner) ([]TmuxSession, error) {
	return ListSessionsOn(ctx, r, "", time.Now())
}

// ListSessionsOn lists the sessions on one host, judging status staleness by
// that host's clock.
func ListSessionsOn(ctx context.Context, r Runner, host string, now time.Time) ([]TmuxSession, error) {
	out, err := r.Run(ctx, ListSessionsArgs()...)
	if err != nil {
		// No server is an answer: nothing is running. Anything else — a
		// missing binary, an unreachable host, a permission error — means we
		// could not read, which is not the same as reading nothing.
		if IsNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseTmuxOutput(out, host, now)
}

// SessionExists reports whether a tmux session is present.
func SessionExists(ctx context.Context, r Runner, session string) bool {
	exists, _ := sessionExists(ctx, r, session)
	return exists
}

// sessionExists is SessionExists with the failure kept. A has-session that
// failed because the host is unreachable is not a verified negative, and the
// callers that act on absence need to tell the two apart.
func sessionExists(ctx context.Context, r Runner, session string) (bool, error) {
	_, err := r.Run(ctx, HasSessionArgs(session)...)
	if err == nil {
		return true, nil
	}
	// Only tmux saying "no such session" or "no server" is a verified
	// absence. Anything else is a failed read.
	if IsVerifiedAbsence(err) {
		return false, nil
	}
	return false, err
}

// ListWindows returns the windows in a session.
func ListWindows(ctx context.Context, r Runner, session string) ([]Window, error) {
	out, err := r.Run(ctx, ListWindowsArgs(session)...)
	if err != nil {
		return nil, err
	}
	return ParseWindows(out), nil
}

// ReadStopOverrides reads a session's stop overrides. A session that does
// not exist has none, which is an answer; any other failure is returned.
func ReadStopOverrides(ctx context.Context, r Runner, session string) (map[string]StopOverride, error) {
	out, err := r.Run(ctx, ShowSessionOptionsArgs(session)...)
	if err != nil {
		if IsVerifiedAbsence(err) {
			return map[string]StopOverride{}, nil
		}
		return nil, err
	}
	return ParseStopOverrides(out), nil
}

// EnsureSession creates the repo's session if it is missing, reporting whether
// it had to. The session's first window is a plain shell at the repo root.
func EnsureSession(ctx context.Context, r Runner, repo config.RepoConfig) (bool, error) {
	exists, err := sessionExists(ctx, r, repo.Label)
	if err != nil {
		return false, fmt.Errorf("ensure %s: %w", repo.Label, err)
	}
	if exists {
		return false, nil
	}
	if _, err := r.Run(ctx, NewSessionArgs(repo.Label, repo.Path)...); err != nil {
		return false, err
	}
	// Cockpit created this session, so it may set its options. Doing it before
	// any process window exists means there is no race to lose a crash to.
	// Best effort: older tmux does not accept "failed", and the per-window
	// setting still covers those.
	_, _ = r.Run(ctx, RemainOnExitFailedArgs(repo.Label)...)
	return true, nil
}

// InspectProcesses reports the state of every configured process, plus any
// other window in the session that the config does not know about. A session
// that does not exist yet is not an error — nothing is running, which is a
// perfectly good answer.
func InspectProcesses(ctx context.Context, r Runner, repo config.RepoConfig) ([]ProcessInfo, error) {
	obs, err := ObserveProcesses(ctx, r, repo, time.Now())
	if err != nil {
		return nil, err
	}
	return obs.Processes, nil
}

// ObserveProcesses is InspectProcesses with the identity and stop intent that
// lifecycle decisions need. Only a verified absence of the session yields an
// empty observation; a failed read is returned as an error, never as "nothing
// is running".
func ObserveProcesses(ctx context.Context, r Runner, repo config.RepoConfig, now time.Time) (ProcessObservation, error) {
	obs := ProcessObservation{
		Project:    repo.Key(),
		Host:       repo.Host,
		Session:    repo.Label,
		ObservedAt: now,
	}

	windows, err := ListWindows(ctx, r, repo.Label)
	switch {
	case err == nil:
		obs.SessionExists = true
	case IsVerifiedAbsence(err):
		windows = nil
	default:
		return obs, fmt.Errorf("inspect %s: %w", repo.Key(), err)
	}
	if len(windows) > 0 {
		obs.Generation = windows[0].Generation
	}

	overrides := map[string]StopOverride{}
	if obs.SessionExists {
		overrides, err = ReadStopOverrides(ctx, r, repo.Label)
		if err != nil {
			return obs, fmt.Errorf("inspect %s: %w", repo.Key(), err)
		}
	}

	obs.Processes = classifyProcesses(repo, windows, overrides)
	return obs, nil
}

// classifyProcesses joins configured processes onto observed windows. It is
// pure so the ownership and stop-intent rules can be tested without tmux.
func classifyProcesses(repo config.RepoConfig, windows []Window, overrides map[string]StopOverride) []ProcessInfo {
	infos := make([]ProcessInfo, 0, len(windows)+len(repo.Processes))
	claimed := map[string]bool{} // window id (or index when unknown) → taken

	windowKey := func(w Window) string {
		if w.ID != "" {
			return w.ID
		}
		return fmt.Sprintf("#%d", w.Index)
	}

	for _, p := range repo.Processes {
		info := ProcessInfo{
			Name:         p.Name,
			Command:      p.Command,
			State:        ProcessNotStarted,
			WindowIndex:  -1,
			AutoStart:    p.ShouldAutoStart(),
			Configured:   true,
			DesiredState: DesiredRunning,
			Outcome:      OutcomeNotStarted,
		}
		if o, ok := overrides[p.Name]; ok {
			info.DesiredState = DesiredStopped
			if !o.Since.IsZero() {
				since := o.Since
				info.StoppedAt = &since
			}
		}

		// A marked window is the process. An unmarked window of the same
		// name is a candidate for adoption and nothing more.
		var marked, named []Window
		for _, w := range windows {
			switch {
			case w.Managed == p.Name:
				marked = append(marked, w)
			case w.Managed == "" && w.Name == p.Name:
				named = append(named, w)
			}
		}

		switch {
		case len(marked) > 0:
			w := marked[0]
			claimed[windowKey(w)] = true
			info.Managed = true
			info.Ambiguous = len(marked) > 1
			for _, extra := range marked[1:] {
				claimed[windowKey(extra)] = true
			}
			fillWindow(&info, w)
		case len(named) > 0:
			w := named[0]
			claimed[windowKey(w)] = true
			info.Adoptable = true
			fillWindow(&info, w)
		}

		info.Outcome, info.Display = describeProcess(info)
		infos = append(infos, info)
	}

	for _, w := range windows {
		if claimed[windowKey(w)] {
			continue
		}
		info := ProcessInfo{Name: w.Name, WindowIndex: w.Index}
		fillWindow(&info, w)
		info.Outcome, info.Display = describeProcess(info)
		infos = append(infos, info)
	}
	return infos
}

func fillWindow(info *ProcessInfo, w Window) {
	info.WindowIndex = w.Index
	info.PanePID = w.PanePID
	info.WindowID = w.ID
	info.PaneID = w.PaneID
	info.Generation = w.Generation
	info.Panes = w.Panes
	info.Split = w.Panes > 1
	if w.Dead {
		info.State = ProcessDead
		if w.HasDeadStatus {
			code := w.DeadStatus
			info.ExitCode = &code
		}
	} else {
		info.State = ProcessRunning
	}
}

// describeProcess derives the observation and its display text from the
// joined facts. It is the one place the display vocabulary lives.
func describeProcess(info ProcessInfo) (ProcessOutcome, string) {
	paused := info.DesiredState == DesiredStopped
	switch {
	case info.Ambiguous:
		return OutcomeUnknown, "Unknown (several windows claim this process)"
	case info.WindowID == "" && info.WindowIndex < 0:
		if paused {
			return OutcomeStopped, "Stopped by you"
		}
		return OutcomeNotStarted, "Not started"
	case info.State == ProcessRunning:
		s := "Running"
		if info.Configured && !info.Managed {
			s += " (unmanaged)"
		}
		if info.Split {
			s += " (split)"
		}
		if paused {
			s += " · automatic startup paused"
		}
		return OutcomeRunning, s
	case info.ExitCode != nil && *info.ExitCode == 0:
		s := "Completed"
		if paused {
			s += " · automatic startup paused"
		}
		return OutcomeCompleted, s
	default:
		s := "Exited (code unknown)"
		if info.ExitCode != nil {
			s = fmt.Sprintf("Exited (code %d)", *info.ExitCode)
		}
		if info.Configured && !info.Managed {
			s += " (unmanaged)"
		}
		if paused {
			s += " · automatic startup paused"
		}
		return OutcomeExited, s
	}
}

// StartProcess launches a process in its own window.
func StartProcess(ctx context.Context, r Runner, repo config.RepoConfig, p config.ProcessConfig) error {
	// One invocation: the window, its remain-on-exit setting and its managed
	// mark land together, so a command that fails instantly cannot take its
	// error message with it.
	if _, err := r.Run(ctx, NewWindowArgs(repo.Label, p, repo.Path)...); err != nil {
		return fmt.Errorf("start %s/%s: %w", repo.Key(), p.Name, err)
	}
	return nil
}

// StopProcess removes a process's window by name. Callers that hold an
// observed window id should use StopWindow instead, which cannot be
// redirected by a rename or a duplicate name.
func StopProcess(ctx context.Context, r Runner, session, name string) error {
	if _, err := r.Run(ctx, KillWindowArgs(session, name)...); err != nil {
		return fmt.Errorf("stop %s/%s: %w", session, name, err)
	}
	return nil
}

// StopWindow removes a window by id.
func StopWindow(ctx context.Context, r Runner, windowID string) error {
	if _, err := r.Run(ctx, KillWindowByIDArgs(windowID)...); err != nil {
		return fmt.Errorf("stop %s: %w", windowID, err)
	}
	return nil
}

// RestartProcess relaunches a process in the window it already holds,
// addressed by name. RestartWindow is the id-addressed form.
func RestartProcess(ctx context.Context, r Runner, repo config.RepoConfig, p config.ProcessConfig) error {
	if _, err := r.Run(ctx, RespawnWindowArgs(repo.Label, p.Name, p, repo.Path)...); err != nil {
		return fmt.Errorf("restart %s/%s: %w", repo.Key(), p.Name, err)
	}
	return nil
}

// RestartWindow relaunches a process in the window with the given id.
func RestartWindow(ctx context.Context, r Runner, windowID string, repo config.RepoConfig, p config.ProcessConfig) error {
	if _, err := r.Run(ctx, RespawnWindowByIDArgs(windowID, p, repo.Path)...); err != nil {
		return fmt.Errorf("restart %s/%s: %w", repo.Key(), p.Name, err)
	}
	return nil
}

// ReconcileProcesses brings the session in line with the config: every
// auto-start process ends up with a live window, dead windows are respawned
// rather than duplicated, and everything already running is left alone.
//
// A process the user stopped on purpose is skipped, present or absent: that
// is what the stop override is for. So is a window cockpit did not create
// but which shares a process's name — restarting it could kill someone
// else's work — and a managed window that has been split.
//
// Failures are collected rather than returned early. One broken process should
// not stop the others, and it should never block the jump that triggered it.
func ReconcileProcesses(ctx context.Context, r Runner, repo config.RepoConfig) []error {
	if len(repo.Processes) == 0 {
		return nil
	}

	obs, err := ObserveProcesses(ctx, r, repo, time.Now())
	if err != nil {
		// A failed listing is not proof that nothing is running, and this is the
		// one caller that acts on the answer. Treating an unreadable session as
		// an empty one starts every process a second time — two dev servers
		// fighting over one port. Only a session verified absent makes an empty
		// list trustworthy; anything else fails closed.
		return []error{fmt.Errorf("reconcile %s: %w", repo.Key(), err)}
	}
	return reconcileFrom(ctx, r, repo, obs)
}

// reconcileFrom applies the reconciliation rules to an observation already
// in hand, which is how the process service reconciles under its lock.
func reconcileFrom(ctx context.Context, r Runner, repo config.RepoConfig, obs ProcessObservation) []error {
	byName := make(map[string]ProcessInfo, len(obs.Processes))
	for _, p := range obs.Processes {
		if p.Configured {
			byName[p.Name] = p
		}
	}

	var errs []error
	for _, p := range repo.Processes {
		if !p.ShouldAutoStart() {
			continue
		}
		info := byName[p.Name]
		switch {
		case info.DesiredState == DesiredStopped:
			// Stopped on purpose. Leave it, present or absent.
		case info.Ambiguous, info.Adoptable:
			// Not ours to touch.
		case info.Outcome == OutcomeRunning:
			// Already up.
		case info.Outcome == OutcomeExited || info.Outcome == OutcomeCompleted:
			if info.Split || !info.Managed {
				continue
			}
			if err := RestartWindow(ctx, r, info.WindowID, repo, p); err != nil {
				errs = append(errs, err)
			}
		case info.Outcome == OutcomeNotStarted:
			if err := StartProcess(ctx, r, repo, p); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}
