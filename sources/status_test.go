package sources

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestSetStatusArgsWritesValueTimeAndWindow(t *testing.T) {
	now := time.Unix(1788250998, 0)
	args := SetStatusArgs("app", AgentStatusNeedsInput, "dev", now)

	if args[0] != "set-option" {
		t.Fatalf("want set-option, got %v", args)
	}
	if !slices.Contains(args, "@cockpit_status") || !slices.Contains(args, "needs_input") {
		t.Errorf("status value missing: %v", args)
	}
	if !slices.Contains(args, "1788250998") {
		t.Errorf("timestamp missing, staleness cannot be detected: %v", args)
	}
	if !slices.Contains(args, "dev") {
		t.Errorf("reporting window missing, inheritance cannot be detected: %v", args)
	}
}

func TestStatusFromOptionsReadsAReportedStatus(t *testing.T) {
	now := time.Unix(1788250998, 0)
	got, reported := StatusFromOptions("working", "1788250990", "dev", "dev", now)

	if !reported {
		t.Error("a fresh matching status is reported, not inferred")
	}
	if got != AgentStatusWorking {
		t.Errorf("status = %v, want working", got)
	}
}

func TestStatusFromOptionsTreatsUnsetAsNotReported(t *testing.T) {
	// tmux reports an unset option as empty rather than erroring, so empty is
	// the common case and must never look like a real state.
	if _, reported := StatusFromOptions("", "", "", "zsh", time.Now()); reported {
		t.Error("an unset option is not a report")
	}
}

func TestStatusFromOptionsRejectsStale(t *testing.T) {
	// A crashed agent stops sending events. Without this, "working" persists
	// forever.
	now := time.Unix(1788250998, 0)
	old := strconv.FormatInt(now.Add(-11*time.Minute).Unix(), 10)

	if _, reported := StatusFromOptions("working", old, "dev", "dev", now); reported {
		t.Error("a status older than the staleness window is not reported")
	}
}

func TestStatusFromOptionsRejectsInheritedWindowValue(t *testing.T) {
	// Window options inherit from the session when unset, so a window with no
	// status of its own reports the session's. A mismatch means inherited.
	now := time.Unix(1788250998, 0)
	if _, reported := StatusFromOptions("working", "1788250990", "dev", "zsh", now); reported {
		t.Error("a value inherited from another window is not this window's report")
	}
}

func TestStatusFromOptionsRejectsUnknownName(t *testing.T) {
	now := time.Unix(1788250998, 0)
	if _, reported := StatusFromOptions("banana", "1788250990", "dev", "dev", now); reported {
		t.Error("an unrecognised status name must not be invented into a state")
	}
}

func TestStatusFromOptionsRejectsUnparseableTime(t *testing.T) {
	// A status with no readable timestamp can never be aged out, so it would
	// read as permanently fresh.
	now := time.Unix(1788250998, 0)
	if _, reported := StatusFromOptions("working", "not-a-time", "dev", "dev", now); reported {
		t.Error("a status without a readable timestamp must not be trusted")
	}
}
