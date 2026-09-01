# Hook-Driven Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Cockpit's pane-hash status guess with real lifecycle events posted by Claude Code and Codex hooks, adding a `needs_input` state the guess cannot represent.

**Architecture:** An agent hook runs `cockpit hook status`, which reads the event JSON on stdin and POSTs it to the existing loopback daemon. The daemon maps the event to a state through a per-engine table and writes it into tmux as session user options. The TUI reads those options as extra `list-sessions` format fields on a call it already makes, which removes the per-session `capture-pane` subprocess rather than adding one. The daemon stays stateless: tmux owns the status, so it dies with the session and survives a daemon restart.

**Tech Stack:** Go 1.22+, cobra, bubbletea/lipgloss, tmux user options, `net/http` on loopback. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-01-hook-driven-status-design.md`

## Global Constraints

- **No new dependencies.** Standard library plus what the repo already imports.
- **`cockpit hook status` always exits 0.** No daemon, no tmux, malformed stdin, empty payload, timeout — all exit 0. A hook that fails, fails the agent that called it. This is the single most important property in the design.
- **500ms total timeout** for the hook subcommand.
- **Resolve tmux by absolute path.** The hook inherits whatever environment the agent hands it. Under launchd, `PATH` was `/usr/bin:/bin:/usr/sbin:/sbin` — no Homebrew, so no tmux.
- **Field separator stays `|`.** tmux replaces non-printable separators with `_` when no UTF-8 locale is set. Never use a tab.
- **A failed lookup is never an empty result.** Distinguish "no server / no session" from "could not ask".
- **Never invent a state.** An unrecognised event name sets nothing.
- **Status staleness threshold: 10 minutes** (`statusStaleAfter`).
- **All new daemon routes go behind `guard`** (`daemon/mcp.go`). A write endpoint that skips it reopens what the guard closed.
- **Markers:** `○`/`●` mean session absent/present (`StatusRing`/`StatusDot`). Reported-vs-inferred is carried by **dimming the label**, never by a third glyph.
- Run `go build ./... && go vet ./... && go test ./...` before every commit.

---

### Task 1: Rename `ClaudeStatus` to `AgentStatus` and add `NeedsInput`

Pure rename plus one new constant, kept separate from behaviour so the behaviour commits stay readable. 22 non-test references.

**Files:**
- Modify: `sources/tmux.go:128-135` (type and constants)
- Modify: every referencing file — `tui/sessions.go`, `tui/grid.go`, `tui/app.go`, `tui/repos.go`, and their tests

**Interfaces:**
- Consumes: nothing
- Produces: `sources.AgentStatus` with `AgentStatusUnknown`, `AgentStatusIdle`, `AgentStatusWorking`, `AgentStatusNeedsInput`

- [ ] **Step 1: Write the failing test**

Add to `sources/tmux_test.go`:

```go
func TestAgentStatusNeedsInputIsDistinct(t *testing.T) {
	// needs_input is the state pane hashing cannot represent. It must not
	// collide with idle, which is what a blocked agent looks like from outside.
	if AgentStatusNeedsInput == AgentStatusIdle {
		t.Fatal("needs_input must be distinct from idle")
	}
	if AgentStatusNeedsInput == AgentStatusUnknown {
		t.Fatal("needs_input must be distinct from unknown")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./sources/ -run TestAgentStatusNeedsInput`
Expected: FAIL — `undefined: AgentStatusNeedsInput`

- [ ] **Step 3: Rename the type and add the constant**

In `sources/tmux.go`, replace lines 128-135:

```go
// AgentStatus represents the reported or inferred state of a coding agent
// running in a tmux session. Both Claude Code and Codex report into it.
type AgentStatus int

const (
	AgentStatusUnknown AgentStatus = iota
	AgentStatusIdle                // the turn ended
	AgentStatusWorking             // acting: a prompt landed or a tool started
	AgentStatusNeedsInput          // blocked on you: a permission prompt or a notification
)
```

- [ ] **Step 4: Update every call site**

```bash
grep -rl 'ClaudeStatus' --include='*.go' . | xargs sed -i '' \
  -e 's/ClaudeStatusUnknown/AgentStatusUnknown/g' \
  -e 's/ClaudeStatusIdle/AgentStatusIdle/g' \
  -e 's/ClaudeStatusWorking/AgentStatusWorking/g' \
  -e 's/ClaudeStatus/AgentStatus/g'
```

Then fix the two stale comments by hand: `tui/sessions.go:18` (`session name → detected status`) and `tui/sessions.go:94` (`Claude Code status indicator (from content-hash diffing)`) — the second becomes `// Status indicator: reported when available, hashed otherwise.`

- [ ] **Step 5: Verify and commit**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages ok

```bash
git add -A
git commit -m "Rename ClaudeStatus to AgentStatus"
```

---

### Task 2: The tmux status store

Argv builders and parsing for the session user options. Pure functions, no I/O, matching the existing `sources/tmux_args.go` pattern.

**Files:**
- Create: `sources/status.go`
- Create: `sources/status_test.go`

**Interfaces:**
- Consumes: `AgentStatus` (Task 1), `Target()` and `fieldSep` from `sources/tmux_args.go`
- Produces:
  - `const statusStaleAfter = 10 * time.Minute`
  - `func SetStatusArgs(session string, st AgentStatus, window string, now time.Time) []string`
  - `func ClearStatusArgs(session string) []string`
  - `func StatusFromOptions(raw, at, window, wantWindow string, now time.Time) (AgentStatus, bool)` — the bool is `reported`
  - `func statusName(st AgentStatus) string` / `func statusFromName(s string) (AgentStatus, bool)`

- [ ] **Step 1: Write the failing tests**

Create `sources/status_test.go`:

```go
package sources

import (
	"slices"
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
	old := now.Add(-11 * time.Minute).Unix()

	_, reported := StatusFromOptions("working", itoa(old), "dev", "dev", now)
	if reported {
		t.Error("a status older than the staleness window is not reported")
	}
}

func TestStatusFromOptionsRejectsInheritedWindowValue(t *testing.T) {
	// Window options inherit from the session when unset, so a window with no
	// status of its own reports the session's. A mismatch means inherited.
	now := time.Unix(1788250998, 0)
	_, reported := StatusFromOptions("working", "1788250990", "dev", "zsh", now)
	if reported {
		t.Error("a value inherited from another window is not this window's report")
	}
}

func TestStatusFromOptionsRejectsUnknownName(t *testing.T) {
	now := time.Unix(1788250998, 0)
	if _, reported := StatusFromOptions("banana", "1788250990", "dev", "dev", now); reported {
		t.Error("an unrecognised status name must not be invented into a state")
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
```

Add `"strconv"` to that file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./sources/ -run TestStatus -v`
Expected: FAIL — `undefined: SetStatusArgs`, `undefined: StatusFromOptions`

- [ ] **Step 3: Write the implementation**

Create `sources/status.go`:

```go
package sources

import (
	"strconv"
	"time"
)

// Status is stored in tmux itself rather than in the daemon, so it lives
// exactly as long as the session, dies with it, and survives a daemon
// restart. The daemon writes through and reads back; it owns nothing.
const (
	statusOption       = "@cockpit_status"
	statusAtOption     = "@cockpit_status_at"
	statusWindowOption = "@cockpit_status_window"

	// statusStaleAfter bounds how long a reported status is trusted. A crashed
	// agent stops sending events, and a "working" option would otherwise
	// persist forever.
	statusStaleAfter = 10 * time.Minute
)

// SetStatusArgs builds the argv that records a status on a session, alongside
// the time it arrived and the window that reported it.
//
// All three are written in one invocation. A follow-up call could arrive after
// the session is gone, leaving a status with no timestamp — which reads as
// permanently fresh.
func SetStatusArgs(session string, st AgentStatus, window string, now time.Time) []string {
	return []string{
		"set-option", "-t", session,
		statusOption, statusName(st), ";",
		"set-option", "-t", session,
		statusAtOption, strconv.FormatInt(now.Unix(), 10), ";",
		"set-option", "-t", session,
		statusWindowOption, window,
	}
}

// ClearStatusArgs builds the argv that removes a session's status, returning it
// to the inferred path.
func ClearStatusArgs(session string) []string {
	return []string{"set-option", "-t", session, "-u", statusOption}
}

// StatusFromOptions turns the three raw option values into a status, reporting
// whether it may be trusted as reported rather than inferred.
//
// Fails closed in every uncertain case: unset, unparseable, stale, inherited
// from another window, or a name from no engine's table.
func StatusFromOptions(raw, at, window, wantWindow string, now time.Time) (AgentStatus, bool) {
	st, ok := statusFromName(raw)
	if !ok {
		return AgentStatusUnknown, false
	}

	// Window options inherit from the session when unset, so a value whose
	// reporting window is not this one belongs to a sibling.
	if window != "" && wantWindow != "" && window != wantWindow {
		return AgentStatusUnknown, false
	}

	epoch, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return AgentStatusUnknown, false
	}
	if now.Sub(time.Unix(epoch, 0)) > statusStaleAfter {
		return AgentStatusUnknown, false
	}
	return st, true
}

func statusName(st AgentStatus) string {
	switch st {
	case AgentStatusIdle:
		return "idle"
	case AgentStatusWorking:
		return "working"
	case AgentStatusNeedsInput:
		return "needs_input"
	default:
		return ""
	}
}

func statusFromName(s string) (AgentStatus, bool) {
	switch s {
	case "idle":
		return AgentStatusIdle, true
	case "working":
		return AgentStatusWorking, true
	case "needs_input":
		return AgentStatusNeedsInput, true
	default:
		return AgentStatusUnknown, false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./sources/ -run TestStatus -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add sources/status.go sources/status_test.go
git commit -m "Store agent status in tmux session options"
```

---

### Task 3: Read status through the existing `list-sessions` poll

This is where the design pays for itself: status becomes two more format fields on a call the TUI already makes every tick.

**Files:**
- Modify: `sources/tmux_args.go:25` (`sessionFormat`)
- Modify: `sources/tmux.go:92-126` (`parseTmuxOutput`), `sources/source.go:6-11` (`TmuxSession`)
- Modify: `sources/tmux_args_test.go`, `sources/tmux_test.go`

**Interfaces:**
- Consumes: `StatusFromOptions` (Task 2)
- Produces: `TmuxSession.Status AgentStatus` and `TmuxSession.StatusReported bool`

**Critical detail:** `parseTmuxOutput` anchors the session name on the *trailing* fields because a name may contain `|`. Adding three fields changes `nameEnd` from `len(parts)-3` to `len(parts)-6`. Getting this wrong silently truncates every session name that contains the separator.

- [ ] **Step 1: Write the failing tests**

Add to `sources/tmux_test.go`:

```go
func TestParseSessionsReadsReportedStatus(t *testing.T) {
	out := "app|3|1|1788250990|working|1788250990|dev\n"
	sessions, err := parseTmuxOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	if sessions[0].Name != "app" {
		t.Errorf("name = %q", sessions[0].Name)
	}
	if !sessions[0].StatusReported || sessions[0].Status != AgentStatusWorking {
		t.Errorf("want a reported working status, got %+v", sessions[0])
	}
}

func TestParseSessionsHandlesUnsetStatusOptions(t *testing.T) {
	// A session that never reported yields empty option fields, not an error.
	out := "docket|1|0|1788250990|||\n"
	sessions, err := parseTmuxOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "docket" {
		t.Fatalf("got %+v", sessions)
	}
	if sessions[0].StatusReported {
		t.Error("a session with no status must not read as reported")
	}
}

func TestParseSessionsKeepsSeparatorInName(t *testing.T) {
	// The name is anchored on the trailing fixed fields precisely so a name
	// containing the separator survives.
	out := "a|b|2|0|1788250990|idle|1788250990|zsh\n"
	sessions, _ := parseTmuxOutput(out)
	if len(sessions) != 1 || sessions[0].Name != "a|b" {
		t.Fatalf("name = %q, want \"a|b\"", sessions[0].Name)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./sources/ -run TestParseSessions -v`
Expected: FAIL — `sessions[0].StatusReported undefined`

- [ ] **Step 3: Extend the format and the struct**

`sources/tmux_args.go:25`:

```go
// sessionFormat is the list-sessions format string parseTmuxOutput expects.
// The three @cockpit_* fields are status, and they ride along on a call the
// TUI already makes every tick — which is what lets the per-session
// capture-pane loop go away rather than being added to.
const sessionFormat = "#{session_name}|#{session_windows}|#{session_attached}|#{session_last_attached}|#{@cockpit_status}|#{@cockpit_status_at}|#{@cockpit_status_window}"
```

`sources/source.go`, in `TmuxSession`:

```go
// TmuxSession represents a single tmux session.
type TmuxSession struct {
	Name     string
	Windows  int
	Attached bool
	LastUsed time.Time
	// Status is the agent state. StatusReported distinguishes a status an
	// agent hook actually sent from one the pane-hash fallback guessed, so the
	// display never presents a guess as a fact.
	Status         AgentStatus
	StatusReported bool
}
```

- [ ] **Step 4: Update the parser**

In `sources/tmux.go`, `parseTmuxOutput`: change the minimum field guard from `< 4` to `< 7`, and:

```go
		// Layout is name | windows | attached | last_attached | status |
		// status_at | status_window, and the name may contain the separator,
		// so anchor on the six fixed fields at the end.
		nameEnd := len(parts) - 6
		if nameEnd < 1 {
			continue
		}
		windows, _ := strconv.Atoi(parts[nameEnd])
		attached := parts[nameEnd+1] == "1"
		epoch, _ := strconv.ParseInt(parts[nameEnd+2], 10, 64)
		lastUsed := time.Unix(epoch, 0)

		name := strings.Join(parts[:nameEnd], fieldSep)
		status, reported := StatusFromOptions(
			parts[nameEnd+3], parts[nameEnd+4], parts[nameEnd+5], parts[nameEnd+5], time.Now())

		sessions = append(sessions, TmuxSession{
			Name:           name,
			Windows:        windows,
			Attached:       attached,
			LastUsed:       lastUsed,
			Status:         status,
			StatusReported: reported,
		})
```

Passing `parts[nameEnd+5]` as both `window` and `wantWindow` reads the session-level value as authoritative; the inheritance check matters only when reading a specific window, which the grid does not do.

- [ ] **Step 5: Run the full sources suite**

Run: `go test ./sources/ -v 2>&1 | tail -20`
Expected: PASS, including the pre-existing session-parsing tests

- [ ] **Step 6: Commit**

```bash
git add sources/tmux.go sources/tmux_args.go sources/source.go sources/tmux_test.go
git commit -m "Read agent status from the session list"
```

---

### Task 4: The status key and its token

A derived token rather than a stored one, so the daemon keeps owning nothing.

**Files:**
- Create: `daemon/statuskey.go`, `daemon/statuskey_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func StatusToken(key []byte, target string) string`, `func LoadOrCreateStatusKey(dir string) ([]byte, error)`

- [ ] **Step 1: Write the failing tests**

Create `daemon/statuskey_test.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusTokenIsStableAndTargetBound(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	if StatusToken(key, "app:dev") != StatusToken(key, "app:dev") {
		t.Error("the same target must derive the same token")
	}
	if StatusToken(key, "app:dev") == StatusToken(key, "other:dev") {
		t.Error("a token for one target must not authorise another")
	}
}

func TestStatusKeyIsCreatedPrivateAndReused(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateStatusKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Errorf("key length = %d, want 32", len(first))
	}

	info, err := os.Stat(filepath.Join(dir, "status-key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode = %o, want 600", perm)
	}

	second, err := LoadOrCreateStatusKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("a rotated key would invalidate every installed hook")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./daemon/ -run TestStatus -v`
Expected: FAIL — `undefined: StatusToken`

- [ ] **Step 3: Write the implementation**

Create `daemon/statuskey.go`:

```go
package daemon

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// statusKeyName is the file under the config directory holding the key that
// derives per-target status tokens.
const statusKeyName = "status-key"

// StatusToken derives the token a hook must present to write a target's
// status. Deriving rather than storing keeps the daemon stateless: nothing is
// recorded per run.
//
// Say plainly what this does and does not do. Any process running as you can
// read the key, so it is not a defence against local code. It stops a request
// from another user on the machine and means the endpoint cannot be driven by
// accident. The browser case is handled a layer up by guard.
func StatusToken(key []byte, target string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(target))
	return hex.EncodeToString(mac.Sum(nil))
}

// LoadOrCreateStatusKey reads the key from dir, creating it on first use.
func LoadOrCreateStatusKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, statusKeyName)

	key, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(key) != 32 {
			return nil, fmt.Errorf("status key %s: want 32 bytes, got %d", path, len(key))
		}
		return key, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read status key: %w", err)
	}

	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate status key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write status key: %w", err)
	}
	return key, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./daemon/ -run TestStatus -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/statuskey.go daemon/statuskey_test.go
git commit -m "Derive a status token instead of storing one"
```

---

### Task 5: Engine event tables and the status endpoint

**Files:**
- Create: `daemon/hooks.go`, `daemon/hooks_test.go`
- Modify: `daemon/mcp.go:82-87` (`Handler`), `daemon/mcp.go:38-46` (`Server`)

**Interfaces:**
- Consumes: `StatusToken` (Task 4), `sources.SetStatusArgs` / `sources.AgentStatus` (Tasks 1-2), `guard` (`daemon/mcp.go`)
- Produces:
  - `func stateFor(engine, event string) (sources.AgentStatus, bool)`
  - `Server.StatusKey []byte`, `Server.Runner sources.Runner`
  - route `POST /hooks/status`

The four shared event names mean the same thing in both engines and the two divergent names are disjoint, so a merged lookup would work today. The table is per-engine anyway: payloads differ in shape, and "this event means the agent is blocked" is a claim about one engine at one version, better written down as such.

- [ ] **Step 1: Write the failing tests**

Create `daemon/hooks_test.go`:

```go
package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/sources"
)

