package tui

import (
	"testing"
	"time"

	"github.com/jhoot/cockpit/buildctl"
	"github.com/jhoot/cockpit/sources"
)

func legacySession(name string, lastUsed time.Time) sources.TmuxSession {
	return sources.TmuxSession{Name: name, Windows: 1, Attached: false, LastUsed: lastUsed}
}

func buildSession(conv, title, status string, live, attachable, resumable bool, updated time.Time) buildctl.Session {
	var runID *string
	if live {
		id := "run-" + conv
		runID = &id
	}
	return buildctl.Session{
		ConversationID: conv,
		RunID:          runID,
		ProjectID:      "project-1",
		ProjectLabel:   "program-health",
		Title:          title,
		Agent:          "codex",
		HostID:         "host-1",
		HostLabel:      "Local",
		HostKind:       "local",
		Status:         status,
		Live:           live,
		Attachable:     attachable,
		Resumable:      resumable,
		UpdatedAt:      updated,
	}
}

// TestMergeNameCollision proves identical display names across sources never
// merge: both records survive with distinct identities.
func TestMergeNameCollision(t *testing.T) {
	now := time.Now()
	legacy := []sources.TmuxSession{legacySession("Fix admissions provenance", now)}
	build := []buildctl.Session{
		buildSession("conv-1", "Fix admissions provenance", "idle", true, true, false, now),
	}

	merged := MergeSessions(legacy, build)
	if len(merged) != 2 {
		t.Fatalf("got %d records, want 2 — collision must not merge", len(merged))
	}

	keys := map[string]bool{}
	var sawLegacy, sawBuild bool
	for _, s := range merged {
		if keys[s.Key()] {
			t.Errorf("duplicate identity key %q", s.Key())
		}
		keys[s.Key()] = true
		if s.DisplayName() != "Fix admissions provenance" {
			t.Errorf("display name = %q, want the shared name on both", s.DisplayName())
		}
		switch s.Source {
		case SourceLegacy:
			sawLegacy = true
		case SourceBuild:
			sawBuild = true
		}
	}
	if !sawLegacy || !sawBuild {
		t.Error("one of the colliding records was swallowed")
	}
	if keys["legacy:Fix admissions provenance"] != true || keys["build:conv-1"] != true {
		t.Errorf("keys = %v, want source-scoped identities", keys)
	}
}

// TestMergeOrdering proves deterministic recency ordering with a stable
// tie-break (Build first, then key).
func TestMergeOrdering(t *testing.T) {
	base := time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC)
	legacy := []sources.TmuxSession{
		legacySession("old-legacy", base.Add(-2*time.Hour)),
		legacySession("tied-legacy", base),
		legacySession("new-legacy", base.Add(time.Hour)),
	}
	build := []buildctl.Session{
		buildSession("conv-tied", "tied-build", "idle", true, true, false, base),
		buildSession("conv-mid", "mid-build", "idle", true, true, false, base.Add(30*time.Minute)),
	}

	merged := MergeSessions(legacy, build)
	want := []string{"new-legacy", "mid-build", "tied-build", "tied-legacy", "old-legacy"}
	if len(merged) != len(want) {
		t.Fatalf("got %d records, want %d", len(merged), len(want))
	}
	for i, name := range want {
		if merged[i].DisplayName() != name {
			t.Errorf("position %d = %q, want %q (full: %v)", i, merged[i].DisplayName(), name, displayNames(merged))
		}
	}

	// Determinism: shuffled input, same output.
	merged2 := MergeSessions(
		[]sources.TmuxSession{legacy[2], legacy[0], legacy[1]},
		[]buildctl.Session{build[1], build[0]},
	)
	for i := range want {
		if merged2[i].Key() != merged[i].Key() {
			t.Fatalf("non-deterministic merge at %d: %q vs %q", i, merged2[i].Key(), merged[i].Key())
		}
	}
}

func displayNames(ms []MergedSession) []string {
	var out []string
	for _, s := range ms {
		out = append(out, s.DisplayName())
	}
	return out
}

// TestActionGating proves attach/resume availability comes only from contract
// flags — never from names, statuses, or inference.
func TestActionGating(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		s          MergedSession
		attachable bool
		resumable  bool
	}{
		{
			name:       "build live attachable",
			s:          MergedSession{Source: SourceBuild, Build: ptr(buildSession("c1", "t", "idle", true, true, false, now))},
			attachable: true,
		},
		{
			name: "build attachable flag but not live",
			s: MergedSession{Source: SourceBuild, Build: ptr(func() buildctl.Session {
				s := buildSession("c2", "t", "exited", false, true, true, now)
				return s
			}())},
			attachable: false, // flag contradicted by liveness: do not trust attach
			resumable:  true,
		},
		{
			name: "build live but attachable flag false",
			s: MergedSession{Source: SourceBuild, Build: ptr(func() buildctl.Session {
				s := buildSession("c3", "t", "disconnected", true, false, false, now)
				return s
			}())},
			attachable: false,
		},
		{
			name:      "build exited resumable",
			s:         MergedSession{Source: SourceBuild, Build: ptr(buildSession("c4", "t", "exited", false, false, true, now))},
			resumable: true,
		},
		{
			name:       "legacy always attachable",
			s:          MergedSession{Source: SourceLegacy, Legacy: ptr(legacySession("x", now))},
			attachable: true,
		},
		{
			name:       "legacy always attachable, never resumable",
			s:          MergedSession{Source: SourceLegacy, Legacy: ptr(legacySession("y", now))},
			attachable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Attachable(); got != tc.attachable {
				t.Errorf("Attachable() = %v, want %v", got, tc.attachable)
			}
			if got := tc.s.Resumable(); got != tc.resumable {
				t.Errorf("Resumable() = %v, want %v", got, tc.resumable)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
