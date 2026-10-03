package sources

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// paneFormat is the list-panes format ParsePaneReports reads. The window name
// comes last because it may contain the separator; the session is joined by
// id against the session list rather than carried by name for the same
// reason.
const paneFormat = "#{session_id}|#{window_id}|#{pane_id}|#{pane_pid}|#{pane_dead}|#{pane_active}" +
	"|#{pane_current_command}|#{q:pane_current_path}" +
	"|#{" + paneStatusOption + "}|#{" + paneStatusAtOption + "}|#{" + paneInvocationOption + "}|#{" + paneSeqOption + "}" +
	"|#{pid}|#{start_time}|#{window_name}"

const paneLeadingFields = 14

// Pane-level status options. They are set with -p on one pane, so two
// agents in one session each keep their own record and neither can
// overwrite the other. Their names differ from the session-level ones on
// purpose: tmux resolves an unset pane option through the window and the
// session, so reusing @cockpit_status would make every pane appear to carry
// the session's legacy report.
const (
	paneStatusOption     = "@cockpit_pane_status"
	paneStatusAtOption   = "@cockpit_pane_status_at"
	paneInvocationOption = "@cockpit_pane_invocation"
	paneSeqOption        = "@cockpit_pane_seq"
)

// PaneReport is one pane's live identity plus whatever status record it
// carries. A pane with no record has Status unknown and Reported false.
type PaneReport struct {
	Host       string
	Session    string // resolved from SessionID; empty when unknown
	SessionID  string
	WindowID   string
	WindowName string
	PaneID     string
	PanePID    int
	Dead       bool
	Generation string
	// Command is the pane's foreground process name, Path its current
	// directory, Active whether it is the window's active pane.
	Command string
	Path    string
	Active  bool
	// Status is the recorded state and Reported whether it is fresh enough to
	// trust. Recorded is true when any record exists at all, fresh or not:
	// an expired record is unknown state, not a session with no agent.
	Status     AgentStatus
	Reported   bool
	Recorded   bool
	ReportedAt time.Time
	// Invocation identifies the agent run that wrote the record, and Seq the
	// hook's own clock when it did, so a late report from an earlier run
	// cannot overwrite a later one.
	Invocation string
	Seq        int64
}

// Key identifies the pane across hosts and server generations.
func (p PaneReport) Key() string {
	return p.Host + "/" + p.Generation + "/" + p.PaneID
}

// ListPanesArgs builds the argv for listing every pane on the server in the
// format ParsePaneReports reads.
func ListPanesArgs() []string {
	return []string{"list-panes", "-a", "-F", paneFormat}
}

// FindPaneArgs builds the argv that lists exactly one pane by id, or nothing
// when it does not exist. display-message with a bad target silently falls
// back to the current pane, so it cannot be used to prove one exists.
func FindPaneArgs(paneID string) []string {
	return []string{"list-panes", "-a", "-f", "#{==:#{pane_id}," + paneID + "}", "-F", paneFormat}
}

// FindWindowArgs lists exactly one window by id in the list-windows format,
// or nothing when it is gone.
func FindWindowArgs(windowID string) []string {
	return []string{"list-windows", "-a", "-f", "#{==:#{window_id}," + windowID + "}", "-F", windowFormat}
}

// SetPaneStatusArgs records a status on one pane, with the time, the
// invocation that sent it, and the hook's sequence stamp, all in one
// invocation so a partial record can never exist.
func SetPaneStatusArgs(paneID string, st AgentStatus, invocation string, seq int64, now time.Time) []string {
	return []string{
		"set-option", "-p", "-t", paneID, paneStatusOption, statusName(st), ";",
		"set-option", "-p", "-t", paneID, paneStatusAtOption, strconv.FormatInt(now.Unix(), 10), ";",
		"set-option", "-p", "-t", paneID, paneInvocationOption, invocation, ";",
		"set-option", "-p", "-t", paneID, paneSeqOption, strconv.FormatInt(seq, 10),
	}
}

