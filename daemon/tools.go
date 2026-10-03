package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/process"
	"github.com/jeffdhooton/cockpit/sources"
)

const (
	// defaultOutputLines is how much scrollback a read returns when the caller
	// does not say.
	defaultOutputLines = 100
	// maxOutputLines caps a read so one call cannot drag a whole history back.
	maxOutputLines = 10000
	// defaultStatusEvents and maxStatusEvents bound a status query.
	defaultStatusEvents = 16
	maxStatusEvents     = 64
)

// Tools implements every cockpit tool. It holds no process state: each call
// reads tmux, git, and the vault directly, so the daemon can restart at any
// time without losing anything.
type Tools struct {
	Cfg        *config.Config
	ConfigPath string
	Runner     sources.Runner
	Version    string
	Port       int
	Now        func() time.Time
	// Settle tunes how long to wait for a spawned agent to finish booting.
	// Injectable so tests do not sleep.
	Settle settleOptions
	// Svc is the shared process service every lifecycle call goes through.
	// It is the same code the TUI runs, so both see one set of rules.
	Svc *process.Service
	// tracker remembers first-observed times for attention items, in memory
	// only; a restart forgets them, as the queue's contract says.
	tracker sources.AttentionTracker
	trackMu sync.Mutex
}

// NewTools builds the tool set backed by a config and a tmux runner.
func NewTools(cfg *config.Config, configPath string, r sources.Runner, version string, port int) *Tools {
	t := &Tools{
		Cfg:        cfg,
		ConfigPath: configPath,
		Runner:     r,
		Version:    version,
		Port:       port,
		Now:        time.Now,
		Settle:     defaultSettleOptions(),
	}
	t.Svc = process.New(cfg, r)
	return t
}

