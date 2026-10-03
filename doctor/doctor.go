// Package doctor diagnoses a cockpit installation. It reads and reports; it
// never repairs, installs, starts, creates, trusts or launches anything. Its
// dependencies are injectable so the collector can be exercised with spies
// that prove those absences.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// SchemaVersion is the version of the JSON document.
const SchemaVersion = 1

// Status is one check's outcome.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Budgets, from the spec. Variables so a test can shorten them.
var (
	SimpleBudget = 2 * time.Second
	SSHConnect   = 5 * time.Second
	HostBudget   = 10 * time.Second
	TotalBudget  = 30 * time.Second
)

const (
	HostParallel  = 3
	maxEvidence   = 6
	maxEvidenceCh = 200
	hookFreshFor  = 10 * time.Minute
)

// Check is one diagnostic result.
type Check struct {
	ID       string        `json:"id"`
	Scope    string        `json:"scope"`
	Status   Status        `json:"status"`
	Summary  string        `json:"summary"`
	Evidence []string      `json:"evidence,omitempty"`
	Duration time.Duration `json:"duration_ms"`
	Remedy   string        `json:"remedy,omitempty"`
	Argv     []string      `json:"argv,omitempty"`
	// Core marks a check whose failure means cockpit cannot work in the
	// requested scope. Optional checks warn instead of failing.
	Core bool `json:"core"`
}