func TestEventTablesMapBothEngines(t *testing.T) {
	cases := []struct {
		engine, event string
		want          sources.AgentStatus
	}{
		{"claude", "UserPromptSubmit", sources.AgentStatusWorking},
		{"claude", "PreToolUse", sources.AgentStatusWorking},
		{"claude", "Notification", sources.AgentStatusNeedsInput},
		{"claude", "Stop", sources.AgentStatusIdle},
		{"codex", "UserPromptSubmit", sources.AgentStatusWorking},
		{"codex", "PermissionRequest", sources.AgentStatusNeedsInput},
		{"codex", "Stop", sources.AgentStatusIdle},
	}
	for _, c := range cases {
		got, ok := stateFor(c.engine, c.event)
		if !ok || got != c.want {
			t.Errorf("%s/%s = %v (%v), want %v", c.engine, c.event, got, ok, c.want)
		}
	}
}

func TestUnknownEventInventsNoState(t *testing.T) {
	if _, ok := stateFor("claude", "PermissionRequest"); ok {
		t.Error("a Codex event name must not resolve under Claude's table")
	}
	if _, ok := stateFor("claude", "Banana"); ok {
		t.Error("an unrecognised event must set no state")
	}
	if _, ok := stateFor("nonsuch", "Stop"); ok {
		t.Error("an unknown engine must set no state")
	}
}

