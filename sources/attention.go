package sources

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

// AttentionKind is what kind of thing needs a person.
type AttentionKind string

const (
	AttentionNeedsInput    AttentionKind = "needs_input"
	AttentionProcessExited AttentionKind = "process_exited"
	AttentionCIFailed      AttentionKind = "ci_failed"
	AttentionHermesDown    AttentionKind = "hermes_down"
	AttentionSpine         AttentionKind = "spine"
	AttentionUnpushed      AttentionKind = "unpushed"      // housekeeping
	AttentionStaleSession  AttentionKind = "stale_session" // housekeeping
)

// Observation says how much an item can be trusted right now.
type Observation string

const (
	ObservationFresh       Observation = "fresh"
	ObservationStale       Observation = "stale"       // last known; the source failed since
	ObservationUnavailable Observation = "unavailable" // the source cannot be read at all
)

// Evidence sources.
const (
	SourceHook    = "hook"
	SourceProcess = "process"
	SourceGitHub  = "github"
	SourceHermes  = "hermes"
	SourceSpine   = "spine"
	SourceGit     = "git"
	SourceTmux    = "tmux"
)

// Primary actions a row can offer. They are names the UI dispatches on; none
// of them launches a command.
const (
	ActionOpenAgent      = "open_agent"      // attach to the reporting pane
	ActionOpenSession    = "open_session"    // session details; attach if it exists
	ActionInspectProcess = "inspect_process" // process panel with the row selected
	ActionViewCI         = "view_ci"         // CI details
	ActionViewGateway    = "view_gateway"    // hermes details
	ActionOpenProject    = "open_project"    // project details
	ActionOpenSpine      = "open_spine"      // switch to the spine session
)

// AttentionTarget is the typed, validated destination of an item. Display
// names are never targets; identities are.
type AttentionTarget struct {
	Type       string `json:"type"` // pane, session, process, ci, hermes, spine, project
	Host       string `json:"host,omitempty"`
	Generation string `json:"generation,omitempty"`
	Session    string `json:"session,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	WindowID   string `json:"window_id,omitempty"`
	PaneID     string `json:"pane_id,omitempty"`
	Invocation string `json:"invocation,omitempty"`
	Project    string `json:"project,omitempty"` // repo key
	Process    string `json:"process,omitempty"`
	Repo       string `json:"repo,omitempty"` // owner/name
	Branch     string `json:"branch,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	RunURL     string `json:"run_url,omitempty"`
	Label      string `json:"label,omitempty"` // hermes label
	Goal       string `json:"goal,omitempty"`  // spine goal
	Stream     string `json:"stream,omitempty"`
	Agent      string `json:"agent,omitempty"`
}

// AttentionItem is one row of the queue.
type AttentionItem struct {
	ID            string          `json:"id"`
	Kind          AttentionKind   `json:"kind"`
	Priority      int             `json:"priority"`
	Housekeeping  bool            `json:"housekeeping"`
	Host          string          `json:"host,omitempty"`
	Project       string          `json:"project,omitempty"`
	Title         string          `json:"title"`
	Detail        string          `json:"detail,omitempty"`
	Action        string          `json:"action"`
	Source        string          `json:"source"`
	Observation   Observation     `json:"observation"`
	ObservedAt    time.Time       `json:"observed_at"`
	FirstObserved time.Time       `json:"first_observed"`
	Target        AttentionTarget `json:"target"`
	// Coverage notes a limitation of the evidence, such as a legacy
	// session-level report that cannot name the pane.
	Coverage string `json:"coverage,omitempty"`
}

// Actionable reports whether the primary action may run: only fresh items
// have targets that can be revalidated.
func (i AttentionItem) Actionable() bool { return i.Observation == ObservationFresh }

// Coverage is one source's read outcome, shown so that an empty queue can
// never claim more than it observed.
type Coverage struct {
	Source      string      `json:"source"`
	Host        string      `json:"host,omitempty"`
	Scope       string      `json:"scope,omitempty"`
	Observation Observation `json:"observation"`
	Detail      string      `json:"detail,omitempty"`
	ObservedAt  time.Time   `json:"observed_at,omitempty"`
}

