package sources

import "time"

// TmuxSession represents a single tmux session.
type TmuxSession struct {
	Name     string
	Windows  int
	Attached bool
	LastUsed time.Time
}

// GitRepoStatus represents the git status of a single repository.
type GitRepoStatus struct {
	Label      string
	Path       string
	Branch     string
	Dirty      bool
	DirtyCount int
	Unpushed   int
	LastCommit string
	Error      error
}