// Call dispatches a tool by name.
func (t *Tools) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "cockpit_list_projects":
		return t.listProjects(ctx)
	case "cockpit_list_processes":
		return t.listProcesses(ctx, args)
	case "cockpit_read_output":
		return t.readOutput(ctx, args)
	case "cockpit_start":
		return t.startProcess(ctx, args)
	case "cockpit_stop":
		return t.stopProcess(ctx, args)
	case "cockpit_restart":
		return t.restartProcess(ctx, args)
	case "cockpit_signals":
		return t.signals(ctx)
	case "cockpit_attention":
		return t.attention(ctx)
	case "cockpit_workspaces":
		return t.workspaces(ctx)
	case "cockpit_git_status":
		return t.gitStatus(ctx, args)
	case "cockpit_spawn_agent":
		return t.spawnAgent(ctx, args)
	case "cockpit_write_input":
		return t.writeInput(ctx, args)
	case "cockpit_whoami":
		return t.whoami(ctx)
	case "cockpit_status":
		return t.status(ctx, args)
	case "cockpit_capture":
		return t.capture(args)
	case "cockpit_tasks":
		return t.tasks(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// --- read-only tools ---

func (t *Tools) listProjects(ctx context.Context) (any, error) {
	sessions, _ := sources.ListSessions(ctx, t.Runner)
	live := make(map[string]sources.TmuxSession, len(sessions))
	for _, s := range sessions {
		live[s.Name] = s
	}

	type project struct {
		Label            string `json:"label"`
		Key              string `json:"key"`
		Host             string `json:"host,omitempty"`
		Path             string `json:"path"`
		SessionRunning   bool   `json:"session_running"`
		SessionAttached  bool   `json:"session_attached"`
		Windows          int    `json:"windows"`
		ProcessesRunning int    `json:"processes_running"`
		ProcessesDead    int    `json:"processes_dead"`
		ProcessesTotal   int    `json:"processes_total"`
		// ProcessesUnknown is true when the windows could not be read; the
		// counts above are then zero because nothing was seen, not because
		// nothing is running.
		ProcessesUnknown bool   `json:"processes_unknown,omitempty"`
		Error            string `json:"error,omitempty"`
	}

	projects := make([]project, 0, len(t.Cfg.Repos))
	for _, repo := range t.Cfg.Repos {
		p := project{
			Label:          repo.Label,
			Key:            repo.Key(),
			Host:           repo.Host,
			Path:           repo.Path,
			ProcessesTotal: len(repo.Processes),
		}
		// Session state is read from this machine's server. A remote
		// project's session lives elsewhere and is reported through its
		// own inspection below rather than matched to a local session
		// that happens to share the label.
		if repo.Host == "" {
			s, running := live[repo.Label]
			p.SessionRunning = running
			p.SessionAttached = s.Attached
			p.Windows = s.Windows
		}
		if len(repo.Processes) > 0 || repo.Host != "" {
			obs, err := t.Svc.Inspect(ctx, repo.Key())
			if err != nil {
				p.ProcessesUnknown = true
				p.Error = err.Error()
			} else {
				if repo.Host != "" {
					p.SessionRunning = obs.SessionExists
				}
				for _, i := range obs.Processes {
					if !i.Configured {
						continue
					}
					switch i.State {
					case sources.ProcessRunning:
						p.ProcessesRunning++
					case sources.ProcessDead:
						p.ProcessesDead++
					}
				}
			}
		}
		projects = append(projects, p)
	}
	return map[string]any{"projects": projects}, nil
}

func (t *Tools) listProcesses(ctx context.Context, args map[string]any) (any, error) {
	repo, err := t.repo(args)
	if err != nil {
		return nil, err
	}
	obs, err := t.Svc.Inspect(ctx, repo.Key())
	if err != nil {
		// A failed read is a structured unknown, never a fabricated
		// "nothing is running".
		return map[string]any{
			"project":   repo.Key(),
			"outcome":   "unavailable",
			"error":     err.Error(),
			"processes": nil,
		}, nil
	}
	return map[string]any{
		"project":        repo.Key(),
		"host":           repo.Host,
		"session":        obs.Session,
		"session_exists": obs.SessionExists,
		"generation":     obs.Generation,
		"observed_at":    obs.ObservedAt,
		"outcome":        "observed",
		"processes":      obs.Processes,
	}, nil
}

func (t *Tools) readOutput(ctx context.Context, args map[string]any) (any, error) {
	repo, window, err := t.window(ctx, args)
	if err != nil {
		return nil, err
	}

	lines := argInt(args, "lines", defaultOutputLines)
	if lines <= 0 {
		lines = defaultOutputLines
	}
	if lines > maxOutputLines {
		lines = maxOutputLines
	}

	r, err := t.Svc.RunnerFor(repo)
	if err != nil {
		return nil, err
	}
	out, err := r.Run(ctx, sources.CapturePaneArgs(window.target, lines)...)
	if err != nil {
		return nil, err
	}

	captured := countLines(out)
	collapsed := collapseBlankRuns(out)
	returned := countLines(collapsed)

	paneID := ""
	if strings.HasPrefix(window.target, "%") {
		paneID = window.target
	}
	return map[string]any{
		"project":   repo.Key(),
		"process":   window.name,
		"window_id": window.id,
		"pane_id":   paneID,
		"lines":     lines,
		"output":    collapsed,
		// Say what was dropped. A silently edited transcript is worse than a
		// short one, because the caller cannot tell which it got.
		"lines_returned":      returned,
		"blank_lines_removed": captured - returned,
		"truncated":           captured >= lines,
	}, nil
}

// countLines counts the lines in captured output, treating empty output as
// zero lines rather than one.
func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func (t *Tools) gitStatus(ctx context.Context, args map[string]any) (any, error) {
	repos := t.Cfg.Repos
	if label := argString(args, "project"); label != "" {
		repo, err := t.repo(args)
		if err != nil {
			return nil, err
		}
		repos = []config.RepoConfig{repo}
	}

	statuses := sources.GetGitStatus(ctx, sources.LocalCommandRunner{}, repos)

	type status struct {
		Label      string `json:"label"`
		Branch     string `json:"branch"`
		Dirty      bool   `json:"dirty"`
		DirtyCount int    `json:"dirty_count"`
		Unpushed   int    `json:"unpushed"`
		Behind     int    `json:"behind"`
		LastCommit string `json:"last_commit"`
		Error      string `json:"error,omitempty"`
	}

	out := make([]status, 0, len(statuses))
	for _, s := range statuses {
		entry := status{
			Label:      s.Label,
			Branch:     s.Branch,
			Dirty:      s.Dirty,
			DirtyCount: s.DirtyCount,
			Unpushed:   s.Unpushed,
			Behind:     s.Behind,
			LastCommit: s.LastCommit,
		}
		if s.Error != nil {
			entry.Error = s.Error.Error()
		}
		out = append(out, entry)
	}
	return map[string]any{"repos": out}, nil
}

// signals is the legacy envelope: the same facts as cockpit_attention,
// adapted. It is not a second derivation.
func (t *Tools) signals(ctx context.Context) (any, error) {
	rep := t.collectAttention(ctx)
	signals := sources.SignalsFromAttention(rep)
	if signals == nil {
		signals = []sources.Signal{}
	}
	return map[string]any{"signals": signals}, nil
}

// attentionSchemaVersion is the version of the cockpit_attention document.
const attentionSchemaVersion = 1

// attention is read-only: records and coverage, no navigation, no
// dismissal, nothing started.
func (t *Tools) attention(ctx context.Context) (any, error) {
	rep := t.collectAttention(ctx)
	return map[string]any{
		"schema_version": attentionSchemaVersion,
		"observed_at":    t.Now(),
		"actionable":     rep.Actionable(),
		"unavailable":    rep.Unavailable,
		"items":          rep.Items,
		"coverage":       rep.Coverage,
	}, nil
}

// hostProbeTimeout bounds one remote host's contribution to a collection.
const hostProbeTimeout = 10 * time.Second

// workspaces is read-only: the host → session → pane tree, no previews,
// no navigation, nothing started.
func (t *Tools) workspaces(ctx context.Context) (any, error) {
	ws := sources.BuildWorkspace(t.collectInput(ctx))
	return map[string]any{
		"schema_version": attentionSchemaVersion,
		"observed_at":    t.Now(),
		"hosts":          ws.Hosts,
		"coverage":       ws.Coverage,
	}, nil
}

// collectAttention derives the queue from one collection pass and stamps
// first-observed times.
func (t *Tools) collectAttention(ctx context.Context) sources.AttentionReport {
	rep := sources.DeriveAttention(t.collectInput(ctx))
	t.trackMu.Lock()
	t.tracker.Track(&rep, t.Now())
	t.trackMu.Unlock()
	return rep
}

// collectInput observes every host — this machine through the local
// runner, each configured host through ssh, in parallel and bounded — into
// the input both the queue and the workspace tree derive from.
func (t *Tools) collectInput(ctx context.Context) sources.AttentionInput {
	now := t.Now()
	in := sources.AttentionInput{Config: t.Cfg.Signals, SelfSession: t.Cfg.General.SessionName, Now: now}

	reposOn := func(host string) []config.RepoConfig {
		var out []config.RepoConfig
		for _, r := range t.Cfg.Repos {
			if r.Host == host {
				out = append(out, r)
			}
		}
		return out
	}

	reports := make([]sources.HostReport, 1+len(t.Cfg.Hosts))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reports[0] = sources.ObserveHostReport(ctx, t.Runner, sources.LocalCommandRunner{}, "", reposOn(""), now, now)
	}()
	for i, h := range t.Cfg.Hosts {
		wg.Add(1)
		go func(i int, h config.HostConfig) {
			defer wg.Done()
			hctx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
			defer cancel()
			r, err := t.Svc.RunnerFor(config.RepoConfig{Host: h.Name})
			if err != nil {
				reports[i+1] = sources.HostReport{Host: h.Name, Outcome: sources.ObservationUnavailable, Err: err.Error(), ObservedAt: now}
				return
			}
			hostNow := now
			if ssh, ok := r.(sources.SSHRunner); ok {
				remoteNow, err := ssh.RemoteNow(hctx)
				if err != nil {
					reports[i+1] = sources.HostReport{Host: h.Name, Outcome: sources.ObservationUnavailable, Err: err.Error(), ObservedAt: now}
					return
				}
				hostNow = remoteNow
			}
			var cr sources.CommandRunner
			if ssh, ok := r.(sources.SSHRunner); ok {
				cr = ssh
			}
			reports[i+1] = sources.ObserveHostReport(hctx, r, cr, h.Name, reposOn(h.Name), hostNow, now)
		}(i, h)
	}
	wg.Wait()
	in.Hosts = reports

	if t.Cfg.GitHub.Enabled {
		in.GitHub = sources.GetGitHubStatus(ctx, t.Cfg.Repos)
		in.GitHubAt = now
	}
	for _, h := range t.Cfg.Hermes {
		in.Hermes = append(in.Hermes, sources.GetHermesStatus(ctx, http.DefaultClient, h))
	}
	in.HermesAt = now
	return in
}

