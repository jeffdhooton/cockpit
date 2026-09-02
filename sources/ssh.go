package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jhoot/cockpit/config"
)

// ErrHostUnreachable means ssh itself failed: no route, refused key, timeout,
// or a prompt BatchMode refused to answer. It is the remote twin of
// ErrTmuxNotFound — the difference between "mini is down" and "mini has no
// sessions", which must never render the same way.
var ErrHostUnreachable = errors.New("host unreachable")

// timeNow is a seam for tests.
var timeNow = time.Now

// SSHRunner runs tmux on a remote host through the system ssh.
//
// Shelling out rather than using an in-process client is deliberate. The real
// ssh already honours everything in ~/.ssh/config — Include, Match,
// IdentitiesOnly, IdentityAgent none, known_hosts revocation — and mini
// depends on several of those. ControlMaster keeps one connection per host,
// so each call is a multiplexed channel rather than a new handshake.
type SSHRunner struct {
	Host string // ssh alias
	Tmux string // absolute remote path
	// ControlDir holds the multiplexing socket. Defaults to the config
	// directory, mode 0700, rather than /tmp.
	ControlDir string
	Timeout    time.Duration
}

// Run executes tmux with args on the remote host. It satisfies Runner, so
// every tmux-backed function works remotely unchanged.
func (r SSHRunner) Run(ctx context.Context, args ...string) (string, error) {
	return r.RunShell(ctx, r.remoteCommand(args))
}

// RunShell executes a raw shell command on the remote host. Callers are
// responsible for quoting; it exists for git and for reading the clock.
func (r SSHRunner) RunShell(ctx context.Context, script string) (string, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if dir := r.controlDir(); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}

	argv := r.argv(script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", classifySSHError(err, stderr.Bytes())
	}
	return string(out), nil
}

// RemoteNow reads the remote clock. Status timestamps are written with the
// remote clock, so staleness must be judged against it.
func (r SSHRunner) RemoteNow(ctx context.Context) (time.Time, error) {
	out, err := r.RunShell(ctx, "date +%s")
	if err != nil {
		return time.Time{}, err
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("remote clock: %w", err)
	}
	return time.Unix(epoch, 0), nil
}

// RunIn runs a command in a remote directory. The directory is left for the
// remote shell to expand, so ~ means the remote user's home.
func (r SSHRunner) RunIn(ctx context.Context, dir, name string, args ...string) (string, error) {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(name))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return r.RunShell(ctx, "cd -- "+quoteRemotePath(dir)+" && "+strings.Join(parts, " "))
}

// remoteCommand renders tmux plus its arguments as one quoted shell string.
// sshd hands the command to the login shell, so quoting is the whole
// correctness of process launch.
func (r SSHRunner) remoteCommand(args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(r.Tmux))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// argv builds the local ssh invocation. "--" before the host means a host
// name can never be read as an option.
func (r SSHRunner) argv(script string) []string {
	return []string{"ssh",
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + r.controlDir() + "/ssh-%C",
		"-o", "ControlPersist=60s",
		"-o", "ConnectTimeout=5",
		"--", r.Host,
		script,
	}
}

func (r SSHRunner) controlDir() string {
	if r.ControlDir != "" {
		return r.ControlDir
	}
	return config.Dir()
}

// classifySSHError separates ssh's own failures from the remote command's.
// ssh exits 255 for transport problems and passes the remote exit status
// through otherwise.
func classifySSHError(err error, stderr []byte) error {
	msg := remoteMessage(stderr)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == 255 {
			return fmt.Errorf("%w: %s", ErrHostUnreachable, msg)
		}
		if msg != "" {
			return errors.New(msg)
		}
		return err
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("ssh not found in PATH: %w", err)
	}
	return err
}

// remoteMessage extracts the error worth showing from ssh's stderr. ssh
// prints its own chatter first — a control-socket race, a known_hosts
// addition — and the remote command's message last.
func remoteMessage(stderr []byte) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(stderr)), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "ControlSocket "):
		case strings.HasPrefix(l, "Warning: Permanently added"):
		default:
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return strings.TrimSpace(string(stderr))
	}
	return lines[len(lines)-1]
}

// shellQuote wraps s in single quotes for a POSIX shell. An embedded single
// quote is handled by closing the quote, adding a backslash-escaped quote,
// and reopening.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteRemotePath quotes a path while letting a leading ~ expand remotely.
func quoteRemotePath(p string) string {
	if p == "~" {
		return "~"
	}
	if strings.HasPrefix(p, "~/") {
		return "~/" + shellQuote(p[2:])
	}
	return shellQuote(p)
}
