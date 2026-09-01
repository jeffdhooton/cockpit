package daemon

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
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
}

// NewTools builds the tool set backed by a config and a tmux runner.
func NewTools(cfg *config.Config, configPath string, r sources.Runner, version string, port int) *Tools {
	return &Tools{
		Cfg:        cfg,
		ConfigPath: configPath,
		Runner:     r,
		Version:    version,
		Port:       port,
		Now:        time.Now,
		Settle:     defaultSettleOptions(),
	}
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
		Path             string `json:"path"`
		SessionRunning   bool   `json:"session_running"`
		SessionAttached  bool   `json:"session_attached"`
		Windows          int    `json:"windows"`
		ProcessesRunning int    `json:"processes_running"`
		ProcessesDead    int    `json:"processes_dead"`
		ProcessesTotal   int    `json:"processes_total"`
	}

	projects := make([]project, 0, len(t.Cfg.Repos))
	for _, repo := range t.Cfg.Repos {
		s, running := live[repo.Label]
		p := project{
			Label:           repo.Label,
			Path:            repo.Path,
			SessionRunning:  running,
			SessionAttached: s.Attached,
			Windows:         s.Windows,
			ProcessesTotal:  len(repo.Processes),
		}
		infos, _ := sources.InspectProcesses(ctx, t.Runner, repo)
		for _, i := range infos {
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
		projects = append(projects, p)
	}
	return map[string]any{"projects": projects}, nil
}

func (t *Tools) listProcesses(ctx context.Context, args map[string]any) (any, error) {
	repo, err := t.repo(args)
	if err != nil {
		return nil, err
	}
	infos, err := sources.InspectProcesses(ctx, t.Runner, repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": repo.Label, "processes": infos}, nil
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

	out, err := t.Runner.Run(ctx, sources.CapturePaneArgs(sources.Target(repo.Label, window), lines)...)
	if err != nil {
		return nil, err
	}

	captured := countLines(out)
	collapsed := collapseBlankRuns(out)
	returned := countLines(collapsed)

	return map[string]any{
		"project": repo.Label,
		"process": window,
		"lines":   lines,
		"output":  collapsed,
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

	statuses := sources.GetGitStatus(ctx, repos)

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

func (t *Tools) signals(ctx context.Context) (any, error) {
	sessions, _ := sources.ListSessions(ctx, t.Runner)

	processes := map[string][]sources.ProcessInfo{}
	for _, repo := range t.Cfg.Repos {
		if len(repo.Processes) == 0 {
			continue
		}
		infos, err := sources.InspectProcesses(ctx, t.Runner, repo)
		if err != nil {
			continue
		}
		processes[repo.Label] = infos
	}

	in := sources.SignalInput{
		Config:    t.Cfg.Signals,
		Sessions:  sessions,
		Git:       sources.GetGitStatus(ctx, t.Cfg.Repos),
		Processes: processes,
		Now:       t.Now(),
	}
	if t.Cfg.GitHub.Enabled {
		in.GitHub = sources.GetGitHubStatus(ctx, t.Cfg.Repos)
	}

	signals := sources.ComputeSignals(in)
	if signals == nil {
		signals = []sources.Signal{}
	}
	return map[string]any{"signals": signals}, nil
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

// repo resolves the "project" argument to a configured repo.
func (t *Tools) repo(args map[string]any) (config.RepoConfig, error) {
	label := argString(args, "project")
	if label == "" {
		return config.RepoConfig{}, fmt.Errorf("project is required")
	}
	repo, ok := t.Cfg.Repo(label)
	if !ok {
		return config.RepoConfig{}, fmt.Errorf("unknown project %q", label)
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

// window resolves a target window, accepting either a configured process or
// any window that is actually open — a spawned agent is not in the config but
// is still worth reading.
func (t *Tools) window(ctx context.Context, args map[string]any) (config.RepoConfig, string, error) {
	repo, err := t.repo(args)
	if err != nil {
		return config.RepoConfig{}, "", err
	}
	name := argString(args, "process")
	if name == "" {
		return repo, "", fmt.Errorf("process is required")
	}
	if _, ok := repo.Process(name); ok {
		return repo, name, nil
	}

	windows, err := sources.ListWindows(ctx, t.Runner, repo.Label)
	if err == nil {
		for _, w := range windows {
			if w.Name == name {
				return repo, name, nil
			}
		}
	}
	return repo, "", fmt.Errorf("project %q has no process or window %q", repo.Label, name)
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
