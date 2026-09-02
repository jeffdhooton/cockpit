package sources

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jhoot/cockpit/config"
)

// CommandRunner runs a program in a directory and returns its stdout. Git
// goes through it so a repo on another machine is the same code down a
// different pipe.
type CommandRunner interface {
	RunIn(ctx context.Context, dir, name string, args ...string) (string, error)
}

// LocalCommandRunner runs commands on this machine.
type LocalCommandRunner struct{}

func (LocalCommandRunner) RunIn(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// GetGitStatus fetches git status for all configured repos in parallel.
func GetGitStatus(ctx context.Context, cr CommandRunner, repos []config.RepoConfig) []GitRepoStatus {
	results := make([]GitRepoStatus, len(repos))
	var wg sync.WaitGroup

	for i, repo := range repos {
		wg.Add(1)
		go func(idx int, r config.RepoConfig) {
			defer wg.Done()
			results[idx] = fetchOneRepo(ctx, cr, r)
		}(i, repo)
	}

	wg.Wait()
	return results
}

func fetchOneRepo(ctx context.Context, cr CommandRunner, repo config.RepoConfig) GitRepoStatus {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	status := GitRepoStatus{
		Label: repo.Label,
		Host:  repo.Host,
		Path:  repo.Path,
	}
	gitCommand := func(ctx context.Context, dir string, args ...string) (string, error) {
		return cr.RunIn(ctx, dir, "git", args...)
	}

	// Branch
	branch, err := gitCommand(ctx, repo.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		status.Error = err
		return status
	}
	status.Branch = strings.TrimSpace(branch)

	// Dirty count
	porcelain, err := gitCommand(ctx, repo.Path, "status", "--porcelain")
	if err != nil {
		status.Error = err
		return status
	}
	status.DirtyCount = parsePorcelainCount(porcelain)
	status.Dirty = status.DirtyCount > 0

	// Unpushed commits
	unpushed, err := gitCommand(ctx, repo.Path, "rev-list", "--count", "@{upstream}..HEAD")
	if err == nil {
		status.Unpushed = parseCount(strings.TrimSpace(unpushed))
	}

	// Behind remote
	behind, err := gitCommand(ctx, repo.Path, "rev-list", "--count", "HEAD..@{upstream}")
	if err == nil {
		status.Behind = parseCount(strings.TrimSpace(behind))
	}

	// Last commit message
	lastCommit, err := gitCommand(ctx, repo.Path, "log", "-1", "--pretty=format:%s")
	if err == nil {
		status.LastCommit = strings.TrimSpace(lastCommit)
	}

	return status
}

// parsePorcelainCount counts non-empty lines in git status --porcelain output.
func parsePorcelainCount(output string) int {
	output = strings.TrimSpace(output)
	if output == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// parseCount parses a single integer string, returning 0 on error.
func parseCount(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
