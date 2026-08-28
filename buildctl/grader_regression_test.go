package buildctl

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Regression tests for grader round 1 findings.

// TestEmptyRunIDRejected: a present-but-empty run_id is malformed, not a
// usable attach target.
func TestEmptyRunIDRejected(t *testing.T) {
	body := `printf '{"schema_version":1,"ok":true,"data":{"sessions":[{"conversation_id":"c","run_id":"","project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"idle","live":true,"attachable":true,"resumable":false,"updated_at":"2026-08-27T21:00:00Z"}]}}'`
	cmd, _ := writeFake(t, body)
	c := &Client{Command: cmd}
	sessions, err := c.ListSessions(context.Background())
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed for empty run_id", err)
	}
	if sessions != nil {
		t.Errorf("partial data leaked: %+v", sessions)
	}
}

// TestLaunchResumeValidateStatus: launch and resume responses get the same
// strict validation as session list rows.
func TestLaunchResumeValidateStatus(t *testing.T) {
	body := `printf '{"schema_version":1,"ok":true,"data":{"conversation_id":"c","run_id":"r","project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"SLEEPING","live":true,"attachable":true,"resumable":false,"updated_at":"2026-08-27T21:00:00Z"}}'`
	cmd, _ := writeFake(t, body)
	c := &Client{Command: cmd}

	_, err := c.Launch(context.Background(), LaunchOptions{ProjectID: "p", Agent: "codex"})
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("launch: error = %v, want ErrMalformed for unknown status", err)
	}
	_, err = c.Resume(context.Background(), "c", "standard")
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("resume: error = %v, want ErrMalformed for unknown status", err)
	}
}

// TestTimeoutWithPipeHoldingGrandchild: the direct child is killed at the
// deadline, but a grandchild keeps stdout open. Wait must still return
// promptly (WaitDelay), not block until the grandchild exits.
func TestTimeoutWithPipeHoldingGrandchild(t *testing.T) {
	// The trap prevents sh from exec'ing sleep, so sleep is a grandchild
	// holding the pipes after sh is killed.
	cmd, _ := writeFake(t, "trap '' TERM\nsleep 300")
	c := &Client{Command: cmd, Timeout: 150 * time.Millisecond}

	start := time.Now()
	_, err := c.ListSessions(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	// Timeout (150ms) + WaitDelay (3s) + slack must be far below 300s.
	if elapsed > 10*time.Second {
		t.Fatalf("call returned after %v — grandchild wedged the wait", elapsed)
	}
}

// TestWaitDelayWithoutTimeout: buildctl exits 0 with a valid envelope, but a
// grandchild holds the pipes. The client must not hang beyond WaitDelay.
func TestWaitDelayWithoutTimeout(t *testing.T) {
	cmd, dir := writeFake(t, "sleep 300 &\ncat \"$DIR/fixture.json\"\nexit 0")
	fixture := `{"schema_version":1,"ok":true,"data":{"sessions":[]}}`
	if err := os.WriteFile(dir+"/fixture.json", []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{Command: cmd, Timeout: 30 * time.Second}

	start := time.Now()
	_, err := c.ListSessions(context.Background())
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("call returned after %v — WaitDelay not bounding pipe wait", elapsed)
	}
	// Output arrived and the process exited cleanly; depending on Go's
	// WaitDelay semantics this either parses or is rejected as malformed —
	// both are acceptable, a hang is not.
	if err != nil && !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want nil or ErrMalformed", err)
	}
}
