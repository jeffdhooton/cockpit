package sources

import (
	"strconv"
	"testing"
	"time"
)

func TestParseTmuxOutput(t *testing.T) {
	// Seven fields: the three trailing @cockpit_* status options are empty
	// here, which is exactly what tmux emits for a session that never reported.
	input := "dev|3|1|1700000000||||\nserver|1|0|1699990000||||\n"
	sessions, err := parseTmuxOutput(input, "", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}

	s := sessions[0]
	if s.Name != "dev" {
		t.Errorf("name = %q, want %q", s.Name, "dev")
	}
	if s.Windows != 3 {
		t.Errorf("windows = %d, want 3", s.Windows)
	}
	if !s.Attached {
		t.Error("expected attached = true")
	}
	if s.LastUsed != time.Unix(1700000000, 0) {
		t.Errorf("last_used = %v, want %v", s.LastUsed, time.Unix(1700000000, 0))
	}

	s2 := sessions[1]
	if s2.Name != "server" {
		t.Errorf("name = %q, want %q", s2.Name, "server")
	}
	if s2.Attached {
		t.Error("expected attached = false")
	}
}

func TestParseTmuxOutputEmpty(t *testing.T) {
	sessions, err := parseTmuxOutput("", "", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

func TestParseTmuxOutputMalformed(t *testing.T) {
	// Lines with fewer than 4 fields should be skipped
	input := "incomplete|1\n"
	sessions, err := parseTmuxOutput(input, "", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0 (malformed lines skipped)", len(sessions))
	}
}

func TestAgentStatusNeedsInputIsDistinct(t *testing.T) {
	// needs_input is the state pane hashing cannot represent. It must not
	// collide with idle, which is what a blocked agent looks like from outside.
	if AgentStatusNeedsInput == AgentStatusIdle {
		t.Fatal("needs_input must be distinct from idle")
	}
	if AgentStatusNeedsInput == AgentStatusUnknown {
		t.Fatal("needs_input must be distinct from unknown")
	}
}

func TestParseSessionsReadsReportedStatus(t *testing.T) {
	now := time.Now().Unix()
	out := "app|3|1|" + strconv.FormatInt(now, 10) + "|working|" + strconv.FormatInt(now, 10) + "|dev|\n"
	sessions, err := parseTmuxOutput(out, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	if sessions[0].Name != "app" {
		t.Errorf("name = %q", sessions[0].Name)
	}
	if !sessions[0].StatusReported || sessions[0].Status != AgentStatusWorking {
		t.Errorf("want a reported working status, got %+v", sessions[0])
	}
}

func TestParseSessionsHandlesUnsetStatusOptions(t *testing.T) {
	// A session that never reported yields empty option fields, not an error.
	out := "docket|1|0|1788250990||||\n"
	sessions, err := parseTmuxOutput(out, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "docket" {
		t.Fatalf("got %+v", sessions)
	}
	if sessions[0].StatusReported {
		t.Error("a session with no status must not read as reported")
	}
}

func TestParseSessionsKeepsSeparatorInName(t *testing.T) {
	// The name is anchored on the trailing fixed fields precisely so a name
	// containing the separator survives.
	out := "a|b|2|0|1788250990|idle|1788250990|zsh|\n"
	sessions, _ := parseTmuxOutput(out, "", time.Now())
	if len(sessions) != 1 || sessions[0].Name != "a|b" {
		t.Fatalf("name = %q, want \"a|b\"", sessions[0].Name)
	}
}

func TestParseSessionsHandlesNeverAttachedSession(t *testing.T) {
	// Verified against real tmux: session_last_attached is empty, not zero,
	// for a session nobody has attached to yet. The field count still holds.
	out := "fresh|1|0||needs_input|" + strconv.FormatInt(time.Now().Unix(), 10) + "|dev|\n"
	sessions, _ := parseTmuxOutput(out, "", time.Now())
	if len(sessions) != 1 || sessions[0].Name != "fresh" {
		t.Fatalf("got %+v", sessions)
	}
	if !sessions[0].StatusReported {
		t.Error("an empty last_attached must not stop the status from parsing")
	}
}

func TestParseSessionsCarriesHostAndView(t *testing.T) {
	now := time.Unix(1788250998, 0)
	out := "mini|1|0|1788250990||||mini\n" + "docket|2|1|1788250990|working|1788250990|zsh|\n"
	sessions, err := parseTmuxOutput(out, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %+v", sessions)
	}
	if sessions[0].ViewOf != "mini" {
		t.Errorf("view marker not read: %+v", sessions[0])
	}
	if sessions[1].ViewOf != "" || !sessions[1].StatusReported {
		t.Errorf("got %+v", sessions[1])
	}

	remote, _ := parseTmuxOutput("docket|1|0|1788250990||||\n", "mini", now)
	if remote[0].Host != "mini" || remote[0].Key() != "mini/docket" {
		t.Errorf("remote parse must stamp the host, got %+v", remote[0])
	}
}

func TestRemoteStalenessUsesTheRemoteClock(t *testing.T) {
	// The status was stamped five minutes ago on mini's clock. The Mac's clock
	// is twenty minutes ahead of mini's. Judged locally the status would be
	// twenty-five minutes old and silently discarded; judged remotely it is
	// fresh.
	remoteNow := time.Unix(1788250998, 0)
	stamped := remoteNow.Add(-5 * time.Minute).Unix()
	localNow := remoteNow.Add(20 * time.Minute)

	line := "docket|1|0||working|" + strconv.FormatInt(stamped, 10) + "|zsh|\n"

	byRemote, _ := parseTmuxOutput(line, "mini", remoteNow)
	if !byRemote[0].StatusReported {
		t.Error("five minutes old on the remote clock is fresh")
	}
	byLocal, _ := parseTmuxOutput(line, "mini", localNow)
	if byLocal[0].StatusReported {
		t.Error("judged by the skewed local clock the same status reads stale, which is the bug the remote clock prevents")
	}
}