// AttentionReport is the derived queue plus what it was derived from.
type AttentionReport struct {
	Items    []AttentionItem `json:"items"`
	Coverage []Coverage      `json:"coverage"`
	// Unavailable counts sources that could not be read; unknown coverage is
	// not zero work.
	Unavailable int `json:"unavailable"`
}

// Actionable returns the fresh non-housekeeping items, the badge count.
func (r AttentionReport) Actionable() int {
	n := 0
	for _, i := range r.Items {
		if !i.Housekeeping && i.Observation == ObservationFresh {
			n++
		}
	}
	return n
}

// HostReport is one host's observations for the collector. Local is the
// empty host. Outcome unavailable with data attached means the data is
// last-known and every item from it is stale.
type HostReport struct {
	Host        string
	Outcome     Observation
	Err         string
	ObservedAt  time.Time
	Sessions    []TmuxSession
	Panes       []PaneReport
	Processes   map[string]ProcessObservation // repo key → observation
	ProcessErrs map[string]string             // repo key → why it could not be read
	Git         []GitRepoStatus
}

// AttentionInput is everything the queue is derived from.
type AttentionInput struct {
	Config      config.SignalsConfig
	SelfSession string
	Hosts       []HostReport
	GitHub      *GitHubStatus
	GitHubAt    time.Time
	Hermes      []HermesStatus
	HermesAt    time.Time
	Spine       *SpineStatus // nil when the source is not polled
	SpineAt     time.Time
	Now         time.Time
}

// DeriveAttention computes the queue. It is pure: occurrence tracking and
// sorting by first sight belong to the caller's tracker.
func DeriveAttention(in AttentionInput) AttentionReport {
	var rep AttentionReport
	for _, h := range in.Hosts {
		rep.deriveHost(in, h)
	}
	rep.deriveGitHub(in)
	rep.deriveHermes(in)
	rep.deriveSpine(in)
	for _, c := range rep.Coverage {
		if c.Observation == ObservationUnavailable {
			rep.Unavailable++
		}
	}
	if rep.Items == nil {
		rep.Items = []AttentionItem{}
	}
	if rep.Coverage == nil {
		rep.Coverage = []Coverage{}
	}
	return rep
}

func hostName(h string) string {
	if h == "" {
		return "local"
	}
	return h
}