// statusRunner records the tmux argv the endpoint produces.
type statusRunner struct{ calls [][]string }

func (r *statusRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func statusServer(t *testing.T) (*Server, *statusRunner) {
	t.Helper()
	r := &statusRunner{}
	s := NewServer(&stubTools{}, "1")
	s.StatusKey = []byte("0123456789abcdef0123456789abcdef")
	s.Runner = r
	return s, r
}

func postStatus(t *testing.T, s *Server, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/status", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("x-cockpit-status-token", token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestStatusEndpointWritesTheReportedState(t *testing.T) {
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token,
		`{"engine":"codex","hook_event_name":"PermissionRequest","target":"app:dev"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if len(r.calls) != 1 {
		t.Fatalf("want one tmux call, got %v", r.calls)
	}
	joined := strings.Join(r.calls[0], " ")
	if !strings.Contains(joined, "needs_input") || !strings.Contains(joined, "app") {
		t.Errorf("argv did not record the state: %v", r.calls[0])
	}
}

func TestStatusEndpointRejectsABadToken(t *testing.T) {
	s, r := statusServer(t)

	rec := postStatus(t, s, "deadbeef",
		`{"engine":"claude","hook_event_name":"Stop","target":"app:dev"}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if len(r.calls) != 0 {
		t.Errorf("tmux was written anyway: %v", r.calls)
	}
}

func TestStatusEndpointIgnoresAnUnknownEvent(t *testing.T) {
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token,
		`{"engine":"claude","hook_event_name":"PreCompact","target":"app:dev"}`)

	if rec.Code != http.StatusNoContent {
		t.Errorf("an ignorable event is not an error, status = %d", rec.Code)
	}
	if len(r.calls) != 0 {
		t.Errorf("no state should have been written: %v", r.calls)
	}
}

