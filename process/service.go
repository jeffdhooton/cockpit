// Package process is the one implementation of process lifecycle control.
// The TUI and the daemon's MCP tools both route through it, so there is a
// single reading of what a window is, who owns it, whether the user stopped
// it on purpose, and what an action actually did.
package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// Outcome is what an action established. It is deliberately not a boolean:
// a lost response after dispatch is neither success nor failure.
type Outcome string

const (
	OutcomeStarted        Outcome = "started"
	OutcomeAlreadyRunning Outcome = "already_running"
	OutcomeRestarted      Outcome = "restarted"
	OutcomeStopped        Outcome = "stopped"
	OutcomeNotRunning     Outcome = "not_running"
	OutcomeAdopted        Outcome = "adopted"
	OutcomeBlocked        Outcome = "blocked" // safety rule refused the action; nothing changed
	OutcomePartial        Outcome = "partial" // some of the transaction landed
	OutcomeUnknown        Outcome = "unknown" // dispatched, response lost
	OutcomeUnavailable    Outcome = "unavailable"
)

// Result is the report of one action.
type Result struct {
	Project string  `json:"project"`
	Process string  `json:"process"`
	Outcome Outcome `json:"outcome"`
	Message string  `json:"message,omitempty"`
	// Observation is the re-read after the action, when one succeeded.
	Observation *sources.ProcessObservation `json:"observation,omitempty"`
}

// Err converts a refused or failed result into an error for callers that
// only have an error channel, such as the MCP tools. Unknown outcomes are
// errors too: the caller must not treat them as done.
func (r Result) Err() error {
	switch r.Outcome {
	case OutcomeBlocked, OutcomeUnavailable, OutcomeUnknown, OutcomePartial:
		return fmt.Errorf("%s/%s: %s: %s", r.Project, r.Process, r.Outcome, r.Message)
	}
	return nil
}

// ErrAmbiguousProject means a bare label matched no local project. It is
// never reinterpreted as a remote one: a qualified key is the only way to
// reach a remote project.
var ErrAmbiguousProject = errors.New("unknown local project; remote projects are addressed as host/label")

// Service holds the runner routing. It has no state of its own: every
// action re-reads tmux under the project lock.
type Service struct {
	Cfg   *config.Config
	Local sources.Runner
	// Remote builds the runner for a configured host. Nil means the system
	// ssh through sources.SSHRunner.
	Remote func(host config.HostConfig) sources.Runner
	Now    func() time.Time
}