func (rep *AttentionReport) deriveHost(in AttentionInput, h HostReport) {
	obs := ObservationFresh
	if h.Outcome == ObservationUnavailable {
		obs = ObservationStale
	}
	cov := Coverage{Source: SourceTmux, Host: h.Host, Scope: hostName(h.Host), Observation: h.Outcome, ObservedAt: h.ObservedAt}
	if h.Outcome == ObservationUnavailable {
		cov.Detail = "unavailable"
		if h.Err != "" {
			cov.Detail = "unavailable: " + h.Err
		}
	} else {
		cov.Detail = fmt.Sprintf("%d %s", len(h.Sessions), plural(len(h.Sessions), "session"))
	}
	rep.Coverage = append(rep.Coverage, cov)

	// Pane reports: one item per fresh blocked pane. Sessions covered by
	// pane records never fall back to the session-level report.
	covered := map[string]bool{}
	for _, p := range h.Panes {
		if p.Recorded {
			covered[p.SessionID] = true
		}
		if !p.Reported || p.Status != AgentStatusNeedsInput {
			continue
		}
		session := p.Session
		if session == in.SelfSession {
			continue
		}
		title := hostName(h.Host) + "/" + session
		if p.WindowName != "" {
			title += " · " + p.WindowName
		}
		rep.Items = append(rep.Items, AttentionItem{
			ID:          fmt.Sprintf("needs_input:%s/%s/%s:%s", h.Host, p.Generation, p.PaneID, p.Invocation),
			Kind:        AttentionNeedsInput,
			Priority:    1,
			Host:        h.Host,
			Project:     projectKey(h.Host, session),
			Title:       title,
			Detail:      "Needs input",
			Action:      ActionOpenAgent,
			Source:      SourceHook,
			Observation: obs,
			ObservedAt:  h.ObservedAt,
			Target: AttentionTarget{
				Type: "pane", Host: h.Host, Generation: p.Generation, Session: session,
				SessionID: p.SessionID, WindowID: p.WindowID, PaneID: p.PaneID, Invocation: p.Invocation,
			},
		})
	}

	legacy := 0
	for _, s := range h.Sessions {
		if s.Name == in.SelfSession || s.ViewOf != "" {
			continue
		}
		// A reported status with no pane record behind it is a session-level
		// report, whether the parser labelled it or an older caller built
		// the session by hand.
		sessionLevel := s.StatusSource == StatusSourceSession || (s.StatusReported && s.StatusSource == "")
		if sessionLevel && !covered[s.ID] {
			legacy++
			if s.StatusReported && s.Status == AgentStatusNeedsInput {
				rep.Items = append(rep.Items, AttentionItem{
					ID:          fmt.Sprintf("needs_input:%s/%s:session", h.Host, sessionIdentity(s)),
					Kind:        AttentionNeedsInput,
					Priority:    1,
					Host:        h.Host,
					Project:     projectKey(h.Host, s.Name),
					Title:       hostName(h.Host) + "/" + s.Name,
					Detail:      "Needs input (session report)",
					Action:      ActionOpenSession,
					Source:      SourceHook,
					Observation: obs,
					ObservedAt:  h.ObservedAt,
					Coverage:    "Reported at session level by an older hook; the exact pane is not known.",
					Target: AttentionTarget{
						Type: "session", Host: h.Host, Generation: s.Generation, Session: s.Name, SessionID: s.ID,
					},
				})
			}
		}
	}
	if legacy > 0 {
		rep.Coverage = append(rep.Coverage, Coverage{
			Source: SourceHook, Host: h.Host, Scope: hostName(h.Host), Observation: h.Outcome,
			Detail:     fmt.Sprintf("%d %s report at session level only (limited precision; update the hooks)", legacy, plural(legacy, "session")),
			ObservedAt: h.ObservedAt,
		})
	}

	// Processes.
	keys := make([]string, 0, len(h.Processes))
	for k := range h.Processes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		po := h.Processes[key]
		for _, p := range po.Processes {
			if !p.Configured || p.Outcome != OutcomeExited || p.DesiredState == DesiredStopped {
				continue
			}
			detail := p.Display
			rep.Items = append(rep.Items, AttentionItem{
				ID:          fmt.Sprintf("process:%s:%s:%s/%s", key, p.Name, p.Generation, p.WindowID),
				Kind:        AttentionProcessExited,
				Priority:    2,
				Host:        h.Host,
				Project:     key,
				Title:       hostName(h.Host) + "/" + po.Session + " · " + p.Name,
				Detail:      detail,
				Action:      ActionInspectProcess,
				Source:      SourceProcess,
				Observation: obs,
				ObservedAt:  po.ObservedAt,
				Target: AttentionTarget{
					Type: "process", Host: h.Host, Generation: p.Generation, Session: po.Session,
					WindowID: p.WindowID, PaneID: p.PaneID, Project: key, Process: p.Name,
				},
			})
		}
	}
	errKeys := make([]string, 0, len(h.ProcessErrs))
	for k := range h.ProcessErrs {
		errKeys = append(errKeys, k)
	}
	sort.Strings(errKeys)
	for _, key := range errKeys {
		rep.Coverage = append(rep.Coverage, Coverage{
			Source: SourceProcess, Host: h.Host, Scope: key, Observation: ObservationUnavailable,
			Detail: "processes unreadable: " + h.ProcessErrs[key], ObservedAt: h.ObservedAt,
		})
	}

	// Housekeeping.
	if in.Config.ShowUnpushed {
		for _, g := range h.Git {
			if g.Error != nil || g.Unpushed == 0 {
				continue
			}
			rep.Items = append(rep.Items, AttentionItem{
				ID:           "unpushed:" + g.Key(),
				Kind:         AttentionUnpushed,
				Priority:     5,
				Housekeeping: true,
				Host:         h.Host,
				Project:      g.Key(),
				Title:        hostName(h.Host) + "/" + g.Label,
				Detail:       fmt.Sprintf("%d unpushed %s", g.Unpushed, plural(g.Unpushed, "commit")),
				Action:       ActionOpenProject,
				Source:       SourceGit,
				Observation:  obs,
				ObservedAt:   h.ObservedAt,
				Target:       AttentionTarget{Type: "project", Host: h.Host, Project: g.Key(), Session: g.Label, Branch: g.Branch},
			})
		}
	}
	if in.Config.ShowStaleSessions {
		threshold, err := time.ParseDuration(in.Config.StaleSessionThreshold)
		if err != nil || threshold <= 0 {
			threshold = defaultStaleThreshold
		}
		for _, s := range h.Sessions {
			if s.Name == in.SelfSession || s.ViewOf != "" || s.Attached || in.Now.Sub(s.LastUsed) < threshold {
				continue
			}
			rep.Items = append(rep.Items, AttentionItem{
				ID:           fmt.Sprintf("stale:%s/%s", h.Host, sessionIdentity(s)),
				Kind:         AttentionStaleSession,
				Priority:     6,
				Housekeeping: true,
				Host:         h.Host,
				Project:      projectKey(h.Host, s.Name),
				Title:        hostName(h.Host) + "/" + s.Name,
				Detail:       fmt.Sprintf("idle %s", formatAge(in.Now.Sub(s.LastUsed))),
				Action:       ActionOpenSession,
				Source:       SourceTmux,
				Observation:  obs,
				ObservedAt:   h.ObservedAt,
				Target:       AttentionTarget{Type: "session", Host: h.Host, Generation: s.Generation, Session: s.Name, SessionID: s.ID},
			})
		}
	}
}

