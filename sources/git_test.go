package sources

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jhoot/cockpit/config"
)

func TestParsePortelainCount(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect int
	}{
		{"empty", "", 0},
		{"one file", " M file.go\n", 1},
		{"two files", " M file.go\n?? new.go\n", 2},
		{"whitespace only", "  \n  \n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePorcelainCount(tt.input)
			if got != tt.expect {
				t.Errorf("parsePorcelainCount(%q) = %d, want %d", tt.input, got, tt.expect)
			}
		})
	}
}

func TestParseCount(t *testing.T) {
	tests := []struct {
		input  string
		expect int
	}{
		{"0", 0},
		{"5", 5},
		{"abc", 0},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseCount(tt.input)
			if got != tt.expect {
				t.Errorf("parseCount(%q) = %d, want %d", tt.input, got, tt.expect)
			}
		})
	}
}

func TestGetGitStatusIntegration(t *testing.T) {
	// Create a temp git repo
	dir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("checkout", "-b", "main")

	// Create a file and commit
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)
	runGit("add", "hello.go")
	runGit("commit", "-m", "initial commit")

	// Create an untracked file to make it dirty
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty\n"), 0644)

	repos := []config.RepoConfig{{Path: dir, Label: "test-repo"}}
	results := GetGitStatus(context.Background(), LocalCommandRunner{}, repos)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Branch != "main" {
		t.Errorf("branch = %q, want %q", r.Branch, "main")
	}
	if !r.Dirty {
		t.Error("expected dirty = true")
	}
	if r.DirtyCount != 1 {
		t.Errorf("dirty_count = %d, want 1", r.DirtyCount)
	}
	if r.LastCommit != "initial commit" {
		t.Errorf("last_commit = %q, want %q", r.LastCommit, "initial commit")
	}
	if r.Label != "test-repo" {
		t.Errorf("label = %q, want %q", r.Label, "test-repo")
	}
}

func TestGetGitStatusInvalidPath(t *testing.T) {
	repos := []config.RepoConfig{{Path: "/nonexistent/repo", Label: "bad"}}
	results := GetGitStatus(context.Background(), LocalCommandRunner{}, repos)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Error == nil {
		t.Error("expected error for invalid path")
	}
}

// fakeCommandRunner records every RunIn call and answers from a script keyed
// by the git subcommand.
type fakeCommandRunner struct {
	calls   [][]string // dir, name, args...
	outputs map[string]string
}

func (f *fakeCommandRunner) RunIn(_ context.Context, dir, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{dir, name}, args...))
	if len(args) > 0 {
		if out, ok := f.outputs[args[0]]; ok {
			return out, nil
		}
	}
	return "", nil
}

func TestFetchOneRepoGoesThroughTheCommandRunner(t *testing.T) {
	f := &fakeCommandRunner{outputs: map[string]string{
		"rev-parse": "main\n",
		"status":    " M a.go\n?? b.go\n",
	}}
	repo := config.RepoConfig{Host: "mini", Path: "~/workspace/docket", Label: "docket"}

	got := fetchOneRepo(context.Background(), f, repo)

	if got.Host != "mini" || got.Branch != "main" || got.DirtyCount != 2 {
		t.Errorf("got %+v", got)
	}
	if len(f.calls) == 0 || f.calls[0][0] != "~/workspace/docket" || f.calls[0][1] != "git" {
		t.Errorf("first call must run git in the repo's own path, got %v", f.calls)
	}
}

func TestSSHRunnerRunInChangesDirectoryFirst(t *testing.T) {
	r := SSHRunner{Host: "mini", Tmux: "/usr/bin/tmux"}
	// RunIn goes through RunShell; capture the script by inspecting argv.
	script := "cd -- ~/'workspace/docket' && 'git' 'status' '--porcelain'"
	got := r.argv(script)
	if got[len(got)-1] != script {
		t.Fatalf("argv tail = %q", got[len(got)-1])
	}
	if q := quoteRemotePath("~/workspace/docket"); q != "~/'workspace/docket'" {
		t.Errorf("a leading ~ must stay unquoted so the remote shell expands it, got %s", q)
	}
	if q := quoteRemotePath("/abs/path with space"); q != "'/abs/path with space'" {
		t.Errorf("an absolute path is quoted whole, got %s", q)
	}
}
