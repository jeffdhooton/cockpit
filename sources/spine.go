package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// spineOutputLimit bounds the snapshot read from spine.
const spineOutputLimit = 4 << 20

// SpineRunner runs `spine <args>` and returns its stdout. The only call the
// source ever makes is `bearings --json`; Cockpit never runs a spine
// subcommand that changes state. It is a seam so tests never exec spine.
type SpineRunner interface {
	RunSpine(ctx context.Context, args ...string) (string, error)
}

// ErrSpineNotFound means the spine binary is not on PATH.
var ErrSpineNotFound = errors.New("spine not found in PATH")

// LocalSpineRunner runs the real spine binary.
type LocalSpineRunner struct{}

func (LocalSpineRunner) RunSpine(ctx context.Context, args ...string) (string, error) {
	path, err := exec.LookPath("spine")
	if err != nil {
		return "", ErrSpineNotFound
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}

// SpineAgent is one agent working a goal.
type SpineAgent struct {
	ID      string  `json:"id"`
	Role    string  `json:"role"`
	Parent  string  `json:"parent"`
	Stream  string  `json:"stream"`
	State   string  `json:"state"`
	Now     string  `json:"now"`
	Spent   float64 `json:"spent"`
	LiveLog string  `json:"live_log"`
	Window  string  `json:"window"`
}

// SpineItem is one row of a snapshot section.
type SpineItem struct {
	Repo            string       `json:"repo"`
	Goal            string       `json:"goal"`
	Stream          string       `json:"stream"`
	Agent           string       `json:"agent"`
	Kind            string       `json:"kind"`
	Title           string       `json:"title"`
	Now             string       `json:"now"`
	OutcomesPassing []int        `json:"outcomes_passing"`
	OutcomeCount    int          `json:"outcome_count"`
	Spent           float64      `json:"spent"`
	Cap             float64      `json:"cap"`
	URL             string       `json:"url"`
	At              time.Time    `json:"at"`
	Choices         []string     `json:"choices"`
	Subject         string       `json:"subject"`
	Agents          []SpineAgent `json:"agents"`
}

// SpineSnapshot is the document `spine bearings --json` prints.
type SpineSnapshot struct {
	At          time.Time   `json:"at"`
	NeedsYou    []SpineItem `json:"needs_you"`
	Underway    []SpineItem `json:"underway"`
	ChartedNext []SpineItem `json:"charted_next"`
	Landed      []SpineItem `json:"landed"`
	Errors      []string    `json:"errors"`
}

// SpineStatus is what the tile shows: the snapshot, or why there is none.
type SpineStatus struct {
	Snapshot *SpineSnapshot
	Err      error
}

// Readable reports whether a snapshot was read.
func (s SpineStatus) Readable() bool { return s.Snapshot != nil && s.Err == nil }

// GetSpineStatus runs `spine bearings --json` and parses it. Every failure
// yields an unreadable status with a short reason.
func GetSpineStatus(ctx context.Context, r SpineRunner) (st SpineStatus) {
	defer func() {
		if p := recover(); p != nil {
			st = SpineStatus{Err: fmt.Errorf("spine: %v", p)}
		}
	}()
	if r == nil {
		return SpineStatus{Err: ErrSpineNotFound}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := r.RunSpine(ctx, "bearings", "--json")
	if err != nil {
		if errors.Is(err, ErrSpineNotFound) {
			return SpineStatus{Err: ErrSpineNotFound}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return SpineStatus{Err: errors.New("spine bearings timed out")}
		}
		return SpineStatus{Err: fmt.Errorf("spine bearings failed: %s", firstLine(err.Error()))}
	}
	snap, err := ParseSpineSnapshot(out)
	if err != nil {
		return SpineStatus{Err: err}
	}
	return SpineStatus{Snapshot: snap}
}

// ParseSpineSnapshot applies the rules Cockpit reads a snapshot by: the size
// limit and the typed decode. Doctor calls it too, so the two cannot disagree.
func ParseSpineSnapshot(out string) (*SpineSnapshot, error) {
	if len(out) > spineOutputLimit {
		return nil, errors.New("spine bearings output too large")
	}
	var snap SpineSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		return nil, fmt.Errorf("spine bearings output is not valid JSON: %s", firstLine(err.Error()))
	}
	return &snap, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if s == "" {
		return "unknown error"
	}
	return s
}

// NeedsYouCount is how many items wait on a person.
func (s SpineStatus) NeedsYouCount() int {
	if s.Snapshot == nil {
		return 0
	}
	return len(s.Snapshot.NeedsYou)
}

func (s SpineStatus) underwayGoals() []SpineItem {
	if s.Snapshot == nil {
		return nil
	}
	var out []SpineItem
	for _, it := range s.Snapshot.Underway {
		if it.Kind == "goal" {
			out = append(out, it)
		}
	}
	return out
}

// UnderwayGoalCount counts underway items of kind goal.
func (s SpineStatus) UnderwayGoalCount() int { return len(s.underwayGoals()) }

// UnderwayAgentCount sums the agents across underway goal items.
func (s SpineStatus) UnderwayAgentCount() int {
	n := 0
	for _, g := range s.underwayGoals() {
		n += len(g.Agents)
	}
	return n
}

// UnderwaySpent sums spend over underway goal items.
func (s SpineStatus) UnderwaySpent() float64 {
	var t float64
	for _, g := range s.underwayGoals() {
		t += g.Spent
	}
	return t
}

// UnderwayCap sums the cap over underway goal items.
func (s SpineStatus) UnderwayCap() float64 {
	var t float64
	for _, g := range s.underwayGoals() {
		t += g.Cap
	}
	return t
}
