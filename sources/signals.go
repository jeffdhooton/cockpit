package sources

import (
	"fmt"
	"sort"
	"time"

	"github.com/jhoot/cockpit/config"
)

// defaultStaleThreshold is used when the configured threshold will not parse.
// Dropping the signal over a typo would hide real staleness.
const defaultStaleThreshold = 24 * time.Hour

// SignalKind categorises what needs attention.
type SignalKind string

const (
	SignalDeadProcess  SignalKind = "dead_process"
	SignalFailingCI    SignalKind = "failing_ci"
	SignalUnpushed     SignalKind = "unpushed"
	SignalStaleSession SignalKind = "stale_session"
)

// Signal is one thing worth looking at.
type Signal struct {
	Kind    SignalKind `json:"kind"`
	Subject string     `json:"subject"`
	Detail  string     `json:"detail"`
}

// SignalInput is everything ComputeSignals reads. Passing time in keeps the
// staleness rule testable.
type SignalInput struct {
	Config    config.SignalsConfig
	Sessions  []TmuxSession
	Git       []GitRepoStatus
	GitHub    *GitHubStatus
	Processes map[string][]ProcessInfo
	Now       time.Time
}

// ComputeSignals gathers everything that needs attention, most urgent first: a
// dead process beats failing continuous integration, which beats unpushed work,
// which beats a session you left open.
func ComputeSignals(in SignalInput) []Signal {
	var signals []Signal
	signals = append(signals, deadProcessSignals(in)...)
	signals = append(signals, failingCISignals(in)...)
	signals = append(signals, unpushedSignals(in)...)
	signals = append(signals, staleSessionSignals(in)...)
	return signals
}

func deadProcessSignals(in SignalInput) []Signal {
	// Sorted, because ranging a map would reorder the panel on every refresh.
	labels := make([]string, 0, len(in.Processes))
	for label := range in.Processes {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	var out []Signal
	for _, label := range labels {
		for _, p := range in.Processes[label] {
			if !p.Configured || p.State != ProcessDead {
				continue
			}
			out = append(out, Signal{
				Kind:    SignalDeadProcess,
				Subject: label + "/" + p.Name,
				Detail:  "process exited",
			})
		}
	}
	return out
}

func failingCISignals(in SignalInput) []Signal {
	if !in.Config.ShowFailingCI || in.GitHub == nil {
		return nil
	}
	var out []Signal
	for _, check := range in.GitHub.RepoChecks {
		if check.CIStatus != "failing" {
			continue
		}
		out = append(out, Signal{
			Kind:    SignalFailingCI,
			Subject: check.RepoLabel,
			Detail:  "checks failing",
		})
	}
	return out
}

func unpushedSignals(in SignalInput) []Signal {
	if !in.Config.ShowUnpushed {
		return nil
	}
	var out []Signal
	for _, repo := range in.Git {
		// A repo we could not read is not a repo with unpushed work.
		if repo.Error != nil || repo.Unpushed == 0 {
			continue
		}
		out = append(out, Signal{
			Kind:    SignalUnpushed,
			Subject: repo.Label,
			Detail:  fmt.Sprintf("%d unpushed %s", repo.Unpushed, plural(repo.Unpushed, "commit")),
		})
	}
	return out
}

func staleSessionSignals(in SignalInput) []Signal {
	if !in.Config.ShowStaleSessions {
		return nil
	}
	threshold, err := time.ParseDuration(in.Config.StaleSessionThreshold)
	if err != nil || threshold <= 0 {
		threshold = defaultStaleThreshold
	}

	var out []Signal
	for _, s := range in.Sessions {
		// A session you are looking at right now is not stale.
		if s.Attached || in.Now.Sub(s.LastUsed) < threshold {
			continue
		}
		out = append(out, Signal{
			Kind:    SignalStaleSession,
			Subject: s.Name,
			Detail:  fmt.Sprintf("idle %s", formatAge(in.Now.Sub(s.LastUsed))),
		})
	}
	return out
}

// plural returns the noun in the form matching n.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// formatAge renders a duration as the coarsest useful unit.
func formatAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