func (t *Tools) whoami(ctx context.Context) (any, error) {
	sessions, _ := sources.ListSessions(ctx, t.Runner)
	names := make([]string, 0, len(sessions))
	for _, s := range sessions {
		names = append(names, s.Name)
	}

	return map[string]any{
		"name":         "cockpit",
		"version":      t.Version,
		"pid":          os.Getpid(),
		"port":         t.Port,
		"config_path":  t.ConfigPath,
		"session_name": t.Cfg.General.SessionName,
		"projects":     len(t.Cfg.Repos),
		"sessions":     names,
	}, nil
}

// statusEvent is one pattern match found in a process's scrollback.
type statusEvent struct {
	Type   string `json:"type"`
	Line   string `json:"line"`
	Match  string `json:"match"`
	Source string `json:"source"`
}

func (t *Tools) status(ctx context.Context, args map[string]any) (any, error) {
	repo, err := t.repo(args)
	if err != nil {
		return nil, err
	}

	limit := argInt(args, "limit", defaultStatusEvents)
	if limit <= 0 {
		limit = defaultStatusEvents
	}
	if limit > maxStatusEvents {
		limit = maxStatusEvents
	}

	targets := repo.Processes
	if name := argString(args, "process"); name != "" {
		p, ok := repo.Process(name)
		if !ok {
			return nil, fmt.Errorf("project %q has no process %q", repo.Label, name)
		}
		targets = []config.ProcessConfig{p}
	}

	type processStatus struct {
		Process string        `json:"process"`
		Events  []statusEvent `json:"events"`
		// Omitted is how many further matches the limit cut off, so a caller
		// knows whether it saw a window or the whole story.
		Omitted int `json:"omitted"`
	}

	out := make([]processStatus, 0, len(targets))
	for _, p := range targets {
		events, omitted := t.scanStatus(ctx, repo, p, limit)
		out = append(out, processStatus{
			Process: p.Name,
			Events:  events,
			Omitted: omitted,
		})
	}
	return map[string]any{"project": repo.Label, "processes": out}, nil
}

