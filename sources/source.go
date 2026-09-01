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
}

// GitRepoStatus represents the git status of a single repository.
type GitRepoStatus struct {
	Label      string
	Path       string
	Branch     string
	Dirty      bool
	DirtyCount int
	Unpushed   int
	Behind     int
	LastCommit string
	Error      error
}
