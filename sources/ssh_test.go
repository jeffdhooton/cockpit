package sources

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":     "'plain'",
		"two words": "'two words'",
		"it's":      `'it'\''s'`,
		"$HOME":     "'$HOME'",
		"a;rm -rf":  "'a;rm -rf'",
		"":          "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSSHRunnerArgvIsMultiplexedAndQuoted(t *testing.T) {
	r := SSHRunner{Host: "mini", Tmux: "/opt/homebrew/bin/tmux", ControlDir: "/cfg"}
	got := r.argv("'/opt/homebrew/bin/tmux' 'new-window' '-n' 'dev' 'npm run dev'")

	want := []string{"ssh",
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/cfg/ssh-%C",
		"-o", "ControlPersist=60s",
		"-o", "ConnectTimeout=5",
		"--", "mini",
		"'/opt/homebrew/bin/tmux' 'new-window' '-n' 'dev' 'npm run dev'"}
	if !slices.Equal(got, want) {
		t.Errorf("argv =\n%q\nwant\n%q", got, want)
	}
}

func TestSSHRunnerRunQuotesEveryTmuxArgument(t *testing.T) {
	r := SSHRunner{Host: "mini", Tmux: "/opt/homebrew/bin/tmux"}
	got := r.remoteCommand([]string{"new-window", "-n", "dev", "npm run dev", "it's"})
	want := `'/opt/homebrew/bin/tmux' 'new-window' '-n' 'dev' 'npm run dev' 'it'\''s'`
	if got != want {
		t.Errorf("remote command = %s, want %s", got, want)
	}
}

// exitState runs a shell that exits with code, so tests can build a real
// *exec.ExitError for the classifier.
func exitState(t *testing.T, code int) *exec.ExitError {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want an exit error for code %d, got %v", code, err)
	}
	return ee
}

func TestClassifySSHError(t *testing.T) {
	unreachable := classifySSHError(exitState(t, 255), []byte("ssh: connect to host mini port 22: No route to host"))
	if !errors.Is(unreachable, ErrHostUnreachable) {
		t.Errorf("exit 255 must be unreachable, got %v", unreachable)
	}
	if !strings.Contains(unreachable.Error(), "No route to host") {
		t.Errorf("ssh's own message must survive: %v", unreachable)
	}

	remote := classifySSHError(exitState(t, 1), []byte("no server running on /private/tmp/tmux-501/default"))
	if errors.Is(remote, ErrHostUnreachable) {
		t.Errorf("a remote command failure is not a transport failure: %v", remote)
	}
	if !strings.Contains(remote.Error(), "no server running") {
		t.Errorf("the remote message must survive: %v", remote)
	}

	if err := classifySSHError(exec.ErrNotFound, nil); err == nil {
		t.Error("a missing local ssh must be an error")
	}
}

func TestSSHRunnerControlDirDefaultsToConfigDir(t *testing.T) {
	r := SSHRunner{Host: "mini", Tmux: "/usr/bin/tmux"}
	got := r.argv("x")
	idx := slices.Index(got, "ControlPersist=60s")
	if idx < 2 || !strings.HasPrefix(got[idx-2], "ControlPath=") || strings.Contains(got[idx-2], "/tmp/") {
		t.Errorf("control socket must live under the config dir, not /tmp: %q", got)
	}
}

func TestSSHRunnerAgainstARealHost(t *testing.T) {
	host := os.Getenv("COCKPIT_TEST_HOST")
	if host == "" {
		t.Skip("set COCKPIT_TEST_HOST to run against a real host")
	}
	r := SSHRunner{Host: host, Tmux: "/opt/homebrew/bin/tmux"}

	now, err := r.RemoteNow(context.Background())
	if err != nil {
		t.Fatalf("RemoteNow: %v", err)
	}
	if d := now.Sub(timeNow()); d > 60e9 || d < -60e9 {
		t.Errorf("remote clock is %v off local", d)
	}

	// No server running is an answer, not a transport failure.
	if _, err := ListSessions(context.Background(), r); errors.Is(err, ErrHostUnreachable) {
		t.Errorf("list-sessions on a reachable host must not read as unreachable: %v", err)
	}
}

func TestClassifySSHErrorDropsSSHChatter(t *testing.T) {
	// Parallel first calls race to create the control socket, and ssh
	// prints a warning about it before the remote command's own error. The
	// warning is not the error.
	stderr := []byte("ControlSocket /x/ssh-abc already exists, disabling multiplexing\n" +
		"Warning: Permanently added 'mini' (ED25519) to the list of known hosts.\n" +
		"sh: line 0: cd: /Users/jclaw/nope: No such file or directory\n")
	err := classifySSHError(exitState(t, 1), stderr)
	if got := err.Error(); strings.Contains(got, "ControlSocket") || !strings.Contains(got, "No such file") {
		t.Errorf("want the remote error alone, got %q", got)
	}
}
