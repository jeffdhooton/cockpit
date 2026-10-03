package daemon

import (
	"crypto/hmac"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jeffdhooton/cockpit/sources"
)

// maxStatusBytes bounds a hook payload. Claude's Stop event can carry an
// entire assistant message, so intake is bounded to stop a large private reply
// crowding out the handful of fields we actually want.
const maxStatusBytes = 16 << 10

// eventTables maps each engine's hook events onto a state.
//
// The four shared names mean the same thing in both engines, and the two that
// diverge use disjoint names, so one merged lookup would give correct answers
// today. Keeping them apart records whose behaviour each claim describes — a
// mapping is a statement about one engine at one version — and makes a third
// engine a table entry rather than a new code path.
//
// Codex's PermissionRequest fires for exactly one reason. Claude's
// Notification is broader, covering a permission prompt or an idle-waiting
// notice, so a Claude tile shows needs_input in some cases a Codex tile would
// not. That is a real difference in meaning, not a naming inconsistency.
var eventTables = map[string]map[string]sources.AgentStatus{
	"claude": {
		"UserPromptSubmit": sources.AgentStatusWorking,
		"PreToolUse":       sources.AgentStatusWorking,
		"Notification":     sources.AgentStatusNeedsInput,
		"Stop":             sources.AgentStatusIdle,
	},
	"codex": {
		"UserPromptSubmit":  sources.AgentStatusWorking,
		"PreToolUse":        sources.AgentStatusWorking,
		"PermissionRequest": sources.AgentStatusNeedsInput,
		"Stop":              sources.AgentStatusIdle,
	},
}

// stateFor resolves one engine's event to a state. An event in no table sets
// nothing rather than falling through to a guess.
func stateFor(engine, event string) (sources.AgentStatus, bool) {
	table, ok := eventTables[engine]
	if !ok {
		return sources.AgentStatusUnknown, false
	}
	st, ok := table[event]
	return st, ok
}

// statusPayload is the allowlist. Anything absent from this struct is dropped
// rather than forwarded, which is the whole point: only enough to colour a
// tile ever reaches a tmux option.
//
// Invocation is the agent run that sent the report — the engine's own
// session id, which both engines put on every event — and Seq is the hook's
// clock when it sent it. Together they let a late report from an exited run
// be refused rather than recolour its replacement.
type statusPayload struct {
	Engine     string `json:"engine"`
	Event      string `json:"hook_event_name"`
	Target     string `json:"target"`
	Invocation string `json:"invocation"`
	Seq        int64  `json:"seq"`
}

const (
	maxEngineLen     = 16
	maxEventLen      = 32
	maxTargetLen     = 128
	maxInvocationLen = 64
)

// statusMu serialises the read-then-write of a pane's record. The daemon is
// the only writer on its server, so a process mutex is the right scope here;
// project lifecycle uses the tmux-side lock because it has many writers.
var statusMu sync.Mutex

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("mcp-session-id", s.SessionID)

	// A write route must not skip the guard that closed the browser hole.
	if !guard(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.StatusKey == nil || s.Runner == nil {
		http.Error(w, "status reporting is not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStatusBytes)
	var p statusPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	engine := clip(p.Engine, maxEngineLen)
	event := clip(p.Event, maxEventLen)
	target := clip(p.Target, maxTargetLen)
	invocation := clip(p.Invocation, maxInvocationLen)
	if target == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	// Compare in constant time, and against this target only: a token
	// authorises one session, not every session.
	want := StatusToken(s.StatusKey, target)
	got := r.Header.Get("x-cockpit-status-token")
	if !hmac.Equal([]byte(want), []byte(got)) {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}

	st, ok := stateFor(engine, event)
	if !ok {
		// Not an error. Most events in both engines are ones we ignore, and a
		// hook firing them must not see a failure.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	now := time.Now()
	t := sources.ParseStatusTarget(target)
	if t.Legacy {
		if _, err := s.Runner.Run(r.Context(),
			sources.SetStatusArgs(t.Session, st, t.Window, now)...); err != nil {
			http.Error(w, "could not record status", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	statusMu.Lock()
	defer statusMu.Unlock()

	// Bind the report to the live pane. A pane that is gone, or whose id now
	// belongs to a different server generation, session or window, is a
	// replacement: the report is for something that no longer exists.
	out, err := s.Runner.Run(r.Context(), sources.FindPaneArgs(t.PaneID)...)
	if err != nil {
		if sources.IsVerifiedAbsence(err) {
			http.Error(w, "target is gone", http.StatusConflict)
			return
		}
		http.Error(w, "could not read target", http.StatusInternalServerError)
		return
	}
	panes := sources.ParsePaneReports(out, "", nil, now)
	if len(panes) != 1 {
		http.Error(w, "target is gone", http.StatusConflict)
		return
	}
	live := panes[0]
	if live.Generation != t.Generation || live.SessionID != t.SessionID || live.WindowID != t.WindowID {
		http.Error(w, "target was replaced", http.StatusConflict)
		return
	}
	// A report older than the one already recorded is late. It is dropped
	// whether it comes from the same run out of order or from an earlier
	// run that has since been replaced.
	if p.Seq != 0 && live.Seq > p.Seq {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The pane record is authoritative for new readers; the session-level
	// record keeps older cockpit binaries reading something.
	args := sources.SetPaneStatusArgs(t.PaneID, st, invocation, p.Seq, now)
	args = append(args, ";")
	args = append(args, sources.SetStatusArgs(t.SessionID, st, live.WindowName, now)...)
	if _, err := s.Runner.Run(r.Context(), args...); err != nil {
		http.Error(w, "could not record status", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clip bounds a field to its cap. Truncating beats rejecting: a slightly long
// tool name should still colour the tile.
func clip(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