// scanStatus matches a process's status patterns against its scrollback,
// newest line first. Cockpit has no live event stream — tmux's history is the
// record — so events are labelled with where they came from.
func (t *Tools) scanStatus(ctx context.Context, repo config.RepoConfig, p config.ProcessConfig, limit int) ([]statusEvent, int) {
	patterns := p.Status.All()
	if len(patterns) == 0 {
		return []statusEvent{}, 0
	}

	compiled := make(map[string]*regexp.Regexp, len(patterns))
	for label, expr := range patterns {
		re, err := regexp.Compile(expr)
		if err != nil {
			continue
		}
		compiled[label] = re
	}

	out, err := t.Runner.Run(ctx, sources.CapturePaneArgs(sources.Target(repo.Label, p.Name), maxOutputLines)...)
	if err != nil {
		return []statusEvent{}, 0
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	events := []statusEvent{}
	omitted := 0
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		// Deterministic order when one line matches several patterns.
		for _, label := range []string{"error", "ready", "compiling", "restarting"} {
			re, ok := compiled[label]
			if !ok {
				continue
			}
			m := re.FindString(line)
			if m == "" {
				continue
			}
			// Keep counting past the limit so the caller learns how much it
			// did not see.
			if len(events) >= limit {
				omitted++
				break
			}
			events = append(events, statusEvent{
				Type:   label,
				Line:   strings.TrimSpace(line),
				Match:  m,
				Source: "scrollback",
			})
			break
		}
	}
	return events, omitted
}

