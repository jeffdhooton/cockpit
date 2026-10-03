package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// checkHosts diagnoses the selected ssh hosts with bounded concurrency. It
// uses the system ssh in batch mode with a transient connection: no control
// socket is created in the config directory, no host key is accepted, no
// login prompt can appear.
func (c *collector) checkHosts() {
	if c.opts.NoHosts {
		return
	}
	if c.cfg == nil {
		if c.opts.Host != "" || c.opts.AllHosts {
			c.add(Check{ID: "hosts", Scope: "local", Status: Skip, Summary: "skipped: config not loaded"})
		}
		return
	}
	var hosts []config.HostConfig
	switch {
	case c.opts.AllHosts:
		hosts = c.cfg.Hosts
	case c.opts.Host != "":
		h, ok := c.cfg.Host(c.opts.Host)
		if !ok {
			c.add(Check{ID: "hosts", Scope: "local", Status: Fail, Core: true, Summary: "host " + c.opts.Host + " is not configured"})
			return
		}
		hosts = []config.HostConfig{h}
	default:
		if len(c.cfg.Hosts) > 0 {
			c.add(Check{ID: "hosts", Scope: "local", Status: Skip,
				Summary: fmt.Sprintf("%d configured; not checked", len(c.cfg.Hosts)),
				Remedy:  "cockpit doctor --all-hosts", Argv: []string{"cockpit", "doctor", "--all-hosts"}})
		}
		return
	}
	if len(hosts) == 0 {
		c.add(Check{ID: "hosts", Scope: "local", Status: Pass, Summary: "no hosts configured"})
		return
	}

	sem := make(chan struct{}, HostParallel)
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func(h config.HostConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c.checkHost(h)
		}(h)
	}
	wg.Wait()
	c.mu.Lock()
	sortChecks(c.report.Checks)
	c.mu.Unlock()
}

// sshArgs is the transient batch-mode invocation.
func sshArgs(host string, script string) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(SSHConnect.Seconds())),
		"--", host, script,
	}
}

