package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeffdhooton/cockpit/config"
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
	// Coverage says what the check actually established. A remote repo is
	// not checked at all; a local one whose remote or gh call failed is
	// an error, not a passing repo.
	Coverage string
	Err      error
	// Identity of the run behind CIStatus, for navigation.
	Repo   string // owner/name
	Branch string // the branch checked
	RunID  string
	RunURL string
}

// Coverage values for RepoCheck.
const (
	CoverageChecked           = "checked"
	CoverageRemoteUnsupported = "remote_unsupported"
	CoverageNoGitHubRemote    = "no_github_remote"
	CoverageError             = "error"
)

// ciBranch is the one branch V1 checks. It is labelled everywhere the result
// is shown, so a green main is never mistaken for a green feature branch.
const ciBranch = "main"

type ghPR struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
}

type ghRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	ID         int64  `json:"databaseId"`
	URL        string `json:"url"`
	HeadBranch string `json:"headBranch"`
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
		Branch:    ciBranch,
	}

	// GitHub checks derive owner/repo from a local checkout and run gh here.
	// A remote repo has no local checkout to ask, so it is reported as
	// unchecked rather than checked against the wrong path.
	if repo.Host != "" {
		check.Coverage = CoverageRemoteUnsupported
		return check
	}
	remoteURL, err := LocalCommandRunner{}.RunIn(ctx, repo.Path, "git", "remote", "get-url", "origin")
	if err != nil {
		check.Coverage = CoverageError
		check.Err = fmt.Errorf("git remote: %w", err)
		return check
	}
	ownerRepo, err := ParseGitHubRepo(remoteURL)
	if err != nil {
		check.Coverage = CoverageNoGitHubRemote
		return check
	}
	check.Repo = ownerRepo
	check.Coverage = CoverageChecked

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
			}
		}
	}

	// Fetch CI status. A failed fetch is an error, never "none": an
	// unreadable run is not a passing one.
	runOut, err := ghCommand(ctx, "run", "list", "--repo", ownerRepo,
		"--branch", ciBranch, "--limit", "1", "--json", "status,conclusion,databaseId,url,headBranch")
	if err != nil {
		check.Coverage = CoverageError
		check.Err = fmt.Errorf("gh run list: %w", err)
		return check
	}
	runs, err := ParseRunList(runOut)
	if err != nil {
		check.Coverage = CoverageError
		check.Err = fmt.Errorf("gh run list: %w", err)
		return check
	}
	if len(runs) > 0 {
		run := runs[0]
		if run.ID != 0 {
			check.RunID = strconv.FormatInt(run.ID, 10)
		}
		check.RunURL = run.URL
		if run.HeadBranch != "" {
			check.Branch = run.HeadBranch
		}
		switch {
		case run.Conclusion == "failure":
			check.CIStatus = "failing"
		case run.Conclusion == "success":
			check.CIStatus = "passing"
		case run.Status == "in_progress" || run.Status == "queued":
			check.CIStatus = "pending"
		}
	}

	return check
}

func ghCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}
