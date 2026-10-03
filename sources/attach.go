package sources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jeffdhooton/cockpit/config"
)

// ErrTargetGone means the pane, window or session an item pointed at is no
// longer there, or has been replaced by another with the same id on a newer
// server. The caller shows it and refreshes; it never creates anything.
var ErrTargetGone = errors.New("this target is no longer available")

// AttachTarget is a validated destination: identities, not names.
type AttachTarget struct {
	Host       string
	Generation string
	Session    string // display only; never used to address anything
	SessionID  string
	WindowID   string
	PaneID     string
}

// ValidateTarget re-reads the target immediately before selection. A pane
// target must still exist on the same server generation, in the same session
// and window. A window target must still exist in the same session. A
// session target must still exist. Anything less is ErrTargetGone.
func ValidateTarget(ctx context.Context, r Runner, t AttachTarget) error {
	switch {
	case t.PaneID != "":
		out, err := r.Run(ctx, FindPaneArgs(t.PaneID)...)
		if err != nil {
			if IsVerifiedAbsence(err) {
				return ErrTargetGone
			}
			return err
		}
		panes := ParsePaneReports(out, t.Host, nil, timeNow())
		if len(panes) != 1 {
			return ErrTargetGone
		}
		p := panes[0]
		if (t.Generation != "" && p.Generation != t.Generation) ||
			(t.SessionID != "" && p.SessionID != t.SessionID) ||
			(t.WindowID != "" && p.WindowID != t.WindowID) {
			return ErrTargetGone
		}
		return nil
	case t.WindowID != "":
		out, err := r.Run(ctx, FindWindowArgs(t.WindowID)...)
		if err != nil {
			if IsVerifiedAbsence(err) {
				return ErrTargetGone
			}
			return err
		}
		windows := ParseWindows(out)
		if len(windows) != 1 {
			return ErrTargetGone
		}
		if t.Generation != "" && windows[0].Generation != t.Generation {
			return ErrTargetGone
		}
		if t.SessionID != "" {
			// list-windows -a does not carry the session id; confirm the
			// session still exists under that id instead.
			if _, err := r.Run(ctx, HasSessionArgs(t.SessionID)...); err != nil {
				if IsVerifiedAbsence(err) {
					return ErrTargetGone
				}
				return err
			}
		}
		return nil
	case t.SessionID != "":
		if _, err := r.Run(ctx, HasSessionArgs(t.SessionID)...); err != nil {
			if IsVerifiedAbsence(err) {
				return ErrTargetGone
			}
			return err
		}
		if t.Generation != "" {
			out, err := r.Run(ctx, ListSessionsArgs()...)
			if err != nil {
				return err
			}
			for _, s := range mustParse(out) {
				if s.ID == t.SessionID {
					if s.Generation != t.Generation {
						return ErrTargetGone
					}
					return nil
				}
			}
			return ErrTargetGone
		}
		return nil
	case t.Session != "":
		// A legacy session-level report has only a name. It is the best
		// identity available and is validated as such.
		exists, err := sessionExists(ctx, r, t.Session)
		if err != nil {
			return err
		}
		if !exists {
			return ErrTargetGone
		}
		return nil
	}
	return ErrTargetGone
}

func mustParse(out string) []TmuxSession {
	sessions, _ := parseTmuxOutput(out, "", timeNow())
	return sessions
}

// sessionAddress is the target string used to switch to the session: the id
// when known, the name for a legacy target.
func (t AttachTarget) sessionAddress() string {
	if t.SessionID != "" {
		return t.SessionID
	}
	return t.Session
}

// SelectTargetArgs focuses the window and pane inside their session, so the
// attach lands on the reporting pane rather than whatever was current.
func SelectTargetArgs(t AttachTarget) [][]string {
	var out [][]string
	if t.WindowID != "" {
		out = append(out, []string{"select-window", "-t", t.WindowID})
	}
	if t.PaneID != "" {
		out = append(out, []string{"select-pane", "-t", t.PaneID})
	}
	return out
}

// AttachLocal validates and then switches the client to the target. It
// creates nothing: a missing target is ErrTargetGone.
func AttachLocal(ctx context.Context, local Runner, t AttachTarget) error {
	if err := ValidateTarget(ctx, local, t); err != nil {
		return err
	}
	for _, args := range SelectTargetArgs(t) {
		if _, err := local.Run(ctx, args...); err != nil {
			return fmt.Errorf("attach: %w", err)
		}
	}
	if _, err := local.Run(ctx, "switch-client", "-t", t.sessionAddress()); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	return nil
}

// ViewAttachExistingCommand is the shell command a view window runs to reach
// an existing remote session without creating one: attach-session fails if
// the session is gone, and the window says so.
func ViewAttachExistingCommand(host config.HostConfig, session string) string {
	return "ssh -t -- " + strings.Join([]string{
		shellQuote(host.Name), shellQuote(host.Tmux),
		shellQuote("attach-session"), shellQuote("-t"), shellQuote(session),
	}, " ")
}

// AttachRemote validates the target on the remote server, focuses the window
// and pane there, then brings up a local view window that attaches to the
// existing session. It never creates a remote session and never runs a
// project command.
func AttachRemote(ctx context.Context, local, remote Runner, host config.HostConfig, t AttachTarget) error {
	if err := ValidateTarget(ctx, remote, t); err != nil {
		return err
	}
	for _, args := range SelectTargetArgs(t) {
		if _, err := remote.Run(ctx, args...); err != nil {
			return fmt.Errorf("attach %s: %w", host.Name, err)
		}
	}
	session := t.Session
	if session == "" {
		return fmt.Errorf("attach %s: target has no session name for the view window", host.Name)
	}
	cmd := ViewAttachExistingCommand(host, session)
	if !SessionExists(ctx, local, host.Name) {
		args := []string{"new-session", "-d", "-s", host.Name, "-n", session, cmd, ";",
			"set-option", "-t", host.Name, viewOfOption, host.Name}
		if _, err := local.Run(ctx, args...); err != nil {
			return fmt.Errorf("attach %s: create view: %w", host.Name, err)
		}
	} else if err := ensureViewWindowWith(ctx, local, host, session, cmd); err != nil {
		return fmt.Errorf("attach %s: %w", host.Name, err)
	}
	if _, err := local.Run(ctx, "switch-client", "-t", host.Name); err != nil {
		return fmt.Errorf("attach %s: %w", host.Name, err)
	}
	_, err := local.Run(ctx, "select-window", "-t", Target(host.Name, session))
	return err
}