// Report is the whole run.
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	Version       string         `json:"cockpit_version"`
	ConfigPath    string         `json:"config_path"`
	Scopes        []string       `json:"scopes"`
	Checks        []Check        `json:"checks"`
	Counts        map[Status]int `json:"counts"`
	CoreReady     bool           `json:"core_ready"`
	Incomplete    []string       `json:"incomplete,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	Duration      time.Duration  `json:"duration_ms"`
}

// Failed reports whether any check failed.
func (r Report) Failed() bool { return r.Counts[Fail] > 0 }

// Options select what to diagnose.
type Options struct {
	ConfigPath string
	Host       string // one configured host
	AllHosts   bool
	// NoHosts is set on the remote side so a remote doctor never recurses.
	NoHosts bool
}

// Exec runs a program with arguments under ctx and returns its stdout, exit
// code and error. Doctor only ever calls it for read-only commands.
type Exec func(ctx context.Context, name string, args ...string) (stdout string, exit int, err error)

// Deps are the injectable edges.
type Deps struct {
	Stat         func(string) (fs.FileInfo, error)
	ReadFile     func(string) ([]byte, error)
	EvalSymlinks func(string) (string, error)
	LookPath     func(string) (string, error)
	Exec         Exec
	HTTP         *http.Client
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
	Now          func() time.Time
	Executable   func() (string, error)
	Home         string
	Version      string
	// Progress receives one line per completed check, for stderr.
	Progress func(string)
}

// SystemDeps are the real edges.
func SystemDeps(version string) Deps {
	home, _ := os.UserHomeDir()
	return Deps{
		Stat:         os.Stat,
		ReadFile:     os.ReadFile,
		EvalSymlinks: filepath.EvalSymlinks,
		LookPath:     exec.LookPath,
		Exec:         systemExec,
		HTTP:         &http.Client{Timeout: SimpleBudget},
		Dial:         (&net.Dialer{Timeout: SimpleBudget}).DialContext,
		Now:          time.Now,
		Executable:   os.Executable,
		Home:         home,
		Version:      version,
	}
}

func systemExec(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "SSH_ASKPASS=", "DISPLAY=")
	out, err := cmd.Output()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
			msg := strings.TrimSpace(string(ee.Stderr))
			if msg != "" {
				err = errors.New(msg)
			}
		}
	}
	return string(out), exit, err
}

// collector carries the run.
type collector struct {
	opts   Options
	deps   Deps
	cfg    *config.Config
	cfgErr error
	tmux   string // resolved tmux path, "" when missing
	mu     sync.Mutex
	report Report
	ctx    context.Context
}

// Run performs the diagnosis. It always returns a report; an internal
// failure becomes a failed check rather than a panic.
func Run(ctx context.Context, opts Options, deps Deps) (rep Report) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	start := deps.Now()
	ctx, cancel := context.WithTimeout(ctx, TotalBudget)
	defer cancel()

	c := &collector{opts: opts, deps: deps, ctx: ctx}
	c.report = Report{SchemaVersion: SchemaVersion, Version: deps.Version, ConfigPath: opts.ConfigPath, StartedAt: start, Counts: map[Status]int{}}
	c.report.Scopes = []string{"local"}
	switch {
	case opts.AllHosts:
		c.report.Scopes = append(c.report.Scopes, "all-hosts")
	case opts.Host != "":
		c.report.Scopes = append(c.report.Scopes, "host:"+opts.Host)
	}

	defer func() {
		if r := recover(); r != nil {
			c.add(Check{ID: "internal", Scope: "local", Status: Fail, Core: true, Summary: "doctor failed internally", Evidence: []string{clipStr(fmt.Sprint(r))}})
		}
		c.finish()
		rep = c.report
	}()

	c.checkConfig()
	c.checkBinary()
	c.checkTmux()
	c.checkRepos()
	c.checkProcesses()
	c.checkDaemon()
	c.checkHooks()
	c.checkHookFreshness()
	c.checkGitHub()
	c.checkObsidian()
	c.checkHermes()
	c.checkSpinePath()
	c.checkSpineSnapshot()
	c.checkHosts()
	return c.report
}

func (c *collector) add(ch Check) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ch.Evidence) > maxEvidence {
		ch.Evidence = append(ch.Evidence[:maxEvidence], fmt.Sprintf("… %d more", len(ch.Evidence)-maxEvidence))
	}
	for i := range ch.Evidence {
		ch.Evidence[i] = clipStr(ch.Evidence[i])
	}
	ch.Summary = clipStr(ch.Summary)
	c.report.Checks = append(c.report.Checks, ch)
	c.report.Counts[ch.Status]++
	if c.deps.Progress != nil {
		c.deps.Progress(fmt.Sprintf("%-4s %-14s %s", strings.ToUpper(string(ch.Status)), ch.ID, ch.Summary))
	}
}

func (c *collector) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report.Duration = c.deps.Now().Sub(c.report.StartedAt)
	c.report.CoreReady = true
	for _, ch := range c.report.Checks {
		if ch.Core && ch.Status == Fail {
			c.report.CoreReady = false
		}
	}
}

// timed runs one check under a budget and records its duration. When the
// budget or the run deadline expires it records the check as timed out —
// fail for core, warn for optional — and lists it as incomplete.
func (c *collector) timed(id, scope string, core bool, budget time.Duration, fn func(ctx context.Context) Check) {
	if c.ctx.Err() != nil {
		c.incomplete(id, scope, core, "not run: the run deadline expired")
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, budget)
	defer cancel()
	start := c.deps.Now()
	done := make(chan Check, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- Check{Status: Fail, Summary: "check failed internally", Evidence: []string{clipStr(fmt.Sprint(r))}}
			}
		}()
		done <- fn(ctx)
	}()
	var ch Check
	select {
	case ch = <-done:
	case <-ctx.Done():
		// Give the check a moment to notice cancellation and report.
		select {
		case ch = <-done:
		case <-time.After(200 * time.Millisecond):
			c.incomplete(id, scope, core, "timed out after "+budget.String())
			return
		}
	}
	// A check that returned because its deadline expired is incomplete,
	// whatever it managed to say on the way out.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && ch.Status != Pass {
		c.incomplete(id, scope, core, "timed out after "+budget.String())
		return
	}
	ch.ID, ch.Scope, ch.Core = id, scope, core
	if ch.Status == "" {
		ch.Status = Pass
	}
	ch.Duration = c.deps.Now().Sub(start) / time.Millisecond
	c.add(ch)
}

func (c *collector) incomplete(id, scope string, core bool, why string) {
	status := Warn
	if core {
		status = Fail
	}
	c.mu.Lock()
	c.report.Incomplete = append(c.report.Incomplete, id)
	c.mu.Unlock()
	c.add(Check{ID: id, Scope: scope, Status: status, Core: core, Summary: why, Remedy: "Run again with a quieter system, or narrow the scope."})
}

func clipStr(s string) string {
	s = sources.StripControl(strings.TrimSpace(s))
	if len(s) > maxEvidenceCh {
		return s[:maxEvidenceCh-1] + "…"
	}
	return s
}

// --- local checks ---

func (c *collector) checkConfig() {
	c.timed("config", "local", true, SimpleBudget, func(ctx context.Context) Check {
		path := config.ExpandTilde(c.opts.ConfigPath)
		if _, err := c.deps.Stat(path); err != nil {
			c.cfgErr = err
			return Check{Status: Fail, Summary: "no config at " + path,
				Remedy: "Create one with cockpit init, or cockpit init --interactive to pick projects.", Argv: []string{"cockpit", "init", "--interactive"}}
		}
		raw, err := c.deps.ReadFile(path)
		if err != nil {
			c.cfgErr = err
			return Check{Status: Fail, Summary: "config unreadable", Evidence: []string{err.Error()}}
		}
		cfg, unknown, err := config.Parse(raw)
		if err != nil {
			c.cfgErr = err
			if strings.Contains(err.Error(), "parse error") {
				return Check{Status: Fail, Summary: "config does not parse", Evidence: []string{err.Error()}, Remedy: "Fix the TOML syntax at the reported line; existing values are kept as written."}
			}
			return Check{Status: Fail, Summary: "config is invalid", Evidence: []string{err.Error()}}
		}
		c.cfg = cfg
		ch := Check{Status: Pass, Summary: path}
		if len(unknown) > 0 {
			ch.Status = Warn
			ch.Summary = path + " (unknown keys ignored)"
			ch.Evidence = append([]string{"unknown keys: " + strings.Join(unknown, ", ")}, ch.Evidence...)
			ch.Remedy = "Remove or rename the unknown keys; they have no effect. The file is not rewritten."
		}
		if dups := duplicateLabels(cfg); len(dups) > 0 {
			ch.Status = Warn
			ch.Evidence = append(ch.Evidence, "duplicate labels: "+strings.Join(dups, ", "))
		}
		if cfg.Daemon.Port < 1 || cfg.Daemon.Port > 65535 {
			ch.Status = Fail
			ch.Evidence = append(ch.Evidence, fmt.Sprintf("daemon port %d is out of range", cfg.Daemon.Port))
		}
		return ch
	})
}

func duplicateLabels(cfg *config.Config) []string {
	seen := map[string]bool{}
	var dups []string
	for _, r := range cfg.Repos {
		if seen[r.Key()] {
			dups = append(dups, r.Key())
		}
		seen[r.Key()] = true
	}
	return dups
}

func (c *collector) checkBinary() {
	c.timed("binary", "local", true, SimpleBudget, func(ctx context.Context) Check {
		ch := Check{Status: Pass}
		exe, err := c.deps.Executable()
		if err == nil {
			ch.Evidence = append(ch.Evidence, "cockpit "+c.deps.Version+" at "+exe)
		}
		tmux, err := c.deps.LookPath("tmux")
		if err != nil {
			c.tmux = ""
			ch.Status = Fail
			ch.Summary = "tmux not found on PATH"
			ch.Remedy = "Install tmux (brew install tmux / apt install tmux) or add it to PATH."
			return ch
		}
		c.tmux = tmux
		ch.Summary = "tmux at " + tmux
		// A launcher pointing at a different or missing binary.
		if c.deps.Home != "" {
			plist := filepath.Join(c.deps.Home, "Library", "LaunchAgents", "com.jeffdhooton.cockpit.daemon.plist")
			if raw, err := c.deps.ReadFile(plist); err == nil {
				if bin := plistProgram(string(raw)); bin != "" {
					if _, err := c.deps.Stat(bin); err != nil {
						ch.Status = Warn
						ch.Evidence = append(ch.Evidence, "launch agent points at a missing binary: "+bin)
						ch.Remedy = "Run cockpit daemon install again from the binary you use."
					} else if exe != "" && bin != exe {
						if real, err := c.deps.EvalSymlinks(exe); err != nil || real != bin {
							ch.Status = Warn
							ch.Evidence = append(ch.Evidence, "launch agent runs a different binary: "+bin)
							ch.Remedy = "Run cockpit daemon install again so the launch agent uses this binary."
						}
					}
				}
			}
		}
		return ch
	})
}

// plistProgram reads the first ProgramArguments string from a launch agent.
func plistProgram(raw string) string {
	i := strings.Index(raw, "<key>ProgramArguments</key>")
	if i < 0 {
		return ""
	}
	rest := raw[i:]
	j := strings.Index(rest, "<string>")
	k := strings.Index(rest, "</string>")
	if j < 0 || k < 0 || k < j {
		return ""
	}
	return strings.TrimSpace(rest[j+len("<string>") : k])
}

func (c *collector) checkTmux() {
	c.timed("tmux", "local", true, SimpleBudget, func(ctx context.Context) Check {
		if c.tmux == "" {
			return Check{Status: Skip, Summary: "skipped: tmux binary not found (see binary)"}
		}
		out, _, err := c.deps.Exec(ctx, c.tmux, "list-sessions", "-F", "#{session_name}")
		if err != nil {
			msg := err.Error()
			switch {
			case sources.IsNoServer(err):
				return Check{Status: Pass, Summary: "no server running; nothing open yet (valid)"}
			case strings.Contains(msg, "ermission denied") || strings.Contains(msg, "not accessible"):
				return Check{Status: Fail, Summary: "tmux socket is not accessible", Evidence: []string{msg},
					Remedy: "Check the socket directory permissions (TMUX_TMPDIR or /tmp/tmux-UID)."}
			case strings.Contains(msg, "protocol version"):
				return Check{Status: Fail, Summary: "tmux client/server protocol mismatch", Evidence: []string{msg},
					Remedy: "The running server is from another tmux version; restart it when convenient."}
			}
			return Check{Status: Fail, Summary: "tmux could not be queried", Evidence: []string{msg}}
		}
		names := strings.Fields(strings.TrimSpace(out))
		return Check{Status: Pass, Summary: fmt.Sprintf("found; %d %s visible", len(names), plural(len(names), "session"))}
	})
}

func (c *collector) checkRepos() {
	if c.cfg == nil {
		c.add(Check{ID: "repos", Scope: "local", Status: Skip, Summary: "skipped: config not loaded"})
		return
	}
	local := 0
	for _, r := range c.cfg.Repos {
		if r.Host != "" {
			continue
		}
		local++
		repo := r
		c.timed("repo."+repo.Label, "local", true, SimpleBudget, func(ctx context.Context) Check {
			return c.checkRepo(ctx, repo)
		})
	}
	if local == 0 {
		c.add(Check{ID: "repos", Scope: "local", Status: Pass, Summary: "no local repositories configured; existing tmux sessions still show (valid)"})
	}
}

func (c *collector) checkRepo(ctx context.Context, repo config.RepoConfig) Check {
	path := repo.Path
	resolved, err := c.deps.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Check{Status: Fail, Summary: repo.Label + ": path does not exist", Evidence: []string{path},
				Remedy: "Fix the path under [[repos]] label = \"" + repo.Label + "\", or remove the entry."}
		}
		return Check{Status: Fail, Summary: repo.Label + ": path cannot be resolved (broken symlink or loop?)", Evidence: []string{path, err.Error()}}
	}
	info, err := c.deps.Stat(resolved)
	if err != nil || !info.IsDir() {
		return Check{Status: Fail, Summary: repo.Label + ": not a readable directory", Evidence: []string{resolved}}
	}
	if _, err := c.deps.LookPath("git"); err != nil {
		return Check{Status: Warn, Summary: repo.Label + ": directory readable; git not found so the checkout was not verified", Evidence: []string{resolved}}
	}
	out, _, err := c.deps.Exec(ctx, "git", "-C", resolved, "rev-parse", "--git-dir")
	if err != nil {
		return Check{Status: Fail, Summary: repo.Label + ": not a git checkout", Evidence: []string{resolved, err.Error()},
			Remedy: "Point the entry at a repository root or worktree."}
	}
	ev := []string{resolved}
	if resolved != path {
		ev = append(ev, "symlink from "+path)
	}
	gitDir := strings.TrimSpace(out)
	if strings.Contains(gitDir, "/worktrees/") {
		ev = append(ev, "git worktree")
	}
	return Check{Status: Pass, Summary: repo.Label + ": readable git checkout", Evidence: ev}
}

func (c *collector) checkProcesses() {
	if c.cfg == nil {
		c.add(Check{ID: "processes", Scope: "local", Status: Skip, Summary: "skipped: config not loaded"})
		return
	}
	total := 0
	for _, r := range c.cfg.Repos {
		total += len(r.Processes)
	}
	if total == 0 {
		c.add(Check{ID: "processes", Scope: "local", Status: Pass, Summary: "no processes declared"})
		return
	}
	c.timed("processes", "local", false, SimpleBudget, func(ctx context.Context) Check {
		ch := Check{Status: Pass, Summary: fmt.Sprintf("%d declared; names, commands and patterns valid", total)}
		for _, r := range c.cfg.Repos {
			if r.Host != "" {
				continue
			}
			for _, p := range r.Processes {
				if p.WorkingDir != "" {
					dir := p.ResolvedWorkingDir(r.Path)
					if _, err := c.deps.Stat(dir); err != nil {
						ch.Status = Warn
						ch.Evidence = append(ch.Evidence, r.Label+"/"+p.Name+": working_dir missing: "+dir)
					}
				}
			}
			if c.tmux == "" || len(r.Processes) == 0 {
				continue
			}
			// Name conflicts: a window with a process's name that cockpit did
			// not mark. Commands are not parsed or executed.
			out, _, err := c.deps.Exec(ctx, c.tmux, "list-windows", "-t", r.Label, "-F", "#{window_name}|#{@cockpit_managed}")
			if err != nil {
				continue
			}
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				name, managed, _ := strings.Cut(line, "|")
				if _, ok := r.Process(name); ok && managed == "" {
					ch.Status = Warn
					ch.Evidence = append(ch.Evidence, r.Label+": window "+name+" matches a process but was not started by cockpit (adopt it from the process panel)")
				}
			}
		}
		if ch.Status == Warn {
			ch.Summary = fmt.Sprintf("%d declared; see evidence", total)
		}
		return ch
	})
}

// daemonIdentity is what cockpit_whoami returns.
type daemonIdentity struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	ConfigPath string   `json:"config_path"`
	Sessions   []string `json:"sessions"`
}

func (c *collector) checkDaemon() {
	if c.cfg == nil {
		c.add(Check{ID: "daemon", Scope: "local", Status: Skip, Summary: "skipped: config not loaded"})
		return
	}
	if !c.cfg.Daemon.IsEnabled() {
		c.add(Check{ID: "daemon", Scope: "local", Status: Skip, Summary: "disabled"})
		return
	}
	c.timed("daemon", "local", false, SimpleBudget, func(ctx context.Context) Check {
		addr := fmt.Sprintf("127.0.0.1:%d", c.cfg.Daemon.Port)
		conn, err := c.deps.Dial(ctx, "tcp", addr)
		if err != nil {
			return Check{Status: Warn, Summary: "not responding on " + addr + "; hook status unavailable",
				Remedy: "Start it with cockpit daemon start (or cockpit daemon install for login).", Argv: []string{"cockpit", "daemon", "start"}}
		}
		_ = conn.Close()
		id, err := c.daemonWhoami(ctx, addr)
		if err != nil {
			return Check{Status: Warn, Summary: "port " + addr + " answers but is not cockpit's daemon", Evidence: []string{err.Error()},
				Remedy: "Something else owns the port; change [daemon] port or stop that service."}
		}
		if id.Name != "cockpit" {
			return Check{Status: Warn, Summary: "port " + addr + " serves " + id.Name + ", not cockpit"}
		}
		ch := Check{Status: Pass, Summary: "cockpit " + id.Version + " on " + addr}
		want := config.ExpandTilde(c.opts.ConfigPath)
		if id.ConfigPath != "" && id.ConfigPath != want {
			ch.Status = Warn
			ch.Summary = "daemon on " + addr + " uses another config"
			ch.Evidence = append(ch.Evidence, "daemon config: "+id.ConfigPath, "this config: "+want)
			ch.Remedy = "Restart the daemon with --config " + want + " or run doctor with the daemon's config."
		}
		if id.Version != "" && c.deps.Version != "" && id.Version != c.deps.Version {
			ch.Status = Warn
			ch.Evidence = append(ch.Evidence, "daemon version "+id.Version+" differs from this binary "+c.deps.Version+"; older clients do not honour stop overrides")
			ch.Remedy = "Restart the daemon from the current binary: cockpit daemon stop && cockpit daemon start."
		}
		// Server scope: the daemon's session names against the local server's.
		if c.tmux != "" {
			out, _, err := c.deps.Exec(ctx, c.tmux, "list-sessions", "-F", "#{session_name}")
			if err == nil {
				local := map[string]bool{}
				for _, n := range strings.Fields(out) {
					local[n] = true
				}
				missing := 0
				for _, n := range id.Sessions {
					if !local[n] {
						missing++
					}
				}
				if missing > 0 || (len(local) > 0 && len(id.Sessions) == 0) {
					ch.Status = Warn
					ch.Evidence = append(ch.Evidence, "daemon and TUI see different tmux sessions; they may use different servers or PATHs")
				}
			}
		}
		return ch
	})
}

func (c *collector) daemonWhoami(ctx context.Context, addr string) (daemonIdentity, error) {
	var id daemonIdentity
	client := c.deps.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cockpit_whoami","arguments":{}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/mcp", strings.NewReader(body))
	if err != nil {
		return id, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return id, err
	}
	defer resp.Body.Close()
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&rpc); err != nil {
		return id, fmt.Errorf("not a JSON-RPC reply: %v", err)
	}
	if rpc.Result.IsError || len(rpc.Result.Content) == 0 {
		return id, errors.New("no whoami result")
	}
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &id); err != nil {
		return id, fmt.Errorf("whoami not parseable: %v", err)
	}
	return id, nil
}

// checkHooks inspects installed engines' cockpit hooks. It reads config
// files only; it never approves, edits or spawns.
func (c *collector) checkHooks() {
	if c.deps.Home == "" {
		c.add(Check{ID: "hooks", Scope: "local", Status: Skip, Summary: "skipped: home directory unknown"})
		return
	}
	exe, _ := c.deps.Executable()
	if real, err := c.deps.EvalSymlinks(exe); err == nil && real != "" {
		exe = real
	}
	claude := filepath.Join(c.deps.Home, ".claude", "settings.json")
	codex := filepath.Join(c.deps.Home, ".codex", "config.toml")

	if _, err := c.deps.Stat(filepath.Dir(claude)); err != nil {
		c.add(Check{ID: "hooks.claude", Scope: "local", Status: Skip, Summary: "Claude Code not installed here"})
	} else {
		c.timed("hooks.claude", "local", false, SimpleBudget, func(ctx context.Context) Check {
			return c.checkClaudeHooks(claude, exe)
		})
	}
	if _, err := c.deps.Stat(filepath.Dir(codex)); err != nil {
		c.add(Check{ID: "hooks.codex", Scope: "local", Status: Skip, Summary: "Codex not installed here"})
	} else {
		c.timed("hooks.codex", "local", false, SimpleBudget, func(ctx context.Context) Check {
			return c.checkCodexHooks(codex, exe)
		})
	}
}

var (
	claudeEvents = []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop"}
	codexEvents  = []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop"}
)

func (c *collector) checkClaudeHooks(path, exe string) Check {
	raw, err := c.deps.ReadFile(path)
	if err != nil {
		return Check{Status: Warn, Summary: "no Claude settings; hooks not installed", Remedy: "cockpit hook install", Argv: []string{"cockpit", "hook", "install"}}
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return Check{Status: Warn, Summary: "Claude settings do not parse", Evidence: []string{err.Error()}}
	}
	installed := 0
	var paths []string
	for _, event := range claudeEvents {
		for _, g := range settings.Hooks[event] {
			for _, h := range g.Hooks {
				if strings.Contains(h.Command, " hook status") {
					installed++
					bin := strings.TrimSpace(strings.TrimSuffix(h.Command, " hook status"))
					paths = append(paths, bin)
					break
				}
			}
		}
	}
	if installed == 0 {
		return Check{Status: Warn, Summary: "hooks not installed", Remedy: "cockpit hook install", Argv: []string{"cockpit", "hook", "install"}}
	}
	ch := Check{Status: Pass, Summary: fmt.Sprintf("%d/%d events installed; trust not applicable to user settings", installed, len(claudeEvents))}
	if installed < len(claudeEvents) {
		ch.Status = Warn
		ch.Remedy = "cockpit hook install adds the missing events."
	}
	c.checkHookBinary(&ch, paths, exe)
	return ch
}

// checkHookBinary verifies the hook's command path exists and matches the
// running binary; a stale path means events go nowhere.
func (c *collector) checkHookBinary(ch *Check, paths []string, exe string) {
	for _, p := range uniq(paths) {
		if _, err := c.deps.Stat(p); err != nil {
			ch.Status = Warn
			ch.Evidence = append(ch.Evidence, "hook command missing: "+p)
			ch.Remedy = "cockpit hook install rewrites the command path."
			continue
		}
		if exe != "" && p != exe {
			ch.Evidence = append(ch.Evidence, "hook runs "+p+", this binary is "+exe+" (stale if versions differ)")
		}
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (c *collector) checkCodexHooks(path, exe string) Check {
	raw, err := c.deps.ReadFile(path)
	if err != nil {
		return Check{Status: Warn, Summary: "no Codex config; hooks not installed", Remedy: "cockpit hook install", Argv: []string{"cockpit", "hook", "install"}}
	}
	var parsed struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
				Enabled     *bool  `toml:"enabled"`
			} `toml:"state"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(string(raw), &parsed); err != nil {
		return Check{Status: Warn, Summary: "Codex config does not parse", Evidence: []string{err.Error()}}
	}
	// Locate cockpit's entries per event with their group/hook indexes, the
	// way Codex keys its trust state: file:event:group:hook.
	var generic map[string]any
	_, _ = toml.Decode(string(raw), &generic)
	hooks, _ := generic["hooks"].(map[string]any)
	installed, trusted, disabled := 0, 0, 0
	var paths []string
	for _, event := range codexEvents {
		groups, _ := hooks[event].([]map[string]any)
		if groups == nil {
			if raw, ok := hooks[event].([]any); ok {
				for _, g := range raw {
					if gm, ok := g.(map[string]any); ok {
						groups = append(groups, gm)
					}
				}
			}
		}
		for gi, g := range groups {
			entries, _ := g["hooks"].([]map[string]any)
			if entries == nil {
				if raw, ok := g["hooks"].([]any); ok {
					for _, e := range raw {
						if em, ok := e.(map[string]any); ok {
							entries = append(entries, em)
						}
					}
				}
			}
			for hi, e := range entries {
				cmd, _ := e["command"].(string)
				if !strings.Contains(cmd, " hook status") {
					continue
				}
				installed++
				bin, _, _ := strings.Cut(cmd, " hook status")
				paths = append(paths, strings.TrimSpace(bin))
				key := fmt.Sprintf("%s:%s:%d:%d", path, snake(event), gi, hi)
				if st, ok := parsed.Hooks.State[key]; ok {
					if st.Enabled != nil && !*st.Enabled {
						disabled++
					} else if st.TrustedHash != "" {
						trusted++
					}
				}
			}
		}
	}
	if installed == 0 {
		return Check{Status: Warn, Summary: "hooks not installed", Remedy: "cockpit hook install", Argv: []string{"cockpit", "hook", "install"}}
	}
	ch := Check{Status: Pass}
	switch {
	case disabled > 0:
		ch.Status = Warn
		ch.Summary = fmt.Sprintf("%d/%d events installed, %d disabled in Codex", installed, len(codexEvents), disabled)
		ch.Remedy = "Enable them in Codex's hooks view, or run cockpit hook install."
	case trusted == installed:
		ch.Summary = fmt.Sprintf("%d/%d events installed and trusted (hash currency not verifiable without Codex)", installed, len(codexEvents))
	default:
		ch.Status = Warn
		ch.Summary = fmt.Sprintf("%d/%d events installed; trust unverified for %d", installed, len(codexEvents), installed-trusted)
		ch.Remedy = "Open codex, review its hooks view and approve cockpit's hooks, or run cockpit hook install (which records trust through codex app-server)."
		ch.Argv = []string{"cockpit", "hook", "install"}
	}
	if installed < len(codexEvents) {
		ch.Status = Warn
		ch.Evidence = append(ch.Evidence, fmt.Sprintf("%d of %d events missing", len(codexEvents)-installed, len(codexEvents)))
	}
	c.checkHookBinary(&ch, paths, exe)
	return ch
}