func TestStatusEndpointDropsUnknownFields(t *testing.T) {
	// Claude's Stop event can carry an entire assistant message, and none of it
	// belongs in a tmux option.
	s, r := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	rec := postStatus(t, s, token, `{"engine":"claude","hook_event_name":"Stop",`+
		`"target":"app:dev","transcript":"`+strings.Repeat("x", 5000)+`"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(strings.Join(r.calls[0], " "), "xxxx") {
		t.Error("an unknown field reached the tmux option")
	}
}

func TestStatusEndpointRefusesABrowser(t *testing.T) {
	s, _ := statusServer(t)
	token := StatusToken(s.StatusKey, "app:dev")

	req := httptest.NewRequest(http.MethodPost, "/hooks/status",
		strings.NewReader(`{"engine":"claude","hook_event_name":"Stop","target":"app:dev"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-cockpit-status-token", token)
	req.Header.Set("origin", "https://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a write route must not skip the guard, status = %d", rec.Code)
	}
}
```

Add `"context"` to the imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./daemon/ -run 'TestEventTables|TestStatusEndpoint|TestUnknownEvent' -v`
Expected: FAIL — `undefined: stateFor`, `s.StatusKey undefined`

- [ ] **Step 3: Write the implementation**

Create `daemon/hooks.go`:

```go
package daemon

import (
	"encoding/json"
	"crypto/hmac"
	"net/http"
	"strings"
	"time"

	"github.com/jhoot/cockpit/sources"
)

// maxStatusBytes bounds a hook payload. Claude's Stop event can carry an
// entire assistant message; intake is bounded so a large private reply cannot
// crowd out the metadata we actually want.
const maxStatusBytes = 16 << 10

// eventTables maps each engine's hook events onto a state.
//
// The four shared names mean the same thing in both engines and the two
// divergent names are disjoint, so one merged lookup would work today. Keeping
// them separate records whose behaviour each claim describes, and makes a
// future engine a table entry rather than a new code path.
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

// stateFor resolves an engine's event to a state. An event in no table sets
// nothing rather than falling through to a guess.
func stateFor(engine, event string) (sources.AgentStatus, bool) {
	table, ok := eventTables[engine]
	if !ok {
		return sources.AgentStatusUnknown, false
	}
	st, ok := table[event]
	return st, ok
}

// statusPayload is the allowlist. Every field is capped and anything absent
// from this struct is dropped rather than forwarded.
type statusPayload struct {
	Engine string `json:"engine"`
	Event  string `json:"hook_event_name"`
	Target string `json:"target"`
}

const (
	maxEngineLen = 16
	maxEventLen  = 32
	maxTargetLen = 128
)

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("mcp-session-id", s.SessionID)
	if !guard(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
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
	if target == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	want := StatusToken(s.StatusKey, target)
	got := r.Header.Get("x-cockpit-status-token")
	if !hmac.Equal([]byte(want), []byte(got)) {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}

	st, ok := stateFor(engine, event)
	if !ok {
		// Not an error: most events in both engines are ones we ignore.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	session, window, _ := strings.Cut(target, ":")
	if _, err := s.Runner.Run(r.Context(),
		sources.SetStatusArgs(session, st, window, time.Now())...); err != nil {
		http.Error(w, "could not record status", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func clip(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
```

- [ ] **Step 4: Wire the route and the server fields**

In `daemon/mcp.go`, add to the `Server` struct:

```go
	// StatusKey derives per-target hook tokens. Runner writes status into tmux.
	StatusKey []byte
	Runner    sources.Runner
```

Add `"github.com/jhoot/cockpit/sources"` to the imports, and register the route in `Handler`:

```go
	mux.HandleFunc("/hooks/status", s.serveStatus)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./daemon/ -v 2>&1 | tail -25`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add daemon/hooks.go daemon/hooks_test.go daemon/mcp.go
git commit -m "Accept hook status events on the daemon"
```

---

### Task 6: Construct the daemon with a key and a runner

**Files:**
- Modify: `daemon/lifecycle.go:93-100` (`Serve`), `daemon/lifecycle.go:139` and the caller in `cmd/daemon.go`

**Interfaces:**
- Consumes: `LoadOrCreateStatusKey` (Task 4), `Server.StatusKey` / `Server.Runner` (Task 5)
- Produces: a daemon whose status endpoint is usable

- [ ] **Step 1: Write the failing test**

Add to `daemon/lifecycle_test.go`:

```go
func TestServeGivesTheServerItsStatusDependencies(t *testing.T) {
	// Without these the status route answers 403 for every request, because
	// the derived token is computed against a nil key.
	srv := NewServer(&stubTools{}, "1")
	if srv.StatusKey != nil {
		t.Skip("constructed elsewhere")
	}
	s := newServerWithStatus(&stubTools{}, "1", []byte("0123456789abcdef0123456789abcdef"), &statusRunner{})
	if s.StatusKey == nil || s.Runner == nil {
		t.Fatal("newServerWithStatus must supply both")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./daemon/ -run TestServeGives`
Expected: FAIL — `undefined: newServerWithStatus`

- [ ] **Step 3: Add the constructor and thread it through**

In `daemon/mcp.go`, beside `NewServer`:

```go
// newServerWithStatus builds a server that can also record hook status.
func newServerWithStatus(tools ToolHandler, version string, key []byte, r sources.Runner) *Server {
	s := NewServer(tools, version)
	s.StatusKey = key
	s.Runner = r
	return s
}
```

In `daemon/lifecycle.go`, change `Serve` to accept them and use the new constructor:

```go
func Serve(ctx context.Context, ln net.Listener, tools ToolHandler, version string, key []byte, r sources.Runner) error {
	srv := &http.Server{Handler: newServerWithStatus(tools, version, key, r).Handler()}
```

Update the call site in `cmd/daemon.go` to load the key from the config directory and pass the same `sources.Runner` the tools already use. If the key cannot be created, log it and pass `nil` — the tool server must still start, and the status route will simply refuse every request.

- [ ] **Step 4: Verify**

Run: `go build ./... && go test ./daemon/ ./cmd/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/mcp.go daemon/lifecycle.go daemon/lifecycle_test.go cmd/daemon.go
git commit -m "Give the daemon its status key and runner"
```

---

### Task 7: `cockpit hook status`

The piece that must never break an agent.

**Files:**
- Create: `cmd/hook.go`, `cmd/hook_test.go`
- Modify: `cmd/root.go:56-61` (`init`)

**Interfaces:**
- Consumes: `daemon.StatusToken`, `daemon.LoadOrCreateStatusKey`, `config`
- Produces: `cockpit hook status --engine <claude|codex>`, and `resolveTarget(runner) string`

- [ ] **Step 1: Write the failing tests**

Create `cmd/hook_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestHookStatusExitsZeroWithNoDaemon(t *testing.T) {
	// A hook that fails, fails the agent that called it. This is the single
	// most important property in the design.
	err := runHookStatus(strings.NewReader(`{"hook_event_name":"Stop"}`), "claude", "app:dev", 0)
	if err != nil {
		t.Fatalf("a down daemon must not surface as an error: %v", err)
	}
}

func TestHookStatusExitsZeroOnMalformedInput(t *testing.T) {
	for _, in := range []string{"", "{not json", "null", "[]"} {
		if err := runHookStatus(strings.NewReader(in), "claude", "app:dev", 0); err != nil {
			t.Errorf("input %q produced an error: %v", in, err)
		}
	}
}

func TestHookStatusExitsZeroWithNoTarget(t *testing.T) {
	if err := runHookStatus(bytes.NewReader(nil), "claude", "", 0); err != nil {
		t.Errorf("no resolvable target must be a silent no-op: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run TestHookStatus -v`
Expected: FAIL — `undefined: runHookStatus`

- [ ] **Step 3: Write the implementation**

Create `cmd/hook.go`:

```go
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/daemon"
	"github.com/spf13/cobra"
)

// hookTimeout bounds the whole round trip. The agent is blocked while this
// runs.
const hookTimeout = 500 * time.Millisecond

var hookEngine string

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Report agent lifecycle events to cockpit",
	Hidden: true,
}

var hookStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Record an agent status event read from stdin",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Exit 0 unconditionally. Every failure below is swallowed on purpose.
		port := 0
		if cfg, err := config.Load(getConfigPath()); err == nil {
			port = cfg.Daemon.Port
		}
		return runHookStatus(os.Stdin, hookEngine, resolveTarget(), port)
	},
}

// resolveTarget finds the tmux target this hook is running inside.
//
// Prefer the environment cockpit injects into processes it launched, then ask
// tmux directly — which covers every session started by hand, and that is most
// of them and most of the value.
func resolveTarget() string {
	if t := os.Getenv("COCKPIT_STATUS_TARGET"); t != "" {
		return t
	}

	tmux, err := sourcesLookTmux()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, tmux, "display-message", "-p",
		"#{session_name}:#{window_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runHookStatus posts one event and never reports failure.
func runHookStatus(stdin io.Reader, engine, target string, port int) error {
	if target == "" || port == 0 {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return nil
	}
	var event struct {
		Event string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &event); err != nil || event.Event == "" {
		return nil
	}

	key, err := daemon.LoadOrCreateStatusKey(config.Dir())
	if err != nil {
		return nil
	}

	body, err := json.Marshal(map[string]string{
		"engine":          engine,
		"hook_event_name": event.Event,
		"target":          target,
	})
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/hooks/status", port), bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-cockpit-status-token", daemon.StatusToken(key, target))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	_ = resp.Body.Close()
	return nil
}
```

Add a small helper beside it that resolves tmux by absolute path — the hook inherits whatever environment the agent hands it, and a launch agent's `PATH` excludes Homebrew:

```go
// sourcesLookTmux resolves tmux absolutely. PATH is not trustworthy here.
func sourcesLookTmux() (string, error) {
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("tmux")
}
```

- [ ] **Step 4: Register the commands**

In `cmd/root.go`'s `init()`:

```go
	hookStatusCmd.Flags().StringVar(&hookEngine, "engine", "claude", "reporting engine: claude or codex")
	hookCmd.AddCommand(hookStatusCmd)
	rootCmd.AddCommand(hookCmd)
```

If `config.Dir()` does not exist, add it to `config/config.go` returning `~/.config/cockpit`, refactoring `DefaultConfigPath` to use it.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/ -v 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 6: Verify the exit code by hand**

```bash
go build -o /tmp/cockpit-hook . && echo '{"hook_event_name":"Stop"}' | /tmp/cockpit-hook hook status; echo "exit=$?"
```
Expected: `exit=0`

- [ ] **Step 7: Commit**

```bash
git add cmd/hook.go cmd/hook_test.go cmd/root.go config/config.go
git commit -m "Add the hook status subcommand"
```

---

### Task 8: `cockpit hook install`

**Files:**
- Create: `cmd/hook_install.go`, `cmd/hook_install_test.go`

**Interfaces:**
- Consumes: `hookCmd` (Task 7)
- Produces: `cockpit hook install`, `func installClaudeHooks(path, bin string) error`, `func installCodexHooks(path, bin string) error`

Writing the config is not the same as installing the hook. A Codex hook lands untrusted and does not fire until trusted, and trust pins to a config hash — so the command's last act is to read back what it wrote and say whether the hook is live.

- [ ] **Step 1: Write the failing tests**

Create `cmd/hook_install_test.go`:

```go
package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallClaudePreservesExistingHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"scry hook pre-search"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := installClaudeHooks(path, "/usr/local/bin/cockpit"); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "scry hook pre-search") {
		t.Error("an unrelated hook was destroyed")
	}
	if !strings.Contains(string(raw), "cockpit hook status") {
		t.Error("the cockpit hook was not written")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("settings are no longer valid JSON: %v", err)
	}
}

func TestInstallClaudeIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{}`), 0o600)

	installClaudeHooks(path, "/usr/local/bin/cockpit")
	installClaudeHooks(path, "/usr/local/bin/cockpit")

	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "cockpit hook status"); n != 4 {
		t.Errorf("want one entry per mapped event, got %d occurrences", n)
	}
}

func TestInstallWritesABackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"a":1}`), 0o600)

	installClaudeHooks(path, "/usr/local/bin/cockpit")

	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("no backup written: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run TestInstall -v`
Expected: FAIL — `undefined: installClaudeHooks`

- [ ] **Step 3: Write the implementation**

Create `cmd/hook_install.go` with `installClaudeHooks` merging into the `hooks` object for `UserPromptSubmit`, `PreToolUse`, `Notification` and `Stop` — reading the file as `map[string]any`, backing it up to `path + ".bak"` first, appending only when no entry's `command` already contains `cockpit hook status`, and writing with mode `0600`.

`installCodexHooks` does the same for `~/.codex/config.toml`, writing matcher groups for `UserPromptSubmit`, `PreToolUse`, `PermissionRequest` and `Stop` with `command = "<bin> hook status --engine codex"`, then runs `codex --help` style verification: read the file back and report whether the entries are present. Print the trust caveat verbatim:

```
Codex hooks land untrusted and do not fire until trusted. Trust is pinned to
a hash of the hook configuration, so editing the command untrusts it again.
Run `codex` and approve the cockpit hook, or the status will stay inferred.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestInstall -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/hook_install.go cmd/hook_install_test.go
git commit -m "Install the status hooks into both engines"
```

---

### Task 9: Show reported status, and stop capturing panes that report

**Files:**
- Modify: `tui/app.go:1272-1286` (`fetchSessionStatuses`), `tui/app.go:308` (`sessionStatusMsg`)
- Modify: `tui/grid.go:201-222` (`renderTile`), `tui/sessions.go:88-131`
- Modify: `tui/styles.go` (a dimmed status helper)
- Modify: `tui/grid_test.go`

**Interfaces:**
- Consumes: `TmuxSession.Status` / `TmuxSession.StatusReported` (Task 3)
- Produces: `func StatusDotDim(label string, v Variant) string`

- [ ] **Step 1: Write the failing tests**

Add to `tui/grid_test.go`:

```go
func TestRenderTileShowsNeedsInput(t *testing.T) {
	s := sess("app")
	s.Status, s.StatusReported = sources.AgentStatusNeedsInput, true
	out := renderTile(Target{Label: "app", Session: &s, Status: sources.AgentStatusNeedsInput}, 22, false)

	if !strings.Contains(out, "needs you") {
		t.Errorf("a blocked agent must say so:\n%s", out)
	}
}

func TestRenderTileKeepsExistenceOnShape(t *testing.T) {
	// Shape carries existence; confidence is carried by dimming. A reported
	// status must not change the marker glyph.
	s := sess("app")
	s.Status, s.StatusReported = sources.AgentStatusWorking, true
	out := renderTile(Target{Label: "app", Session: &s, Status: sources.AgentStatusWorking}, 22, false)

	if !strings.Contains(out, "●") || strings.Contains(out, "○") {
		t.Errorf("a live session keeps the filled marker:\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tui/ -run TestRenderTile -v`
Expected: FAIL — no "needs you" in output

- [ ] **Step 3: Render the new state**

In `tui/styles.go`:

```go
// StatusDotDim renders a status whose value was inferred rather than reported.
// Dimming costs no width and degrades to "looks the same" on a terminal
// without styling, where a third glyph would mean the wrong thing.
func StatusDotDim(label string, v Variant) string {
	style := lipgloss.NewStyle().Foreground(variantColor(v))
	return style.Render("●") + " " + MutedText.Render(label)
}
```

In `tui/grid.go` `renderTile`, extend the status switch:

```go
		case sources.AgentStatusNeedsInput:
			status = StatusDot(Truncate("needs you", inner-2), VariantWarning)
```

and choose the dot function by `t.Session.StatusReported`: reported uses `StatusDot`, inferred uses `StatusDotDim`.

- [ ] **Step 4: Drop the capture-pane loop for reporting sessions**

In `tui/app.go` `fetchSessionStatuses`, skip any session whose `StatusReported` is true:

```go
		for _, s := range sessions {
			// A session that reports needs no guess, and this is the most
			// expensive poll cockpit runs — one subprocess per session, per tick.
			if s.StatusReported {
				continue
			}
			content, err := sources.CapturePaneContent(ctx, s.Name)
			...
```

- [ ] **Step 5: Run the tui suite**

Run: `go test ./tui/ -v 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add tui/
git commit -m "Show a reported status and skip its capture"
```

---

### Task 10: `needs_input` at the top of Signals

**Files:**
- Modify: `sources/signals.go`, `sources/signals_test.go`

**Interfaces:**
- Consumes: `TmuxSession.Status` / `StatusReported` (Task 3)
- Produces: a signal for every session reporting `needs_input`

- [ ] **Step 1: Write the failing test**

Add to `sources/signals_test.go`:

```go
func TestBlockedAgentOutranksEveryOtherSignal(t *testing.T) {
	in := SignalInput{
		Config:   config.SignalsConfig{ShowUnpushed: true},
		Sessions: []TmuxSession{{Name: "app", Status: AgentStatusNeedsInput, StatusReported: true}},
		Git:      []GitRepoStatus{{Label: "other", Unpushed: 3}},
		Now:      time.Now(),
	}

	got := ComputeSignals(in)

	if len(got) == 0 {
		t.Fatal("a blocked agent produced no signal")
	}
	if got[0].Subject != "app" {
		t.Errorf("a blocked agent must sort above an unpushed commit, got %+v", got[0])
	}
}
```

`ComputeSignals(in SignalInput) []Signal` — the input is a struct
(`sources/signals.go:34`), and a `Signal` is `{Kind, Subject, Detail}`. Add a
`SignalKind` constant for the new category alongside the existing ones.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./sources/ -run TestBlockedAgent -v`
Expected: FAIL

- [ ] **Step 3: Emit the signal first**

Add `blockedAgentSignals(in)` as the **first** entry in `ComputeSignals`, ahead of `deadProcessSignals`, emitting one signal per session with `StatusReported && Status == AgentStatusNeedsInput`:

```go
func blockedAgentSignals(in SignalInput) []Signal {
	var out []Signal
	for _, s := range in.Sessions {
		// Only a reported status counts. The pane-hash guess cannot see this
		// state at all, so an inferred one is never needs_input.
		if !s.StatusReported || s.Status != AgentStatusNeedsInput {
			continue
		}
		out = append(out, Signal{
			Kind:    SignalBlockedAgent,
			Subject: s.Name,
			Detail:  "waiting on you",
		})
	}
	return out
}
```

Update the `ComputeSignals` doc comment, which currently states the ordering as "a dead process beats failing continuous integration" — a waiting agent now heads that list. It clears the moment you answer, and that churn is the accepted price for the one signal you can act on immediately.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./sources/ -v 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add sources/signals.go sources/signals_test.go
git commit -m "Put a waiting agent at the top of signals"
```

---

### Task 11: Document it and verify end to end

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Install the hooks for real**

```bash
go build -o ~/.local/bin/cockpit . && cockpit hook install
```
Expected: reports both engines, and names the Codex trust step

- [ ] **Step 2: Verify a real session moves through the states**

Start a Claude Code session in a tmux session, then watch:

```bash
watch -n1 "tmux list-sessions -F '#{session_name}|#{@cockpit_status}|#{@cockpit_status_at}'"
```

Expected: `working` on a prompt, `needs_input` on a permission prompt, `idle` when the turn ends. Repeat with `codex` after approving its hook trust.

- [ ] **Step 3: Confirm the capture-pane loop is gone for those sessions**

```bash
sudo fs_usage -w -f exec 2>/dev/null | grep -c capture-pane &
sleep 10; kill %1
```
Expected: markedly fewer than before — a reporting session issues none.

- [ ] **Step 4: Document the feature**

Add a `## Agent status` section to `README.md` covering `cockpit hook install`, the three states, the Codex trust step, and the fact that a session without hooks falls back to a dimmed, inferred status.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "Document hook driven status"
```

---

## Self-Review

**Spec coverage.** §2 event tables → Task 5. §2 install and Codex trust → Task 8. §3 tmux store, staleness, window inheritance → Tasks 2-3. §4.1 hook subcommand and its three rules → Task 7. §4.2 endpoint, allowlist, caps, guard → Task 5. §4.3 derived token → Task 4. §4.4 TUI, marker scheme, `AgentStatus` rename → Tasks 1 and 9. §5 degradation → the staleness test in Task 2, the unknown-event test in Task 5, the fallback in Task 9. §6 testing → distributed across tasks, plus Task 11 end to end. §8.4 signal → Task 10.

**Known gaps, deliberate.** Per-window status reads (§3's inheritance gotcha) are implemented in `StatusFromOptions` and covered by tests, but nothing consumes them yet — the grid is session-level. Remote hosts are the next project and out of scope here (§9).

**Type consistency.** `AgentStatus` and its four constants are defined in Task 1 and used unchanged in 2, 3, 5, 9, 10. `StatusToken(key, target)` is defined in Task 4 and called in 5 and 7. `SetStatusArgs(session, status, window, now)` is defined in Task 2 and called in Task 5. `guard` is pre-existing in `daemon/mcp.go` and reused in Task 5.
