package tui

import (
	"sort"

	"github.com/jhoot/cockpit/sources"
)

// Target is one tile in the grid: a running tmux session, a saved repo with no
// session, or both joined on session.Name == repo.Label — the same identity
// tmuxJump already assumes when it switches to a session named for a repo.
type Target struct {
	Label   string
	Session *sources.TmuxSession
	Repo    *sources.GitRepoStatus
	Status  sources.ClaudeStatus
}

// Running reports whether the target has a live tmux session behind it.
func (t Target) Running() bool { return t.Session != nil }

// BuildTargets joins sessions and repos into one ordered tile list. Running
// targets come first, then dormant, alphabetical within each group. Ordering is
// deliberately not last-used: on a 5-second refresh that would move the tile
// under the cursor while the user is aiming at it.
func BuildTargets(
	sessions []sources.TmuxSession,
	repos []sources.GitRepoStatus,
	statuses map[string]sources.ClaudeStatus,
	selfSession string,
) []Target {
	repoByLabel := make(map[string]*sources.GitRepoStatus, len(repos))
	for i := range repos {
		repoByLabel[repos[i].Label] = &repos[i]
	}

	var running, dormant []Target
	live := make(map[string]bool, len(sessions))

	for i := range sessions {
		s := &sessions[i]
		if s.Name == selfSession {
			continue
		}
		live[s.Name] = true
		running = append(running, Target{
			Label:   s.Name,
			Session: s,
			Repo:    repoByLabel[s.Name],
			Status:  statuses[s.Name],
		})
	}

	for i := range repos {
		r := &repos[i]
		if live[r.Label] {
			continue
		}
		dormant = append(dormant, Target{Label: r.Label, Repo: r})
	}

	sort.Slice(running, func(i, j int) bool { return running[i].Label < running[j].Label })
	sort.Slice(dormant, func(i, j int) bool { return dormant[i].Label < dormant[j].Label })

	return append(running, dormant...)
}
