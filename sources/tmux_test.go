package sources

import (
	"testing"
	"time"
)

func TestParseTmuxOutput(t *testing.T) {
	input := "dev\t3\t1\t1700000000\nserver\t1\t0\t1699990000\n"
	sessions, err := parseTmuxOutput(input)
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
	sessions, err := parseTmuxOutput("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(sessions))
	}
}

func TestParseTmuxOutputMalformed(t *testing.T) {
	// Lines with fewer than 4 fields should be skipped
	input := "incomplete\t1\n"
	sessions, err := parseTmuxOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("got %d sessions, want 0 (malformed lines skipped)", len(sessions))
	}
}
