package sources

import (
	"context"
	"fmt"
	"strings"

	"github.com/jhoot/cockpit/config"
)

// A view is a local tmux session, named for a remote host, whose windows each
// run an ssh attach onto one of that host's sessions. Jumping to a remote
// project means switching to the view and selecting the project's window, so
// it feels like a local jump and prefix-S back to cockpit still works.
//
// The view is disposable. Killing a window kills nothing remote; the remote
// session holds the state and `new-session -A` reattaches next time.

// viewOfOption marks a view session so the grid does not render it as a
// project of its own.
const viewOfOption = "@cockpit_view_of"

// ViewAttachCommand is the shell command a view window runs: an interactive
// ssh that attaches to, or creates, the remote session.
func ViewAttachCommand(host config.HostConfig, session string) string {
	return "ssh -t -- " + strings.Join([]string{
		shellQuote(host.Name), shellQuote(host.Tmux),
		shellQuote("new-session"), shellQuote("-A"), shellQuote("-s"), shellQuote(session),
	}, " ")
}

// ViewSessionArgs creates the view session with its first window already
// attached to a project. Creating the session bare would leave a stray shell
// as window 0 forever.
func ViewSessionArgs(host config.HostConfig, session string) []string {
	return []string{
		"new-session", "-d", "-s", host.Name, "-n", session, ViewAttachCommand(host, session), ";",
		"set-option", "-t", host.Name, viewOfOption, host.Name,
	}
}

// ViewWindowArgs adds a project window to an existing view session.
func ViewWindowArgs(host config.HostConfig, session string) []string {
	return []string{"new-window", "-d", "-t", host.Name + ":", "-n", session, ViewAttachCommand(host, session)}
}

// ViewRespawnArgs restarts a view window whose ssh has exited.
func ViewRespawnArgs(host config.HostConfig, session string) []string {
	return []string{"respawn-window", "-k", "-t", Target(host.Name, session), ViewAttachCommand(host, session)}
}

// JumpRemote brings a remote project up and switches to it.
//
// The remote side goes first: the session and its processes exist before any
// local window tries to attach. If the host cannot be reached, nothing local
// happens at all — an ssh window onto a dead host is just a window with an
// error in it.
func JumpRemote(ctx context.Context, local, remote Runner, host config.HostConfig, repo config.RepoConfig) error {
	created, err := EnsureSession(ctx, remote, repo)
	if err != nil {
		return fmt.Errorf("jump %s: %w", repo.Key(), err)
	}
	// A process that fails to launch must never stand between the user and
	// the session they asked for.
	_ = ReconcileProcesses(ctx, remote, repo)
	if created {
		_, _ = remote.Run(ctx, SelectFirstWindowArgs(repo.Label)...)
	}

	if !SessionExists(ctx, local, host.Name) {
		if _, err := local.Run(ctx, ViewSessionArgs(host, repo.Label)...); err != nil {
			return fmt.Errorf("jump %s: create view: %w", repo.Key(), err)
		}
	} else if err := ensureViewWindow(ctx, local, host, repo.Label); err != nil {
		return fmt.Errorf("jump %s: %w", repo.Key(), err)
	}

	if _, err := local.Run(ctx, "switch-client", "-t", host.Name); err != nil {
		return fmt.Errorf("jump %s: %w", repo.Key(), err)
	}
	_, err = local.Run(ctx, "select-window", "-t", Target(host.Name, repo.Label))
	return err
}

// ensureViewWindow makes sure the view has a live window for the session: a
// missing one is added, a dead one — ssh exited — is respawned in place, and a
// live one is left alone.
func ensureViewWindow(ctx context.Context, local Runner, host config.HostConfig, session string) error {
	windows, err := ListWindows(ctx, local, host.Name)
	if err != nil {
		return fmt.Errorf("read view: %w", err)
	}
	for _, w := range windows {
		if w.Name != session {
			continue
		}
		if !w.Dead {
			return nil
		}
		_, err := local.Run(ctx, ViewRespawnArgs(host, session)...)
		return err
	}
	_, err = local.Run(ctx, ViewWindowArgs(host, session)...)
	return err
}
