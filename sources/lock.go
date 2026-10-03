package sources

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// A project lock serialises lifecycle operations and reconciliation across
// every cockpit process talking to one tmux server: the TUI, the daemon, and
// any client on another machine reaching it over ssh. It is a global user
// option on that server, so it exists whether or not the project's session
// does yet, and it is taken with if-shell -F, which tmux evaluates and acts on
// inside one command-queue item — no other client's command can land between
// the test and the set. The lock carries an expiry so a caller that crashes
// holding it blocks nobody for long, and a release only removes a lock whose
// token is the caller's own.
//
// tmux compares format values as strings, so the expiry stamp is zero-padded
// to a fixed width before it is compared.

const (
	lockTTL          = 30 * time.Second
	lockRetry        = 100 * time.Millisecond
	lockWait         = 5 * time.Second
	lockStampWidth   = 12
	lockOptionPrefix = "@cockpit_lock_"
)

// ErrLockBusy means another cockpit client held the project's lock for the
// whole wait. The caller must not proceed as if it had won.
var ErrLockBusy = errors.New("another cockpit client is operating on this project")

// ProjectLock is a held lock. Release must be called; it is safe to call
// after expiry.
type ProjectLock struct {
	Session string
	Token   string
	// ServerAbsent is true when there was no tmux server to lock. The caller
	// is then alone by definition: the first thing it does will start one,
	// and new-session itself refuses a duplicate name.
	ServerAbsent bool
}

func lockOption(session string) string      { return lockOptionPrefix + session }
func lockUntilOption(session string) string { return lockOptionPrefix + session + "_until" }

func lockStamp(t time.Time) string {
	return fmt.Sprintf("%0*d", lockStampWidth, t.Unix())
}

// AcquireLockArgs is the compare-and-set: take the lock if it is free or
// expired, in one server-side step.
func AcquireLockArgs(session, token string, now time.Time) []string {
	free := "#{||:#{==:#{" + lockOption(session) + "},},#{<:#{" + lockUntilOption(session) + "}," + lockStamp(now) + "}}"
	take := "set-option -g " + lockUntilOption(session) + " " + lockStamp(now.Add(lockTTL)) +
		" ; set-option -g " + lockOption(session) + " " + token
	return []string{"if-shell", "-F", free, take}
}

// ReadLockArgs reads back who holds the lock.
func ReadLockArgs(session string) []string {
	return []string{"display-message", "-p", "#{" + lockOption(session) + "}"}
}

// ReleaseLockArgs drops the lock only if token still holds it.
func ReleaseLockArgs(session, token string) []string {
	held := "#{==:#{" + lockOption(session) + "}," + token + "}"
	drop := "set-option -gu " + lockOption(session) + " ; set-option -gu " + lockUntilOption(session)
	return []string{"if-shell", "-F", held, drop}
}

// AcquireProjectLock takes the project's lock on the runner's server, waiting
// up to lockWait for another holder to finish. It never waits on a server
// that is not there.
func AcquireProjectLock(ctx context.Context, r Runner, session string, now func() time.Time) (*ProjectLock, error) {
	token, err := lockToken()
	if err != nil {
		return nil, err
	}
	deadline := now().Add(lockWait)
	for {
		if _, err := r.Run(ctx, AcquireLockArgs(session, token, now())...); err != nil {
			if IsNoServer(err) {
				return &ProjectLock{Session: session, Token: token, ServerAbsent: true}, nil
			}
			return nil, fmt.Errorf("lock %s: %w", session, err)
		}
		out, err := r.Run(ctx, ReadLockArgs(session)...)
		if err != nil {
			return nil, fmt.Errorf("lock %s: %w", session, err)
		}
		if trimmed(out) == token {
			return &ProjectLock{Session: session, Token: token}, nil
		}
		if now().After(deadline) {
			return nil, fmt.Errorf("lock %s: %w", session, ErrLockBusy)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockRetry):
		}
	}
}

// Release drops the lock. A lock taken on an absent server has nothing to
// drop; one whose server has since started is released by token, which
// matches nothing and is harmless.
func (l *ProjectLock) Release(ctx context.Context, r Runner) {
	if l == nil || l.ServerAbsent {
		return
	}
	_, _ = r.Run(ctx, ReleaseLockArgs(l.Session, l.Token)...)
}

func lockToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + hex.EncodeToString(b[:]), nil
}

func trimmed(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
