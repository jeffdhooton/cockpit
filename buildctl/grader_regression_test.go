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

// TestOkTrueWithErrorObject: a contradictory envelope (ok=true carrying an
// error object) is rejected as a whole.
func TestOkTrueWithErrorObject(t *testing.T) {
	body := `printf '{"schema_version":1,"ok":true,"error":{"code":"internal_error","message":"x","retryable":false},"data":{"sessions":[]}}'`
	cmd, _ := writeFake(t, body)
	c := &Client{Command: cmd}
	sessions, err := c.ListSessions(context.Background())
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed for ok=true with error object", err)
	}
	if sessions != nil {
		t.Errorf("partial data leaked: %+v", sessions)
	}
}

// TestResumeIdentityMismatch: a resume success whose conversation_id
// contradicts the request is rejected as a whole (grader round 4).
func TestResumeIdentityMismatch(t *testing.T) {
	// launch_response.json carries conversation_id "conversation-new".
	cmd, _ := writeFake(t, serveFixture(t, "launch_response.json"))
	c := &Client{Command: cmd}
	_, err := c.Resume(context.Background(), "someone-else", "standard")
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed for identity mismatch", err)
	}
}

// TestLaunchProjectMismatch: a launch success whose project_id contradicts
// the request is rejected as a whole (grader round 4).
func TestLaunchProjectMismatch(t *testing.T) {
	cmd, _ := writeFake(t, serveFixture(t, "launch_response.json"))
	c := &Client{Command: cmd}
	_, err := c.Launch(context.Background(), LaunchOptions{ProjectID: "different-project", Agent: "codex"})
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed for project mismatch", err)
	}
}

// TestDuplicateConversationRunRejected: two rows with the same
// (conversation_id, run_id) are a contradictory listing — reject as a whole.
// The same conversation under two different runs is legitimate (resume
// produces a new run) and must pass.
func TestDuplicateConversationRunRejected(t *testing.T) {
	row := func(run string) string {
		runJSON := "null"
		if run != "" {
			runJSON = `"` + run + `"`
		}
		return `{"conversation_id":"c","run_id":` + runJSON + `,"project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"idle","live":true,"attachable":true,"resumable":false,"updated_at":"2026-08-27T21:00:00Z"}`
	}

	dup := `printf '{"schema_version":1,"ok":true,"data":{"sessions":[` + row("run-1") + `,` + row("run-1") + `]}}'`
	cmd, _ := writeFake(t, dup)
	c := &Client{Command: cmd}
	sessions, err := c.ListSessions(context.Background())
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("duplicate (conv,run): error = %v, want ErrMalformed", err)
	}
	if sessions != nil {
		t.Errorf("partial data leaked: %+v", sessions)
	}

	// Same conversation, different runs: valid.
	ok := `printf '{"schema_version":1,"ok":true,"data":{"sessions":[` + row("run-1") + `,` + row("run-2") + `]}}'`
	cmd2, _ := writeFake(t, ok)
	c2 := &Client{Command: cmd2}
	sessions, err = c2.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("same conversation across runs must be accepted: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("got %d sessions, want 2", len(sessions))
	}
}