// collapseBlankRuns trims leading and trailing blank lines and squeezes any
// interior run down to one. tmux pads a pane to its full height, so without
// this a one-line crash message comes back buried in forty blank lines the
// caller pays for.
func collapseBlankRuns(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank = true
			continue
		}
		if blank && len(out) > 0 {
			out = append(out, "")
		}
		blank = false
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// --- lookup helpers ---

// projectKey reads the project argument, qualified by an optional host. A
// bare label is a local project; "host/label", or a host argument, reaches
// a remote one. A bare label is never reinterpreted as a remote project.
func projectKey(args map[string]any) string {
	label := argString(args, "project")
	if host := argString(args, "host"); host != "" && label != "" && !strings.Contains(label, "/") {
		return host + "/" + label
	}
	return label
}

// repo resolves the "project" argument to a configured repo.
func (t *Tools) repo(args map[string]any) (config.RepoConfig, error) {
	key := projectKey(args)
	if key == "" {
		return config.RepoConfig{}, fmt.Errorf("project is required")
	}
	repo, _, err := t.Svc.Resolve(key)
	if err != nil {
		return config.RepoConfig{}, fmt.Errorf("unknown project %q", key)
	}
	return repo, nil
}

// process resolves both "project" and "process" to configured values.
func (t *Tools) process(args map[string]any) (config.RepoConfig, config.ProcessConfig, error) {
	repo, err := t.repo(args)
	if err != nil {
		return config.RepoConfig{}, config.ProcessConfig{}, err
	}
	name := argString(args, "process")
	if name == "" {
		return repo, config.ProcessConfig{}, fmt.Errorf("process is required")
	}
	p, ok := repo.Process(name)
	if !ok {
		return repo, config.ProcessConfig{}, fmt.Errorf("project %q has no process %q", repo.Label, name)
	}
	return repo, p, nil
}

// windowRef is a resolved window: its name, its id, and the pane target to
// address it by. Names are for display; the target is an identity.
type windowRef struct {
	name   string
	id     string
	target string
}

// window resolves a target window, accepting either a configured process or
// any window that is actually open — a spawned agent is not in the config but
// is still worth reading. It resolves through the observation so the pane
// addressed is the one seen, not the first window that shares a name.
func (t *Tools) window(ctx context.Context, args map[string]any) (config.RepoConfig, windowRef, error) {
	repo, err := t.repo(args)
	if err != nil {
		return config.RepoConfig{}, windowRef{}, err
	}
	name := argString(args, "process")
	paneID := argString(args, "pane_id")
	if name == "" && paneID == "" {
		return repo, windowRef{}, fmt.Errorf("process or pane_id is required")
	}
	obs, err := t.Svc.Inspect(ctx, repo.Key())
	if err != nil {
		return repo, windowRef{}, fmt.Errorf("project %q: cannot read windows: %w", repo.Key(), err)
	}
	if paneID != "" {
		// A pane id from cockpit_workspaces: it must belong to this
		// project's session, or it is refused rather than read blind.
		for _, p := range obs.Processes {
			if p.PaneID == paneID {
				return repo, windowRef{name: p.Name, id: p.WindowID, target: paneID}, nil
			}
		}
		return repo, windowRef{}, fmt.Errorf("project %q has no pane %s", repo.Key(), paneID)
	}
	for _, p := range obs.Processes {
		if p.Name != name || p.PaneID == "" && p.WindowIndex < 0 {
			continue
		}
		ref := windowRef{name: name, id: p.WindowID, target: p.PaneID}
		if ref.target == "" {
			// A fixture or an old tmux without ids: fall back to the name.
			ref.target = sources.Target(repo.Label, name)
		}
		return repo, ref, nil
	}
	return repo, windowRef{}, fmt.Errorf("project %q has no process or window %q", repo.Key(), name)
}

// --- argument helpers ---
//
// JSON decodes every number as a float64, so integers arrive that way.

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argInt(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func argBool(args map[string]any, key string, fallback bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return fallback
}

func argMap(args map[string]any, key string) map[string]string {
	raw, ok := args[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