// ParsePaneReports reads list-panes output for one host. Session names are
// resolved through sessions, keyed by id.
func ParsePaneReports(out, host string, sessions []TmuxSession, now time.Time) []PaneReport {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	nameByID := make(map[string]string, len(sessions))
	for _, s := range sessions {
		if s.ID != "" {
			nameByID[s.ID] = s.Name
		}
	}

	var reports []PaneReport
	for _, line := range strings.Split(out, "\n") {
		parts := splitPaneLine(strings.TrimSpace(line))
		if len(parts) < paneLeadingFields+1 {
			continue
		}
		pid, _ := strconv.Atoi(parts[3])
		r := PaneReport{
			Host:       host,
			SessionID:  parts[0],
			Session:    nameByID[parts[0]],
			WindowID:   parts[1],
			PaneID:     parts[2],
			PanePID:    pid,
			Dead:       parts[4] == "1",
			Active:     parts[5] == "1",
			Command:    parts[6],
			Path:       parts[7],
			Invocation: parts[10],
			Generation: serverGeneration(parts[12], parts[13]),
			WindowName: strings.Join(parts[paneLeadingFields:], fieldSep),
		}
		r.Seq, _ = strconv.ParseInt(parts[11], 10, 64)
		if parts[8] != "" {
			r.Recorded = true
			if epoch, err := strconv.ParseInt(parts[9], 10, 64); err == nil {
				r.ReportedAt = time.Unix(epoch, 0)
			}
			// No window inheritance question here: a pane option is set on
			// the pane and nowhere else.
			r.Status, r.Reported = StatusFromOptions(parts[8], parts[9], "", "", now)
		}
		reports = append(reports, r)
	}
	return reports
}

// splitPaneLine splits a pane line on the separator, re-joining a
// backslash-escaped separator inside the quoted path field (index 7), which
// #{q:} produces, and unescaping the spaces it quotes.
func splitPaneLine(line string) []string {
	raw := strings.Split(line, fieldSep)
	var out []string
	for i := 0; i < len(raw); i++ {
		part := raw[i]
		if len(out) == 7 {
			for strings.HasSuffix(part, `\`) && i+1 < len(raw) {
				part = part[:len(part)-1] + fieldSep + raw[i+1]
				i++
			}
			part = strings.ReplaceAll(part, `\ `, " ")
		}
		out = append(out, part)
	}
	return out
}

// ApplyPaneReports folds per-pane records into their sessions. A session with
// any pane record takes its status from the panes — fresh needs-input first,
// then working, then idle — and ignores the legacy session-level option,
// which any hook in the session may have overwritten. A session whose only
// records have expired is unknown: expiry is not an answer. A session with
// no pane records keeps whatever the session-level option said.
func ApplyPaneReports(sessions []TmuxSession, panes []PaneReport) []TmuxSession {
	byID := make(map[string][]PaneReport)
	for _, p := range panes {
		if p.Recorded {
			byID[p.SessionID] = append(byID[p.SessionID], p)
		}
	}
	for i := range sessions {
		s := &sessions[i]
		records, ok := byID[s.ID]
		if !ok || s.ID == "" {
			continue
		}
		s.StatusSource = StatusSourcePane
		s.Status, s.StatusReported = RollupPaneStatus(records)
	}
	return sessions
}

// RollupPaneStatus reduces a session's pane records to one tile status. Only
// fresh records count; a pane that stopped reporting says nothing about the
// others and cannot make the session healthy.
func RollupPaneStatus(records []PaneReport) (AgentStatus, bool) {
	best := AgentStatusUnknown
	for _, r := range records {
		if !r.Reported {
			continue
		}
		if rank(r.Status) > rank(best) {
			best = r.Status
		}
	}
	return best, best != AgentStatusUnknown
}

func rank(st AgentStatus) int {
	switch st {
	case AgentStatusNeedsInput:
		return 3
	case AgentStatusWorking:
		return 2
	case AgentStatusIdle:
		return 1
	default:
		return 0
	}
}

// HostObservation is one successful read of a tmux server: its sessions, its
// pane records, and the generation that issued every id in it.
type HostObservation struct {
	Host       string
	Generation string
	Sessions   []TmuxSession
	Panes      []PaneReport
	ObservedAt time.Time
}

// ObserveHost reads sessions and pane records in two calls and joins them.
// No server is a verified empty observation. Any other failure is returned
// as is: a half-read host is not an observation.
func ObserveHost(ctx context.Context, r Runner, host string, now time.Time) (HostObservation, error) {
	obs := HostObservation{Host: host, ObservedAt: now}
	sessions, err := ListSessionsOn(ctx, r, host, now)
	if err != nil {
		return obs, err
	}
	if len(sessions) == 0 {
		return obs, nil
	}
	obs.Generation = sessions[0].Generation

	out, err := r.Run(ctx, ListPanesArgs()...)
	if err != nil {
		if IsNoServer(err) {
			// The server went away between the two calls; what it had is
			// gone with it.
			return HostObservation{Host: host, ObservedAt: now}, nil
		}
		return obs, err
	}
	obs.Panes = ParsePaneReports(out, host, sessions, now)
	obs.Sessions = ApplyPaneReports(sessions, obs.Panes)
	return obs, nil
}
