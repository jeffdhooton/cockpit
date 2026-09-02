package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
)

// hostPoll is one pass over a remote host: its sessions, git statuses, and
// process states together, so a tile never shows fresh git beside stale
// sessions from a different pass. Err is set when the pass failed.
type hostPoll struct {
	Host      string
	Sessions  []sources.TmuxSession
	Repos     []sources.GitRepoStatus
	Processes map[string][]sources.ProcessInfo // repo key → processes
	Err       error
}

type hostDataMsg struct{ hostPoll }

// hostState is what the grid knows about a host between polls: the last data
// that arrived, and whether the link is currently down.
type hostState struct {
	poll        hostPoll
	failures    int
	unreachable bool
}

// backoffDelay is how long to wait before polling a host again after n
// consecutive transport failures: 5s, 30s, then 60s for as long as it lasts.
func backoffDelay(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 5 * time.Second
	case failures == 2:
		return 30 * time.Second
	default:
		return 60 * time.Second
	}
}

// mergeHost folds a poll result into the host's state. A transport failure
// keeps the last-known data and marks the host unreachable, so the grid
// never goes blank for a machine that was there a moment ago. Any other
// error is the remote side's own and is carried on the poll for the tile to
// name; it is not a dead link and does not back off.
func mergeHost(prev hostState, in hostPoll) hostState {
	if errors.Is(in.Err, sources.ErrHostUnreachable) {
		next := prev
		next.failures++
		next.unreachable = true
		next.poll.Err = in.Err
		return next
	}
	return hostState{poll: in}
}

// fetchHost polls one remote host. The clock is read first, on its own, which
// both establishes the multiplexed connection before anything fans out and
// supplies the timestamp remote status is aged against.
func (m Model) fetchHost(h config.HostConfig) tea.Cmd {
	var repos []config.RepoConfig
	for _, r := range m.config.Repos {
		if r.Host == h.Name {
			repos = append(repos, r)
		}
	}
	return func() tea.Msg {
		ctx := context.Background()
		r := sources.SSHRunner{Host: h.Name, Tmux: h.Tmux}

		now, err := r.RemoteNow(ctx)
		if err != nil {
			return hostDataMsg{hostPoll{Host: h.Name, Err: err}}
		}
		sessions, err := sources.ListSessionsOn(ctx, r, h.Name, now)
		if err != nil {
			return hostDataMsg{hostPoll{Host: h.Name, Err: err}}
		}

		poll := hostPoll{
			Host:      h.Name,
			Sessions:  sessions,
			Repos:     sources.GetGitStatus(ctx, r, repos),
			Processes: map[string][]sources.ProcessInfo{},
		}
		for _, repo := range repos {
			if len(repo.Processes) == 0 {
				continue
			}
			if infos, err := sources.InspectProcesses(ctx, r, repo); err == nil {
				poll.Processes[repo.Key()] = infos
			}
		}
		return hostDataMsg{poll}
	}
}

// hostTick schedules the next poll of a host, honouring its backoff.
func (m Model) hostTick(h config.HostConfig) tea.Cmd {
	d := time.Duration(m.config.General.RefreshInterval) * time.Second
	if st, ok := m.hosts[h.Name]; ok && st.unreachable {
		d = backoffDelay(st.failures)
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return hostTickMsg{Host: h.Name} })
}

type hostTickMsg struct{ Host string }

// remoteSessions, remoteRepos, and remoteProcesses flatten every host's
// last-known data for the grid.
func (m Model) remoteSessions() []sources.TmuxSession {
	var out []sources.TmuxSession
	for _, h := range m.config.Hosts {
		out = append(out, m.hosts[h.Name].poll.Sessions...)
	}
	return out
}

func (m Model) remoteRepos() []sources.GitRepoStatus {
	var out []sources.GitRepoStatus
	for _, h := range m.config.Hosts {
		st, polled := m.hosts[h.Name]
		if polled && len(st.poll.Repos) > 0 {
			out = append(out, st.poll.Repos...)
			continue
		}
		// Not polled yet, or the first poll failed: still show the configured
		// repos as dormant tiles rather than nothing.
		for _, r := range m.config.Repos {
			if r.Host == h.Name {
				out = append(out, sources.GitRepoStatus{Label: r.Label, Host: r.Host, Path: r.Path})
			}
		}
	}
	return out
}

func (m Model) remoteProcesses() map[string][]sources.ProcessInfo {
	out := map[string][]sources.ProcessInfo{}
	for _, st := range m.hosts {
		for k, v := range st.poll.Processes {
			out[k] = v
		}
	}
	return out
}

// localRepos is the configured repos on this machine, for the local git,
// process, and GitHub polls. Remote ones go through fetchHost.
func (m Model) localRepos() []config.RepoConfig {
	var out []config.RepoConfig
	for _, r := range m.config.Repos {
		if r.Host == "" {
			out = append(out, r)
		}
	}
	return out
}