func snake(event string) string {
	var b strings.Builder
	for i, r := range event {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r | 0x20)
	}
	return b.String()
}

// checkHookFreshness looks for a real recent report in tmux. Nothing seen is
// "not yet observed", which proves neither delivery nor breakage.
func (c *collector) checkHookFreshness() {
	if c.tmux == "" {
		c.add(Check{ID: "hooks.delivery", Scope: "local", Status: Skip, Summary: "skipped: tmux not found"})
		return
	}
	c.timed("hooks.delivery", "local", false, SimpleBudget, func(ctx context.Context) Check {
		now := c.deps.Now()
		sessOut, _, err := c.deps.Exec(ctx, c.tmux, "list-sessions", "-F", "#{session_name}|#{@cockpit_status}|#{@cockpit_status_at}")
		if err != nil {
			if sources.IsNoServer(err) {
				return Check{Status: Skip, Summary: "no tmux server; delivery not yet observed"}
			}
			return Check{Status: Skip, Summary: "tmux unreadable; delivery not observed", Evidence: []string{err.Error()}}
		}
		paneOut, _, _ := c.deps.Exec(ctx, c.tmux, "list-panes", "-a", "-F", "#{pane_id}|#{@cockpit_pane_status}|#{@cockpit_pane_status_at}")
		freshPane, freshSession := 0, 0
		var latest time.Time
		for _, line := range strings.Split(strings.TrimSpace(paneOut), "\n") {
			parts := strings.Split(line, "|")
			if len(parts) < 3 || parts[1] == "" {
				continue
			}
			if at, ok := parseEpoch(parts[2]); ok && now.Sub(at) <= hookFreshFor {
				freshPane++
				if at.After(latest) {
					latest = at
				}
			}
		}
		for _, line := range strings.Split(strings.TrimSpace(sessOut), "\n") {
			parts := strings.Split(line, "|")
			if len(parts) < 3 || parts[1] == "" {
				continue
			}
			if at, ok := parseEpoch(parts[2]); ok && now.Sub(at) <= hookFreshFor {
				freshSession++
				if at.After(latest) {
					latest = at
				}
			}
		}
		switch {
		case freshPane > 0:
			return Check{Status: Pass, Summary: fmt.Sprintf("pane-level report delivered %s ago (%d %s)", now.Sub(latest).Round(time.Second), freshPane, plural(freshPane, "pane"))}
		case freshSession > 0:
			return Check{Status: Warn, Summary: fmt.Sprintf("session-level report delivered %s ago; limited precision (older hook)", now.Sub(latest).Round(time.Second)),
				Remedy: "Update the hooks on this machine: cockpit hook install with the current binary.", Argv: []string{"cockpit", "hook", "install"}}
		}
		return Check{Status: Skip, Summary: "delivery not yet observed (no report in the last 10m); not evidence of a fault"}
	})
}