func (rep *AttentionReport) deriveGitHub(in AttentionInput) {
	if in.GitHub == nil {
		return
	}
	gh := in.GitHub
	obs := ObservationFresh
	if gh.Error != nil {
		obs = ObservationStale
	}
	checked, unsupported, failed := 0, 0, 0
	for _, c := range gh.RepoChecks {
		switch c.Coverage {
		case CoverageChecked:
			checked++
		case CoverageRemoteUnsupported:
			unsupported++
		case CoverageError:
			failed++
		}
	}
	detail := fmt.Sprintf("%d %s checked on %s", checked, plural(checked, "repo"), ciBranch)
	if unsupported > 0 {
		detail += fmt.Sprintf(", %d remote not checked (unsupported)", unsupported)
	}
	if failed > 0 {
		detail += fmt.Sprintf(", %d unreadable", failed)
	}
	cov := Coverage{Source: SourceGitHub, Scope: "github", Observation: obs, Detail: detail, ObservedAt: in.GitHubAt}
	if gh.Error != nil {
		cov.Observation = ObservationUnavailable
		cov.Detail = "unavailable: " + gh.Error.Error()
	}
	rep.Coverage = append(rep.Coverage, cov)

	if !in.Config.ShowFailingCI {
		return
	}
	for _, c := range gh.RepoChecks {
		if c.CIStatus != "failing" {
			continue
		}
		rep.Items = append(rep.Items, AttentionItem{
			ID:          "ci:" + c.RepoLabel + ":" + c.RunID,
			Kind:        AttentionCIFailed,
			Priority:    3,
			Project:     c.RepoLabel,
			Title:       "local/" + c.RepoLabel + " · " + c.Branch,
			Detail:      "CI failed",
			Action:      ActionViewCI,
			Source:      SourceGitHub,
			Observation: obs,
			ObservedAt:  in.GitHubAt,
			Coverage:    "Latest run on " + c.Branch + "; feature branches are not checked.",
			Target: AttentionTarget{
				Type: "ci", Project: c.RepoLabel, Repo: c.Repo, Branch: c.Branch, RunID: c.RunID, RunURL: c.RunURL,
			},
		})
	}
}

func (rep *AttentionReport) deriveHermes(in AttentionInput) {
	for _, h := range in.Hermes {
		cov := Coverage{Source: SourceHermes, Host: h.Host, Scope: h.Label, Observation: ObservationFresh, ObservedAt: in.HermesAt}
		if !h.Reachable {
			cov.Observation = ObservationUnavailable
			cov.Detail = "dashboard unreachable"
			if h.Err != nil {
				cov.Detail += ": " + h.Err.Error()
			}
			rep.Coverage = append(rep.Coverage, cov)
			continue
		}
		cov.Detail = "gateway " + h.Gateway
		rep.Coverage = append(rep.Coverage, cov)
		if h.Gateway == "running" {
			continue
		}
		rep.Items = append(rep.Items, AttentionItem{
			ID:          "hermes:" + h.Label,
			Kind:        AttentionHermesDown,
			Priority:    4,
			Host:        h.Host,
			Title:       h.Label,
			Detail:      "Gateway " + h.Gateway,
			Action:      ActionViewGateway,
			Source:      SourceHermes,
			Observation: ObservationFresh,
			ObservedAt:  in.HermesAt,
			Target:      AttentionTarget{Type: "hermes", Host: h.Host, Label: h.Label},
		})
	}
}

