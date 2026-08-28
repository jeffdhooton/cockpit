package tui

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jhoot/cockpit/buildctl"
	"github.com/jhoot/cockpit/sources"
)

// SessionSource identifies which authority owns a session record. Identity is
// (Source, key) — a Build conversation and a legacy tmux session with the
// same display name are always two separate records.
type SessionSource int

const (
	SourceLegacy SessionSource = iota // default tmux server
	SourceBuild                       // Build via the buildctl contract
)

// MergedSession is one row in the unified navigator. Exactly one of Legacy /
// Build is non-nil.
type MergedSession struct {
	Source SessionSource
	Legacy *sources.TmuxSession
	Build  *buildctl.Session
}

// Key is the collision-proof identity used for status maps and comparisons.
// The run id disambiguates a hostile/buggy producer that lists two records
// with the same conversation id.
func (s MergedSession) Key() string {
	if s.Source == SourceBuild && s.Build != nil {
		run := "none"
		if s.Build.RunID != nil && *s.Build.RunID != "" {
			run = *s.Build.RunID
		}
		return "build:" + s.Build.ConversationID + ":" + run
	}
	if s.Legacy != nil {
		return "legacy:" + s.Legacy.Name
	}
	return "unknown"
}

// DisplayName is the human label: Build's authoritative title for Build
// sessions, the tmux session name for legacy ones. Contract-supplied strings
// are sanitized before they reach the terminal.
func (s MergedSession) DisplayName() string {
	if s.Source == SourceBuild && s.Build != nil {
		if s.Build.Title != "" {
			return SanitizeDisplay(s.Build.Title)
		}
		return SanitizeDisplay(s.Build.ConversationID)
	}
	if s.Legacy != nil {
		return s.Legacy.Name
	}
	return ""
}

// maxDisplayLen bounds any contract-supplied string before rendering, so a
// hostile or buggy producer cannot blow up the fixed panel layout.
const maxDisplayLen = 120

// SanitizeDisplay strips characters that must never reach the terminal from
// contract-supplied strings and bounds their length: C0 controls (including
// ESC, so no 7-bit ANSI), DEL, C1 controls (8-bit CSI/OSC/DCS), and Unicode
// format characters (zero-width and bidi overrides used for spoofing).
// Values cross the contract as data; they reach the terminal as data too.
func SanitizeDisplay(s string) string {
	out := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	runes := []rune(out)
	if len(runes) > maxDisplayLen {
		out = string(runes[:maxDisplayLen-1]) + "…"
	}
	return out
}

// Activity is the recency timestamp used for sorting.
func (s MergedSession) Activity() time.Time {
	if s.Source == SourceBuild && s.Build != nil {
		return s.Build.UpdatedAt
	}
	if s.Legacy != nil {
		return s.Legacy.LastUsed
	}
	return time.Time{}
}

// Attachable reports only what the owning authority says: Build records use
// the contract flag on a live run with a concrete run id; legacy sessions
// are always switchable.
func (s MergedSession) Attachable() bool {
	if s.Source == SourceBuild {
		return s.Build != nil && s.Build.Attachable && s.Build.Live &&
			s.Build.RunID != nil && *s.Build.RunID != ""
	}
	return s.Legacy != nil
}

// Resumable comes only from the contract flag; legacy sessions never resume.
func (s MergedSession) Resumable() bool {
	return s.Source == SourceBuild && s.Build != nil && s.Build.Resumable
}

// MergeSessions combines Build and legacy tmux sessions into one navigable,
// deterministically ordered list. Records are never merged across sources:
// name collisions between a Build title and a tmux session name leave both
// records intact.
func MergeSessions(legacy []sources.TmuxSession, build []buildctl.Session) []MergedSession {
	merged := make([]MergedSession, 0, len(legacy)+len(build))
	for i := range build {
		s := build[i]
		merged = append(merged, MergedSession{Source: SourceBuild, Build: &s})
	}
	for i := range legacy {
		l := legacy[i]
		merged = append(merged, MergedSession{Source: SourceLegacy, Legacy: &l})
	}

	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i].Activity(), merged[j].Activity()
		if !a.Equal(b) {
			return a.After(b) // most recent first
		}
		// Deterministic tie-break: Build before legacy, then by key.
		if merged[i].Source != merged[j].Source {
			return merged[i].Source == SourceBuild
		}
		return merged[i].Key() < merged[j].Key()
	})
	return merged
}
