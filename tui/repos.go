package tui

import "github.com/jeffdhooton/cockpit/sources"

// ReposModel holds the local git statuses the grid and the session view read.
type ReposModel struct {
	Repos   []sources.GitRepoStatus
	Loading bool
}

func NewReposModel() ReposModel {
	return ReposModel{Loading: true}
}
