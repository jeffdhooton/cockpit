package sources

import (
	"fmt"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

// defaultStaleThreshold is used when the configured threshold will not parse.
// Dropping the signal over a typo would hide real staleness.
const defaultStaleThreshold = 24 * time.Hour

// SignalKind categorises what needs attention.
type SignalKind string

const (
	SignalBlockedAgent SignalKind = "blocked_agent"
	SignalHermesDown   SignalKind = "hermes_down"
	SignalDeadProcess  SignalKind = "dead_process"
	SignalFailingCI    SignalKind = "failing_ci"
	SignalUnpushed     SignalKind = "unpushed"
	SignalStaleSession SignalKind = "stale_session"
)

// Signal is one thing worth looking at. It is the legacy envelope the
// cockpit_signals tool has always returned; the same facts drive the
// attention queue, and this is an adaptation of them, not a second reading.
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
	Panes     []PaneReport
	Git       []GitRepoStatus
	GitHub    *GitHubStatus
	Processes map[string][]ProcessInfo
	Hermes    []HermesStatus
	Now       time.Time
}

// ComputeSignals gathers everything that needs attention, most urgent first:
// an agent waiting on you beats a dead process, which beats failing continuous
// integration, which beats unpushed work, which beats a session you left open.
func ComputeSignals(in SignalInput) []Signal {
	return SignalsFromAttention(DeriveAttention(in.attentionInput()))
}

// attentionInput lifts the flat legacy input into the host-scoped one the
// queue derives from. Everything here is one fresh local observation.
func (in SignalInput) attentionInput() AttentionInput {
	host := HostReport{Outcome: ObservationFresh, ObservedAt: in.Now, Sessions: in.Sessions, Panes: in.Panes, Git: in.Git}
	if len(in.Processes) > 0 {
		host.Processes = map[string]ProcessObservation{}
		for label, infos := range in.Processes {
			host.Processes[label] = ProcessObservation{Project: label, Session: label, SessionExists: true, Processes: describeAll(infos), ObservedAt: in.Now}
		}
	}
	return AttentionInput{
		Config:   in.Config,
		Hosts:    []HostReport{host},
		GitHub:   in.GitHub,
		GitHubAt: in.Now,
		Hermes:   in.Hermes,
		HermesAt: in.Now,
		Now:      in.Now,
	}
}

// describeAll fills Outcome for legacy ProcessInfo values that only carry
// State, so older callers and fixtures derive the same items.
func describeAll(infos []ProcessInfo) []ProcessInfo {
	out := make([]ProcessInfo, len(infos))
	for i, p := range infos {
		if p.Outcome == "" {
			switch p.State {
			case ProcessRunning:
				p.Outcome = OutcomeRunning
			case ProcessDead:
				if p.ExitCode != nil && *p.ExitCode == 0 {
					p.Outcome = OutcomeCompleted
				} else {
					p.Outcome = OutcomeExited
				}
			default:
				p.Outcome = OutcomeNotStarted
			}
		}
		out[i] = p
	}
	return out
}

// SignalsFromAttention adapts a derived report to the legacy envelope: the
// same items, in the legacy kind order, with the legacy subject shapes.
// Stale items are included, as they always were: a signal is what was last
// seen, and the queue carries the freshness that this envelope cannot.
func SignalsFromAttention(rep AttentionReport) []Signal {
	order := []AttentionKind{AttentionNeedsInput, AttentionHermesDown, AttentionProcessExited, AttentionCIFailed, AttentionUnpushed, AttentionStaleSession}
	var out []Signal
	for _, kind := range order {
		for _, item := range rep.Items {
			if item.Kind != kind {
				continue
			}
			out = append(out, signalFor(item))
		}
	}
	return out
}

func signalFor(item AttentionItem) Signal {
	switch item.Kind {
	case AttentionNeedsInput:
		return Signal{Kind: SignalBlockedAgent, Subject: projectKey(item.Host, item.Target.Session), Detail: "waiting on you"}
	case AttentionHermesDown:
		return Signal{Kind: SignalHermesDown, Subject: item.Target.Label, Detail: "gateway " + trimPrefixFold(item.Detail, "Gateway ")}
	case AttentionProcessExited:
		return Signal{Kind: SignalDeadProcess, Subject: item.Project + "/" + item.Target.Process, Detail: "process exited"}
	case AttentionCIFailed:
		return Signal{Kind: SignalFailingCI, Subject: item.Project, Detail: "checks failing on " + item.Target.Branch}
	case AttentionUnpushed:
		return Signal{Kind: SignalUnpushed, Subject: item.Project, Detail: item.Detail}
	default:
		return Signal{Kind: SignalStaleSession, Subject: projectKey(item.Host, item.Target.Session), Detail: item.Detail}
	}
}

func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
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