func (rep *AttentionReport) deriveSpine(in AttentionInput) {
	if in.Spine == nil {
		return
	}
	cov := Coverage{Source: SourceSpine, Scope: "spine", Observation: ObservationFresh, ObservedAt: in.SpineAt}
	if !in.Spine.Readable() {
		cov.Observation = ObservationUnavailable
		cov.Detail = "unavailable"
		if in.Spine.Err != nil {
			cov.Detail += ": " + in.Spine.Err.Error()
		}
		rep.Coverage = append(rep.Coverage, cov)
		return
	}
	n := len(in.Spine.Snapshot.NeedsYou)
	cov.Detail = fmt.Sprintf("%d %s needs you", n, plural(n, "item"))
	rep.Coverage = append(rep.Coverage, cov)
	for _, it := range in.Spine.Snapshot.NeedsYou {
		title := it.Goal
		if title == "" {
			title = it.Repo
		}
		title += " · " + it.Title
		detail := it.Kind
		if it.Agent != "" {
			detail += " from " + it.Agent
		}
		rep.Items = append(rep.Items, AttentionItem{
			ID:          "spine:" + strings.Join([]string{it.Repo, it.Goal, it.Stream, it.Agent, it.Title}, "/"),
			Kind:        AttentionSpine,
			Priority:    1,
			Project:     it.Repo,
			Title:       title,
			Detail:      detail,
			Action:      ActionOpenSpine,
			Source:      SourceSpine,
			Observation: ObservationFresh,
			ObservedAt:  in.SpineAt,
			Target: AttentionTarget{
				Type: "spine", Project: it.Repo, Goal: it.Goal, Stream: it.Stream, Agent: it.Agent,
			},
		})
	}
}

func projectKey(host, label string) string {
	if host == "" {
		return label
	}
	return host + "/" + label
}

func sessionIdentity(s TmuxSession) string {
	if s.ID != "" {
		return s.Generation + "/" + s.ID
	}
	return s.Name
}

// AttentionTracker remembers when each item was first seen, in memory only.
// It sorts by priority, then oldest first sight, then id, and keeps the
// order stable across polls so a row does not move under the cursor.
type AttentionTracker struct {
	first map[string]time.Time
}

// Track stamps first-observed times and sorts the report in place.
func (t *AttentionTracker) Track(rep *AttentionReport, now time.Time) {
	if t.first == nil {
		t.first = map[string]time.Time{}
	}
	seen := map[string]bool{}
	for i := range rep.Items {
		id := rep.Items[i].ID
		seen[id] = true
		if _, ok := t.first[id]; !ok {
			t.first[id] = now
		}
		rep.Items[i].FirstObserved = t.first[id]
	}
	for id := range t.first {
		if !seen[id] {
			delete(t.first, id)
		}
	}
	SortAttention(rep.Items)
}

// SortAttention orders items by priority, first sight, then id.
func SortAttention(items []AttentionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.FirstObserved.Equal(b.FirstObserved) {
			return a.FirstObserved.Before(b.FirstObserved)
		}
		return a.ID < b.ID
	})
}

// FilterAttention keeps items whose host, project, kind, title or detail
// contains the query, case-insensitively. Coverage is never filtered.
func FilterAttention(items []AttentionItem, query string) []AttentionItem {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return items
	}
	var out []AttentionItem
	for _, i := range items {
		hay := strings.ToLower(strings.Join([]string{hostName(i.Host), i.Project, string(i.Kind), i.Title, i.Detail}, " "))
		if strings.Contains(hay, query) {
			out = append(out, i)
		}
	}
	return out
}