func parseEpoch(s string) (time.Time, bool) {
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil || n == 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

func (c *collector) checkGitHub() {
	if c.cfg == nil {
		c.add(Check{ID: "github", Scope: "local", Status: Skip, Summary: "skipped: config not loaded"})
		return
	}
	if !c.cfg.GitHub.Enabled {
		c.add(Check{ID: "github", Scope: "local", Status: Skip, Summary: "disabled"})
		return
	}
	c.timed("github", "local", false, HostBudget, func(ctx context.Context) Check {
		gh, err := c.deps.LookPath("gh")
		if err != nil {
			return Check{Status: Warn, Summary: "gh not found; CI and PR status unavailable", Remedy: "Install the GitHub CLI or set enabled = false under [github]."}
		}
		if _, exit, err := c.deps.Exec(ctx, gh, "auth", "status"); err != nil || exit != 0 {
			return Check{Status: Warn, Summary: "gh is not authenticated", Remedy: "gh auth login", Argv: []string{"gh", "auth", "login"}}
		}
		ch := Check{Status: Pass}
		checked, remote := 0, 0
		for _, r := range c.cfg.Repos {
			if r.Host != "" {
				remote++
				continue
			}
			if checked >= 3 {
				break
			}
			out, _, err := c.deps.Exec(ctx, "git", "-C", r.Path, "remote", "get-url", "origin")
			if err != nil {
				continue
			}
			ownerRepo, err := sources.ParseGitHubRepo(out)
			if err != nil {
				continue
			}
			checked++
			if _, exit, err := c.deps.Exec(ctx, gh, "repo", "view", ownerRepo, "--json", "nameWithOwner"); err != nil || exit != 0 {
				ch.Status = Warn
				ch.Evidence = append(ch.Evidence, r.Label+": "+ownerRepo+" not accessible with the current gh login")
			}
		}
		ch.Summary = fmt.Sprintf("authenticated; %d %s reachable (CI checked on main only)", checked, plural(checked, "repo"))
		if remote > 0 {
			ch.Evidence = append(ch.Evidence, fmt.Sprintf("%d remote %s not checked: remote CI is unsupported", remote, plural(remote, "repo")))
		}
		return ch
	})
}

func (c *collector) checkObsidian() {
	if c.cfg == nil {
		c.add(Check{ID: "obsidian", Scope: "local", Status: Skip, Summary: "skipped: config not loaded"})
		return
	}
	if !c.cfg.Obsidian.Enabled() {
		c.add(Check{ID: "obsidian", Scope: "local", Status: Skip, Summary: "not configured"})
		return
	}
	c.timed("obsidian", "local", false, SimpleBudget, func(ctx context.Context) Check {
		ch := Check{Status: Pass, Summary: "task files readable; write access inferred from permissions, not tested"}
		for name, p := range map[string]string{"today_file": c.cfg.Obsidian.TodayFile, "inbox_file": c.cfg.Obsidian.InboxFile} {
			if p == "" {
				continue
			}
			info, err := c.deps.Stat(p)
			if err != nil {
				ch.Status = Warn
				ch.Evidence = append(ch.Evidence, name+" missing: "+p)
				ch.Remedy = "Create the file, or fix the path under [obsidian]."
				continue
			}
			if info.Mode().Perm()&0o200 == 0 {
				ch.Status = Warn
				ch.Evidence = append(ch.Evidence, name+" is not writable by permission bits: "+p)
			}
		}
		return ch
	})
}

func (c *collector) checkHermes() {
	if c.cfg == nil || len(c.cfg.Hermes) == 0 {
		c.add(Check{ID: "hermes", Scope: "local", Status: Skip, Summary: "not configured"})
		return
	}
	for _, h := range c.cfg.Hermes {
		h := h
		c.timed("hermes."+h.Label, "local", false, SimpleBudget, func(ctx context.Context) Check {
			client := c.deps.HTTP
			if client == nil {
				client = http.DefaultClient
			}
			st := sources.GetHermesStatus(ctx, client, h)
			if !st.Reachable {
				ev := []string{}
				if st.Err != nil {
					ev = append(ev, st.Err.Error())
				}
				return Check{Status: Warn, Summary: h.Label + ": dashboard unreachable", Evidence: ev}
			}
			if st.Gateway != "running" {
				return Check{Status: Warn, Summary: h.Label + ": gateway " + st.Gateway}
			}
			return Check{Status: Pass, Summary: h.Label + ": gateway running (" + strings.Join(st.Platforms, " ") + ")"}
		})
	}
}

func (c *collector) checkSpinePath() {
	c.timed("spine.path", "local", false, SimpleBudget, func(ctx context.Context) Check {
		_, err := c.deps.LookPath("spine")
		if err != nil {
			return Check{Status: Warn, Summary: "spine not found on PATH", Remedy: "Install spine or add it to PATH."}
		}
		return Check{Status: Pass, Summary: "spine found on PATH"}
	})
}

func (c *collector) checkSpineSnapshot() {
	c.timed("spine.snapshot", "local", false, SimpleBudget, func(ctx context.Context) Check {
		spinePath, err := c.deps.LookPath("spine")
		if err != nil {
			return Check{Status: Skip, Summary: "skipped: spine not found on PATH (see spine.path)"}
		}
		out, exit, err := c.deps.Exec(ctx, spinePath, "bearings", "--json")
		if err != nil || exit != 0 {
			ev := []string{}
			if exit != 0 {
				ev = append(ev, fmt.Sprintf("exit code %d", exit))
			}
			if err != nil {
				ev = append(ev, err.Error())
			}
			return Check{Status: Warn, Summary: "spine bearings --json failed", Evidence: ev, Remedy: "Run spine bearings --json to see the error."}
		}
		var bearings struct {
			NeedsYou    json.RawMessage `json:"needs_you"`
			Underway    json.RawMessage `json:"underway"`
			ChartedNext json.RawMessage `json:"charted_next"`
			Landed      json.RawMessage `json:"landed"`
		}
		if err := json.Unmarshal([]byte(out), &bearings); err != nil {
			return Check{Status: Warn, Summary: "spine bearings output is not valid JSON", Evidence: []string{err.Error()}, Remedy: "Run spine bearings --json to see the error."}
		}
		if bearings.NeedsYou == nil || bearings.Underway == nil || bearings.ChartedNext == nil || bearings.Landed == nil {
			return Check{Status: Warn, Summary: "spine bearings missing required fields", Evidence: []string{"check needs_you, underway, charted_next, landed"}, Remedy: "Run spine bearings --json to verify the snapshot."}
		}
		return Check{Status: Pass, Summary: "spine bearings readable"}
	})
}

func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// sortChecks keeps a stable order for rendering: local first in run order,
// then hosts alphabetically.
func sortChecks(checks []Check) {
	sort.SliceStable(checks, func(i, j int) bool {
		li, lj := checks[i].Scope == "local", checks[j].Scope == "local"
		if li != lj {
			return li
		}
		if !li && checks[i].Scope != checks[j].Scope {
			return checks[i].Scope < checks[j].Scope
		}
		return false
	})
}