// New builds a service over the local runner and the default ssh transport.
func New(cfg *config.Config, local sources.Runner) *Service {
	return &Service{Cfg: cfg, Local: local, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// Resolve turns a project key — a bare local label or host/label — into
// its config and the runner that owns it. A qualified remote key can never
// resolve to the local runner, and a bare label never to a remote project.
func (s *Service) Resolve(key string) (config.RepoConfig, sources.Runner, error) {
	host, label, qualified := strings.Cut(key, "/")
	if !qualified {
		repo, ok := s.Cfg.Repo(key)
		if !ok {
			return config.RepoConfig{}, nil, fmt.Errorf("project %q: %w", key, ErrAmbiguousProject)
		}
		return repo, s.Local, nil
	}
	repo, ok := s.Cfg.RepoOn(host, label)
	if !ok {
		return config.RepoConfig{}, nil, fmt.Errorf("unknown project %q", key)
	}
	hc, ok := s.Cfg.Host(host)
	if !ok {
		return config.RepoConfig{}, nil, fmt.Errorf("project %q names host %q, which is not configured", key, host)
	}
	return repo, s.runnerFor(hc), nil
}

// RunnerFor returns the runner that owns a repo, local or remote.
func (s *Service) RunnerFor(repo config.RepoConfig) (sources.Runner, error) {
	if repo.Host == "" {
		return s.Local, nil
	}
	hc, ok := s.Cfg.Host(repo.Host)
	if !ok {
		return nil, fmt.Errorf("project %q names host %q, which is not configured", repo.Key(), repo.Host)
	}
	return s.runnerFor(hc), nil
}

func (s *Service) runnerFor(h config.HostConfig) sources.Runner {
	if s.Remote != nil {
		return s.Remote(h)
	}
	return sources.SSHRunner{Host: h.Name, Tmux: h.Tmux}
}

// Inspect reads a project's processes. It creates nothing and starts
// nothing: a dormant project lists its declared commands as not started.
func (s *Service) Inspect(ctx context.Context, key string) (sources.ProcessObservation, error) {
	repo, r, err := s.Resolve(key)
	if err != nil {
		return sources.ProcessObservation{}, err
	}
	return sources.ObserveProcesses(ctx, r, repo, s.now())
}

// Start launches one configured command. It creates at most the project's
// shell session and that command's window, clears any stop override as part
// of the launch, and refuses to duplicate a window that already exists —
// including one another client is creating at the same moment, which the
// project lock serialises.
func (s *Service) Start(ctx context.Context, key, process string) Result {
	return s.transact(ctx, key, process, s.start)
}

// Stop records the user's intent before terminating the validated window, so
// that whatever happens next, the next project entry will not undo the stop.
func (s *Service) Stop(ctx context.Context, key, process string) Result {
	return s.transact(ctx, key, process, s.stop)
}

// Restart relaunches a running or exited managed window in place. It never
// leaves a stop override behind: restart means keep running.
func (s *Service) Restart(ctx context.Context, key, process string) Result {
	return s.transact(ctx, key, process, s.restart)
}

// Adopt marks an unmanaged window that matches a configured process as that
// process, after the caller has shown the user which window and obtained
// consent. It launches and replaces nothing.
func (s *Service) Adopt(ctx context.Context, key, process, windowID string) Result {
	return s.transact(ctx, key, process, func(ctx context.Context, r sources.Runner, repo config.RepoConfig, p config.ProcessConfig, obs sources.ProcessObservation) Result {
		return s.adopt(ctx, r, repo, p, obs, windowID)
	})
}

// Reconcile brings a project's auto-start processes up under the lock,
// honouring stop overrides and ownership. It is what project entry calls.
func (s *Service) Reconcile(ctx context.Context, repo config.RepoConfig) []error {
	r, err := s.RunnerFor(repo)
	if err != nil {
		return []error{err}
	}
	return s.ReconcileOn(ctx, r, repo)
}

// ReconcileOn is Reconcile with the runner already chosen.
func (s *Service) ReconcileOn(ctx context.Context, r sources.Runner, repo config.RepoConfig) []error {
	if len(repo.Processes) == 0 {
		return nil
	}
	lock, err := sources.AcquireProjectLock(ctx, r, repo.Label, s.now)
	if err != nil {
		return []error{fmt.Errorf("reconcile %s: %w", repo.Key(), err)}
	}
	defer lock.Release(ctx, r)
	return sources.ReconcileProcesses(ctx, r, repo)
}

// Prepare makes a project ready to enter: its session exists and its
// auto-start processes are reconciled, all under the project lock. It
// reports whether the session was created. Process failures never block the
// entry; they are returned alongside.
func (s *Service) Prepare(ctx context.Context, r sources.Runner, repo config.RepoConfig) (created bool, procErrs []error, err error) {
	lock, err := sources.AcquireProjectLock(ctx, r, repo.Label, s.now)
	if err != nil {
		return false, nil, fmt.Errorf("prepare %s: %w", repo.Key(), err)
	}
	defer lock.Release(ctx, r)

	created, err = sources.EnsureSession(ctx, r, repo)
	if err != nil {
		return false, nil, err
	}
	if len(repo.Processes) > 0 {
		procErrs = sources.ReconcileProcesses(ctx, r, repo)
	}
	return created, procErrs, nil
}

type action func(ctx context.Context, r sources.Runner, repo config.RepoConfig, p config.ProcessConfig, obs sources.ProcessObservation) Result

// transact resolves, locks, observes, acts, and re-observes. Every mutation
// goes through here, so the identity re-read under the lock is not optional.
func (s *Service) transact(ctx context.Context, key, process string, act action) Result {
	res := Result{Project: key, Process: process}
	repo, r, err := s.Resolve(key)
	if err != nil {
		res.Outcome = OutcomeBlocked
		res.Message = err.Error()
		return res
	}
	res.Project = repo.Key()
	p, ok := repo.Process(process)
	if !ok {
		res.Outcome = OutcomeBlocked
		res.Message = fmt.Sprintf("project %q has no configured process %q", repo.Key(), process)
		return res
	}

	lock, err := sources.AcquireProjectLock(ctx, r, repo.Label, s.now)
	if err != nil {
		return s.unavailable(res, err, "could not take the project lock")
	}
	defer lock.Release(ctx, r)

	obs, err := sources.ObserveProcesses(ctx, r, repo, s.now())
	if err != nil {
		return s.unavailable(res, err, "could not read the project's windows; nothing was changed")
	}

	out := act(ctx, r, repo, p, obs)
	out.Project, out.Process = repo.Key(), p.Name
	if out.Outcome == OutcomeBlocked || out.Outcome == OutcomeUnavailable || out.Outcome == OutcomeUnknown {
		return out
	}
	after, err := sources.ObserveProcesses(ctx, r, repo, s.now())
	if err != nil {
		// The action was dispatched; what it did cannot be established.
		out.Outcome = OutcomeUnknown
		out.Message = strings.TrimSpace(out.Message + " Outcome unknown: the re-read failed (" + err.Error() + "). Re-read when the host answers; do not retry blindly.")
		return out
	}
	out.Observation = &after
	return out
}

// unavailable classifies a failure that happened before any mutation.
func (s *Service) unavailable(res Result, err error, what string) Result {
	res.Outcome = OutcomeUnavailable
	res.Message = what + ": " + err.Error()
	return res
}

// dispatched classifies a failure on the mutating call itself. A transport
// error after dispatch is an unknown outcome: the command may have run.
func dispatched(res Result, err error, did string) Result {
	if errors.Is(err, sources.ErrHostUnreachable) {
		res.Outcome = OutcomeUnknown
		res.Message = did + " was dispatched but the connection dropped before a response: " + err.Error() +
			". The command may have run. Re-read when the host is reachable; no automatic retry."
		return res
	}
	res.Outcome = OutcomeBlocked
	res.Message = did + " failed: " + err.Error()
	return res
}

func find(obs sources.ProcessObservation, name string) sources.ProcessInfo {
	for _, p := range obs.Processes {
		if p.Configured && p.Name == name {
			return p
		}
	}
	return sources.ProcessInfo{Name: name, WindowIndex: -1, Outcome: sources.OutcomeNotStarted}
}

func (s *Service) start(ctx context.Context, r sources.Runner, repo config.RepoConfig, p config.ProcessConfig, obs sources.ProcessObservation) Result {
	res := Result{}
	info := find(obs, p.Name)
	switch {
	case info.Ambiguous:
		res.Outcome = OutcomeBlocked
		res.Message = "several windows claim this process; inspect and close the extra ones first"
		return res
	case info.Adoptable:
		res.Outcome = OutcomeBlocked
		res.Message = fmt.Sprintf("a window named %q exists that cockpit did not start; adopt it or rename it before starting", p.Name)
		return res
	case info.Outcome == sources.OutcomeRunning:
		res.Outcome = OutcomeAlreadyRunning
		res.Message = "already running"
		if info.DesiredState == sources.DesiredStopped {
			if _, err := r.Run(ctx, sources.ClearStopOverrideArgs(repo.Label, p.Name)...); err != nil {
				return dispatched(res, err, "clearing the stop override")
			}
			res.Message = "already running; automatic startup resumed"
		}
		return res
	case info.Outcome == sources.OutcomeExited || info.Outcome == sources.OutcomeCompleted:
		if info.Split {
			res.Outcome = OutcomeBlocked
			res.Message = "the window has been split; restarting it would kill the other panes"
			return res
		}
		if _, err := r.Run(ctx, sources.ClearStopOverrideArgs(repo.Label, p.Name)...); err != nil {
			return dispatched(res, err, "clearing the stop override")
		}
		if err := sources.RestartWindow(ctx, r, info.WindowID, repo, p); err != nil {
			return dispatched(res, err, "respawn")
		}
		res.Outcome = OutcomeRestarted
		res.Message = "relaunched in its retained window"
		return res
	}

	// Not started or stopped: create the shell session if needed, then this
	// one window and nothing else.
	if !obs.SessionExists {
		created, err := sources.EnsureSession(ctx, r, repo)
		if err != nil {
			if errors.Is(err, sources.ErrHostUnreachable) {
				return dispatched(res, err, "session creation")
			}
			// Someone else created it between the lock and now (a client
			// that predates the lock). Re-read rather than guess.
			again, rerr := sources.ObserveProcesses(ctx, r, repo, s.now())
			if rerr != nil || !again.SessionExists {
				return dispatched(res, err, "session creation")
			}
			obs = again
			if f := find(obs, p.Name); f.WindowID != "" {
				res.Outcome = OutcomeAlreadyRunning
				res.Message = "another client created it first"
				return res
			}
		} else if created {
			res.Message = "created the project's shell session; "
		}
	}
	if _, err := r.Run(ctx, sources.ClearStopOverrideArgs(repo.Label, p.Name)...); err != nil {
		res.Outcome = OutcomePartial
		res.Message += "session exists but the stop override could not be cleared: " + err.Error()
		return res
	}
	if err := sources.StartProcess(ctx, r, repo, p); err != nil {
		if errors.Is(err, sources.ErrHostUnreachable) {
			return dispatched(res, err, "launch")
		}
		res.Outcome = OutcomePartial
		res.Message += "session exists but the launch failed: " + err.Error()
		return res
	}
	res.Outcome = OutcomeStarted
	res.Message += "started"
	return res
}

func (s *Service) stop(ctx context.Context, r sources.Runner, repo config.RepoConfig, p config.ProcessConfig, obs sources.ProcessObservation) Result {
	res := Result{}
	info := find(obs, p.Name)
	switch {
	case info.WindowID == "" && info.WindowIndex < 0:
		res.Outcome = OutcomeNotRunning
		res.Message = "not running; nothing to stop"
		return res
	case !info.Controllable():
		res.Outcome = OutcomeBlocked
		res.Message = whyNotControllable(info)
		return res
	}

	// Intent first. If this write fails nothing is terminated: a stop that
	// the next project entry silently undoes is worse than no stop.
	if _, err := r.Run(ctx, sources.SetStopOverrideArgs(repo.Label, p.Name, s.now())...); err != nil {
		if errors.Is(err, sources.ErrHostUnreachable) {
			return dispatched(res, err, "recording the stop intent")
		}
		res.Outcome = OutcomeBlocked
		res.Message = "could not record the stop intent; nothing was terminated: " + err.Error()
		return res
	}
	if err := sources.StopWindow(ctx, r, info.WindowID); err != nil {
		if errors.Is(err, sources.ErrHostUnreachable) {
			return dispatched(res, err, "kill-window")
		}
		res.Outcome = OutcomePartial
		res.Message = "automatic startup paused; the process may still be running: " + err.Error()
		return res
	}
	res.Outcome = OutcomeStopped
	res.Message = "stopped; it stays stopped until you start it"
	return res
}

func (s *Service) restart(ctx context.Context, r sources.Runner, repo config.RepoConfig, p config.ProcessConfig, obs sources.ProcessObservation) Result {
	res := Result{}
	info := find(obs, p.Name)
	switch {
	case info.WindowID == "" && info.WindowIndex < 0:
		res.Outcome = OutcomeNotRunning
		res.Message = "not running; use start"
		return res
	case !info.Controllable():
		res.Outcome = OutcomeBlocked
		res.Message = whyNotControllable(info)
		return res
	}
	if _, err := r.Run(ctx, sources.ClearStopOverrideArgs(repo.Label, p.Name)...); err != nil {
		if errors.Is(err, sources.ErrHostUnreachable) {
			return dispatched(res, err, "clearing the stop override")
		}
		res.Outcome = OutcomeBlocked
		res.Message = "could not clear the stop override; nothing was restarted: " + err.Error()
		return res
	}
	if err := sources.RestartWindow(ctx, r, info.WindowID, repo, p); err != nil {
		return dispatched(res, err, "respawn")
	}
	res.Outcome = OutcomeRestarted
	res.Message = "restarted in its window"
	return res
}

func (s *Service) adopt(ctx context.Context, r sources.Runner, repo config.RepoConfig, p config.ProcessConfig, obs sources.ProcessObservation, windowID string) Result {
	res := Result{}
	info := find(obs, p.Name)
	switch {
	case info.Managed:
		res.Outcome = OutcomeBlocked
		res.Message = "already managed by cockpit"
		return res
	case !info.Adoptable || info.WindowID == "":
		res.Outcome = OutcomeBlocked
		res.Message = "no unmanaged window named for this process exists"
		return res
	case windowID != "" && windowID != info.WindowID:
		res.Outcome = OutcomeBlocked
		res.Message = fmt.Sprintf("the window you reviewed (%s) is no longer the one named %q (%s); review again", windowID, p.Name, info.WindowID)
		return res
	}
	if _, err := r.Run(ctx, sources.MarkManagedArgs(info.WindowID, p.Name)...); err != nil {
		return dispatched(res, err, "adoption")
	}
	res.Outcome = OutcomeAdopted
	res.Message = "adopted " + info.WindowID + "; nothing was launched"
	return res
}

func whyNotControllable(info sources.ProcessInfo) string {
	switch {
	case info.Ambiguous:
		return "several windows claim this process; close the extra ones first"
	case info.Split:
		return "the window has been split; stopping it would kill the other panes"
	case !info.Managed:
		return "this window was not started by cockpit; adopt it first (it is inspection-only until then)"
	default:
		return "the window's identity could not be established"
	}
}

// Output is a bounded read of one window's pane.
type Output struct {
	Project  string `json:"project"`
	Window   string `json:"window"`
	WindowID string `json:"window_id,omitempty"`
	PaneID   string `json:"pane_id,omitempty"`
	Lines    int    `json:"lines"`
	Text     string `json:"output"`
}

// DefaultOutputLines and MaxOutputLines bound the interactive read.
const (
	DefaultOutputLines = 200
	MaxOutputLines     = 2000
)

// ReadOutput captures the last lines of a process's pane. The window is
// found by the observed identity when it has one; an unmanaged window that
// merely shares the name is still readable, because reading is safe.
func (s *Service) ReadOutput(ctx context.Context, key, window string, lines int) (Output, error) {
	repo, r, err := s.Resolve(key)
	if err != nil {
		return Output{}, err
	}
	if lines <= 0 {
		lines = DefaultOutputLines
	}
	if lines > MaxOutputLines {
		lines = MaxOutputLines
	}
	obs, err := sources.ObserveProcesses(ctx, r, repo, s.now())
	if err != nil {
		return Output{}, err
	}
	var target string
	out := Output{Project: repo.Key(), Window: window, Lines: lines}
	for _, p := range obs.Processes {
		if p.Name != window {
			continue
		}
		if p.PaneID != "" {
			target = p.PaneID
			out.WindowID, out.PaneID = p.WindowID, p.PaneID
		}
		break
	}
	if target == "" {
		return out, fmt.Errorf("project %q has no window %q to read", repo.Key(), window)
	}
	text, err := r.Run(ctx, sources.CapturePaneArgs(target, lines)...)
	if err != nil {
		return out, err
	}
	out.Text = sources.StripControl(text)
	return out, nil
}

// Spawn launches an ad-hoc command — an agent, a shell — as its own window
// in the project's session, creating the session if needed, under the
// project lock. A window of that name already present refuses the launch
// rather than producing a duplicate. It returns the runner used so the
// caller can keep talking to the window.
func (s *Service) Spawn(ctx context.Context, repo config.RepoConfig, p config.ProcessConfig) (sources.Runner, error) {
	r, err := s.RunnerFor(repo)
	if err != nil {
		return nil, err
	}
	lock, err := sources.AcquireProjectLock(ctx, r, repo.Label, s.now)
	if err != nil {
		return nil, fmt.Errorf("spawn %s: %w", repo.Key(), err)
	}
	defer lock.Release(ctx, r)

	if _, err := sources.EnsureSession(ctx, r, repo); err != nil {
		return nil, err
	}
	windows, err := sources.ListWindows(ctx, r, repo.Label)
	if err != nil {
		return nil, fmt.Errorf("spawn %s: cannot read windows: %w", repo.Key(), err)
	}
	for _, w := range windows {
		if w.Name == p.Name {
			return nil, fmt.Errorf("spawn %s: a window named %q already exists", repo.Key(), p.Name)
		}
	}
	if err := sources.StartProcess(ctx, r, repo, p); err != nil {
		return nil, err
	}
	return r, nil
}