func (c *collector) checkHost(h config.HostConfig) {
	scope := h.Name
	var repos []config.RepoConfig
	for _, r := range c.cfg.Repos {
		if r.Host == h.Name {
			repos = append(repos, r)
		}
	}

	reachable := false
	c.timed("ssh", scope, true, HostBudget, func(ctx context.Context) Check {
		out, exit, err := c.deps.Exec(ctx, "ssh", sshArgs(h.Name, "echo cockpit-doctor-ok")...)
		if err != nil || exit != 0 || !strings.Contains(out, "cockpit-doctor-ok") {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			ch := Check{Status: Fail, Summary: "ssh " + h.Name + " failed non-interactively", Evidence: []string{msg}}
			switch {
			case strings.Contains(msg, "Host key verification failed"):
				ch.Remedy = "Connect once by hand (ssh " + h.Name + ") to review and accept the host key; doctor never accepts keys."
			case strings.Contains(msg, "Permission denied"):
				ch.Remedy = "Check the key or IdentitiesOnly settings for this host in ~/.ssh/config."
			case strings.Contains(msg, "Could not resolve") || strings.Contains(msg, "timed out") || strings.Contains(msg, "No route"):
				ch.Remedy = "The host is not reachable; check the network or the alias in ~/.ssh/config."
			default:
				ch.Remedy = "Try ssh " + h.Name + " by hand and fix what it reports."
			}
			return ch
		}
		reachable = true
		return Check{Status: Pass, Summary: "connected non-interactively"}
	})
	if !reachable {
		c.add(Check{ID: "tmux", Scope: scope, Status: Skip, Summary: "skipped: ssh failed"})
		for _, r := range repos {
			c.add(Check{ID: "repo." + r.Label, Scope: scope, Status: Skip, Summary: "skipped: ssh failed"})
		}
		c.add(Check{ID: "cockpit", Scope: scope, Status: Skip, Summary: "skipped: ssh failed"})
		return
	}

	c.timed("tmux", scope, true, HostBudget, func(ctx context.Context) Check {
		tmux := sources.QuoteRemotePath(h.Tmux)
		script := "if [ -x " + tmux + " ]; then " + tmux + " -V && " + tmux + " list-sessions -F '#{session_name}' 2>&1; echo \"__exit=$?\"; else echo __missing; fi"
		out, _, err := c.deps.Exec(ctx, "ssh", sshArgs(h.Name, script)...)
		if err != nil {
			return Check{Status: Fail, Summary: "could not query remote tmux", Evidence: []string{err.Error()}}
		}
		if strings.Contains(out, "__missing") {
			return Check{Status: Fail, Summary: "remote tmux missing at " + h.Tmux,
				Remedy: "Install tmux there, or fix tmux = under [[hosts]] name = \"" + h.Name + "\"."}
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		version := ""
		if len(lines) > 0 {
			version = lines[0]
		}
		body := strings.Join(lines[1:], "\n")
		if sources.IsNoServer(fmt.Errorf("%s", body)) {
			return Check{Status: Pass, Summary: version + "; no server running (valid)"}
		}
		if strings.Contains(body, "__exit=0") {
			n := 0
			for _, l := range lines[1:] {
				if l != "" && !strings.HasPrefix(l, "__exit") {
					n++
				}
			}
			return Check{Status: Pass, Summary: fmt.Sprintf("%s; %d %s visible", version, n, plural(n, "session"))}
		}
		return Check{Status: Fail, Summary: "remote tmux could not be queried", Evidence: []string{clipStr(body)}}
	})

	for _, r := range repos {
		r := r
		c.timed("repo."+r.Label, scope, true, HostBudget, func(ctx context.Context) Check {
			path := sources.QuoteRemotePath(r.Path)
			script := "if [ -d " + path + " ]; then cd -- " + path + " && git rev-parse --git-dir >/dev/null 2>&1 && echo __git || echo __nogit; else echo __missing; fi"
			out, _, err := c.deps.Exec(ctx, "ssh", sshArgs(h.Name, script)...)
			if err != nil {
				return Check{Status: Fail, Summary: r.Label + ": could not check", Evidence: []string{err.Error()}}
			}
			switch {
			case strings.Contains(out, "__git"):
				return Check{Status: Pass, Summary: r.Label + ": readable git checkout"}
			case strings.Contains(out, "__nogit"):
				return Check{Status: Fail, Summary: r.Label + ": not a git checkout", Evidence: []string{r.Path}}
			default:
				return Check{Status: Fail, Summary: r.Label + ": path does not exist", Evidence: []string{r.Path}}
			}
		})
	}

	c.timed("cockpit", scope, false, HostBudget, func(ctx context.Context) Check {
		if h.Cockpit == "" {
			return Check{Status: Skip, Summary: "no remote cockpit path configured; session visibility and process control do not need one"}
		}
		bin := sources.QuoteRemotePath(h.Cockpit)
		script := "if [ -x " + bin + " ]; then " + bin + " doctor --json 2>/dev/null || " + bin + " version; else echo __missing; fi"
		out, _, err := c.deps.Exec(ctx, "ssh", sshArgs(h.Name, script)...)
		if err != nil && strings.TrimSpace(out) == "" {
			return Check{Status: Warn, Summary: "remote cockpit could not be run", Evidence: []string{err.Error()}}
		}
		if strings.Contains(out, "__missing") {
			return Check{Status: Warn, Summary: "remote cockpit missing at " + h.Cockpit, Remedy: "Install cockpit there or remove cockpit = under [[hosts]]."}
		}
		var remote Report
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &remote); err != nil || remote.SchemaVersion == 0 {
			// Older remote: version only.
			v := strings.TrimSpace(out)
			return Check{Status: Warn, Summary: "remote cockpit " + clipStr(v) + " predates doctor; hooks and daemon there unverified",
				Remedy: "Update cockpit on " + h.Name + " to the current release."}
		}
		if remote.SchemaVersion != SchemaVersion {
			return Check{Status: Warn, Summary: fmt.Sprintf("remote doctor schema %d differs from %d; details not merged", remote.SchemaVersion, SchemaVersion)}
		}
		ch := Check{Status: Pass, Summary: "remote cockpit " + remote.Version + " answered doctor"}
		if remote.Version != c.deps.Version {
			ch.Status = Warn
			ch.Evidence = append(ch.Evidence, "version mismatch: remote "+remote.Version+", local "+c.deps.Version+" (older clients do not honour stop overrides)")
			ch.Remedy = "Update cockpit on " + h.Name + " to match."
		}
		for _, rc := range remote.Checks {
			if rc.Scope != "local" {
				continue
			}
			sub := rc
			sub.ID = "remote." + rc.ID
			sub.Scope = scope
			sub.Core = false
			if sub.Status == Fail {
				// A remote core failure is a warning here: the host's
				// sessions are still visible over ssh.
				sub.Status = Warn
			}
			c.add(sub)
		}
		if !remote.CoreReady {
			ch.Status = Warn
			ch.Evidence = append(ch.Evidence, "remote core not ready; see remote.* checks")
		}
		return ch
	})
}
