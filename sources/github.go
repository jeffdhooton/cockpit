package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jhoot/cockpit/config"
)

// GitHubStatus aggregates GitHub PR and CI data across all repos.
type GitHubStatus struct {
	PRsAwaitingReview int
	PRsDraft          int
	FailingChecks     int
	RepoChecks        []RepoCheck
	Error             error
}

// RepoCheck holds GitHub status for a single repo.
type RepoCheck struct {
	RepoLabel string
	PRCount   int
	CIStatus  string // "passing", "failing", "pending", "none"
}

type ghPR struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
}

type ghRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

var (
	httpsPattern = regexp.MustCompile(`github\.com[:/]([^/]+/[^/.\s]+?)(?:\.git)?$`)
	sshPattern   = regexp.MustCompile(`git@github\.com:([^/]+/[^/.\s]+?)(?:\.git)?$`)
)

// ParseGitHubRepo extracts "owner/repo" from a git remote URL.
func ParseGitHubRepo(remoteURL string) (string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if m := httpsPattern.FindStringSubmatch(remoteURL); m != nil {
		return m[1], nil
	}
	if m := sshPattern.FindStringSubmatch(remoteURL); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("cannot parse GitHub repo from %q", remoteURL)
}

// ParsePRList parses JSON output from gh pr list.
func ParsePRList(jsonBytes []byte) ([]ghPR, error) {
	var prs []ghPR
	if err := json.Unmarshal(jsonBytes, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// ParseRunList parses JSON output from gh run list.
func ParseRunList(jsonBytes []byte) ([]ghRun, error) {
	var runs []ghRun
	if err := json.Unmarshal(jsonBytes, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// GetGitHubStatus fetches PR and CI status for all configured repos.
func GetGitHubStatus(ctx context.Context, repos []config.RepoConfig) *GitHubStatus {
	status := &GitHubStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, repo := range repos {
		wg.Add(1)
		go func(r config.RepoConfig) {
			defer wg.Done()
			check := fetchRepoCheck(ctx, r)
			mu.Lock()
			defer mu.Unlock()
			status.RepoChecks = append(status.RepoChecks, check)
			status.PRsAwaitingReview += check.PRCount
			if check.CIStatus == "failing" {
				status.FailingChecks++
			}
		}(repo)
	}

	wg.Wait()
	return status
}

func fetchRepoCheck(ctx context.Context, repo config.RepoConfig) RepoCheck {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	check := RepoCheck{
		RepoLabel: repo.Label,
		CIStatus:  "none",
	}

	// Get remote URL to derive owner/repo
	remoteURL, err := gitCommand(ctx, repo.Path, "remote", "get-url", "origin")
	if err != nil {
		return check
	}
	ownerRepo, err := ParseGitHubRepo(remoteURL)
	if err != nil {
		return check
	}

	// Fetch PRs
	prOut, err := ghCommand(ctx, "pr", "list", "--repo", ownerRepo,
		"--state", "open", "--json", "number,title,isDraft,reviewDecision", "--limit", "10")
	if err == nil {
		prs, err := ParsePRList(prOut)
		if err == nil {
			for _, pr := range prs {
				if pr.ReviewDecision == "REVIEW_REQUIRED" {
					check.PRCount++
				}
				if pr.IsDraft {
					// Count drafts at status level handled by caller
				}
			}
		}
	}

	// Fetch CI status
	runOut, err := ghCommand(ctx, "run", "list", "--repo", ownerRepo,
		"--branch", "main", "--limit", "1", "--json", "status,conclusion")
	if err == nil {
		runs, err := ParseRunList(runOut)
		if err == nil && len(runs) > 0 {
			run := runs[0]
			switch {
			case run.Conclusion == "failure":
				check.CIStatus = "failing"
			case run.Conclusion == "success":
				check.CIStatus = "passing"
			case run.Status == "in_progress" || run.Status == "queued":
				check.CIStatus = "pending"
			}
		}
	}

	return check
}

func ghCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}
