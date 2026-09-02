package sources

import "time"

// TmuxSession represents a single tmux session.
type TmuxSession struct {
	Name     string
	Windows  int
	Attached bool
	LastUsed time.Time
	// Status is the agent state. StatusReported separates a status an agent
	// hook actually sent from one the pane-hash fallback guessed, so the
	// display can mark a guess as a guess.
	Status         AgentStatus
	StatusReported bool
	// Host is the machine the session lives on; empty means local. ViewOf
	// marks a local session cockpit created purely to hold ssh windows onto
	// a remote host, which the grid must not render as a project.
	Host   string
	ViewOf string
}

// Key identifies the session across hosts: host/name remotely, name locally.
func (s TmuxSession) Key() string {
	if s.Host == "" {
		return s.Name
	}
	return s.Host + "/" + s.Name
}

// GitRepoStatus represents the git status of a single repository.
type GitRepoStatus struct {
	Label      string
	Host       string // empty means local
	Path       string
	Branch     string
	Dirty      bool
	DirtyCount int
	Unpushed   int
	Behind     int
	LastCommit string
	Error      error
}

// Key identifies the repo across hosts: host/label remotely, label locally.
func (g GitRepoStatus) Key() string {
	if g.Host == "" {
		return g.Label
	}
	return g.Host + "/" + g.Label
}
