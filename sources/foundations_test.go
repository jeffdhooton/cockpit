package sources

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSessionsReadsIdentityAndKeepsLegacyLines(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	out := "app|2|1|1700000000|needs_input|1700000090|dev||$3|4242|1699999000\n" +
		"legacy|1|0|1700000000||||\n"
	got, err := parseTmuxOutput(out, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %+v", got)
	}
	if got[0].ID != "$3" || got[0].Generation != "4242-1699999000" {
		t.Errorf("identity not read: %+v", got[0])
	}
	if got[0].Status != AgentStatusNeedsInput || !got[0].StatusReported || got[0].StatusSource != StatusSourceSession {
		t.Errorf("legacy session status must still be read: %+v", got[0])
	}
	if got[1].Name != "legacy" || got[1].ID != "" {
		t.Errorf("an older line must still parse without identity: %+v", got[1])
	}
}

func TestParseWindowsReadsIdentityAndUnknownExitCode(t *testing.T) {
	got := ParseWindows("1|dev|1|222|0||@1|%1|1|dev|100|200\n2|a|b|1|333|1|3|@2|%2|2||100|200\n")
	if len(got) != 2 {
		t.Fatalf("want 2 windows, got %+v", got)
	}
	if got[0].ID != "@1" || got[0].PaneID != "%1" || got[0].Managed != "dev" || got[0].Generation != "100-200" {
		t.Errorf("identity not read: %+v", got[0])
	}
	if got[0].HasDeadStatus {
		t.Errorf("an empty pane_dead_status is an unknown code, not zero: %+v", got[0])
	}
	if got[1].Name != "a|b" || got[1].Panes != 2 || !got[1].HasDeadStatus || got[1].DeadStatus != 3 {
		t.Errorf("a name containing the separator must survive: %+v", got[1])
	}
}

func TestStopOverridesRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	args := SetStopOverrideArgs("app", "dev", now)
	if !strings.Contains(strings.Join(args, " "), "@cockpit_stopped_dev v1:1700000000") {
		t.Errorf("unexpected args %v", args)
	}
	got := ParseStopOverrides("@cockpit_stopped_dev v1:1700000000\nremain-on-exit failed\n@cockpit_stopped_worker \"v9:5\"\n")
	if o, ok := got["dev"]; !ok || !o.Since.Equal(now) || o.Version != "v1" {
		t.Errorf("dev = %+v", got["dev"])
	}
	if o, ok := got["worker"]; !ok || o.Version != "v9" {
		t.Errorf("a newer version must still count as stopped: %+v", got)
	}
	if len(got) != 2 {
		t.Errorf("unrelated options leaked in: %+v", got)
	}
	clear := strings.Join(ClearStopOverrideArgs("app", "dev"), " ")
	if !strings.Contains(clear, "-u") || !strings.Contains(clear, "@cockpit_stopped_dev") {
		t.Errorf("clear args = %v", clear)
	}
}

func TestPaneReportsRollUpPerSession(t *testing.T) {
	now := time.Unix(1_700_000_600, 0)
	sessions := []TmuxSession{
		{Name: "app", ID: "$1", Status: AgentStatusIdle, StatusReported: true, StatusSource: StatusSourceSession},
		{Name: "old", ID: "$2", Status: AgentStatusWorking, StatusReported: true, StatusSource: StatusSourceSession},
		{Name: "expired", ID: "$3"},
	}
	out := "$1|@1|%1|10|0|1|claude|/w|needs_input|1700000590|inv-a|5|100|200|reviewer\n" +
		"$1|@2|%2|11|0|0|codex|/w|working|1700000595|inv-b|6|100|200|coder\n" +
		"$1|@3|%3|12|0|0|zsh|/w|||||100|200|shell\n" +
		"$3|@4|%4|13|0|1|claude|/w|working|1699990000|inv-c|1|100|200|stale\n"
	panes := ParsePaneReports(out, "mini", sessions, now)
	if len(panes) != 4 {
		t.Fatalf("want 4 panes, got %d", len(panes))
	}
	if panes[0].Session != "app" || panes[0].WindowName != "reviewer" || panes[0].Invocation != "inv-a" || panes[0].Seq != 5 {
		t.Errorf("pane = %+v", panes[0])
	}
	if panes[2].Recorded {
		t.Errorf("a pane with no record must not count as recorded: %+v", panes[2])
	}

	got := ApplyPaneReports(sessions, panes)
	if got[0].Status != AgentStatusNeedsInput || !got[0].StatusReported || got[0].StatusSource != StatusSourcePane {
		t.Errorf("app must roll up to needs_input from its panes: %+v", got[0])
	}
	if got[1].Status != AgentStatusWorking || got[1].StatusSource != StatusSourceSession {
		t.Errorf("a session with no pane record keeps its legacy report: %+v", got[1])
	}
	if got[2].StatusReported || got[2].Status != AgentStatusUnknown || got[2].StatusSource != StatusSourcePane {
		t.Errorf("an expired pane record is unknown, not healthy: %+v", got[2])
	}
}

func TestLockArgsCompareAndSet(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	args := AcquireLockArgs("app", "tok", now)
	if args[0] != "if-shell" || args[1] != "-F" {
		t.Fatalf("lock must be an if-shell -F compare-and-set: %v", args)
	}
	if !strings.Contains(args[2], "001700000000") || !strings.Contains(args[3], "001700000030") {
		t.Errorf("stamps must be zero-padded for tmux's string compare: %v", args)
	}
	rel := ReleaseLockArgs("app", "tok")
	if !strings.Contains(rel[2], "tok") || !strings.Contains(rel[3], "-gu") {
		t.Errorf("release must be conditional on the token: %v", rel)
	}
}

func TestLockOnAbsentServerIsHeldByDefinition(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{"if-shell": errors.New("error connecting to /tmp/x (No such file or directory)")}}
	l, err := AcquireProjectLock(context.Background(), f, "app", time.Now)
	if err != nil || !l.ServerAbsent {
		t.Fatalf("no server means nobody to contend with: %v %+v", err, l)
	}
	l.Release(context.Background(), f)
	if len(f.called("if-shell")) != 1 {
		t.Errorf("nothing to release on an absent server: %v", f.calls)
	}
}

func TestLockGivesUpWhenAnotherClientHoldsIt(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{"display-message": "someone-else\n"}}
	clock := time.Unix(1_700_000_000, 0)
	now := func() time.Time { clock = clock.Add(2 * time.Second); return clock }
	_, err := AcquireProjectLock(context.Background(), f, "app", now)
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("want ErrLockBusy, got %v", err)
	}
}

// TestIntegrationLockIsExclusiveAcrossClients races many acquirers against a
// real private server, the way the TUI and daemon race in practice. The
// if-shell compare-and-set must admit exactly one holder at a time.
func TestIntegrationLockIsExclusiveAcrossClients(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "lockhost"); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var mu sync.Mutex
	holders := 0
	maxHolders := 0
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 3; n++ {
				l, err := AcquireProjectLock(ctx, r, "proj", time.Now)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				mu.Lock()
				holders++
				if holders > maxHolders {
					maxHolders = holders
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				holders--
				mu.Unlock()
				l.Release(ctx, r)
			}
		}()
	}
	wg.Wait()
	if maxHolders != 1 {
		t.Errorf("lock admitted %d holders at once", maxHolders)
	}
	out, _ := r.Run(ctx, ReadLockArgs("proj")...)
	if strings.TrimSpace(out) != "" {
		t.Errorf("lock left behind: %q", out)
	}
}

func TestIntegrationExpiredLockIsTakenOver(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "lockhost"); err != nil {
		t.Fatal(err)
	}
	past := func() time.Time { return time.Now().Add(-2 * lockTTL) }
	stale, err := AcquireProjectLock(ctx, r, "proj", past)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := AcquireProjectLock(ctx, r, "proj", time.Now)
	if err != nil {
		t.Fatalf("a crashed holder's expired lock must not block forever: %v", err)
	}
	stale.Release(ctx, r)
	out, _ := r.Run(ctx, ReadLockArgs("proj")...)
	if strings.TrimSpace(out) != fresh.Token {
		t.Errorf("the stale holder released the fresh holder's lock: %q", out)
	}
	fresh.Release(ctx, r)
}

func TestIntegrationObserveHostReadsPaneRecords(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "split-window", "-d", "-t", "agents:{start}"); err != nil {
		t.Fatal(err)
	}
	obs, err := ObserveHost(ctx, r, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Panes) != 2 || obs.Generation == "" {
		t.Fatalf("observation = %+v", obs)
	}
	now := time.Now()
	if _, err := r.Run(ctx, SetPaneStatusArgs(obs.Panes[0].PaneID, AgentStatusNeedsInput, "inv-1", 7, now)...); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, SetPaneStatusArgs(obs.Panes[1].PaneID, AgentStatusIdle, "inv-2", 8, now)...); err != nil {
		t.Fatal(err)
	}
	obs, err = ObserveHost(ctx, r, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Sessions[0].Status != AgentStatusNeedsInput || obs.Sessions[0].StatusSource != StatusSourcePane {
		t.Errorf("session = %+v", obs.Sessions[0])
	}
	if !obs.Panes[0].Reported || obs.Panes[0].Invocation != "inv-1" || obs.Panes[0].Seq != 7 {
		t.Errorf("pane = %+v", obs.Panes[0])
	}
}

// TestIntegrationPaneRecordsDoNotInheritSessionStatus is the regression for
// a bug the fakes could not show: tmux resolves an unset pane option
// through the session, so a legacy session-level report must not read as a
// record on every pane.
func TestIntegrationPaneRecordsDoNotInheritSessionStatus(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, SetStatusArgs("legacy", AgentStatusNeedsInput, "agent", time.Now())...); err != nil {
		t.Fatal(err)
	}
	obs, err := ObserveHost(ctx, r, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Panes) != 1 || obs.Panes[0].Recorded {
		t.Fatalf("a session-level report must not appear as a pane record: %+v", obs.Panes)
	}
	if obs.Sessions[0].StatusSource != StatusSourceSession || obs.Sessions[0].Status != AgentStatusNeedsInput {
		t.Errorf("the session-level report must still be read as such: %+v", obs.Sessions[0])
	}
}

func TestPaneReportsCarryCommandPathAndActive(t *testing.T) {
	now := time.Unix(1_700_000_600, 0)
	sessions := []TmuxSession{{Name: "app", ID: "$1"}}
	out := "$1|@1|%1|10|0|1|claude|/Users/me/work/app|needs_input|1700000590|inv-a|5|100|200|reviewer\n" +
		"$1|@2|%2|11|0|0|zsh|/Users/me/odd\\|dir||||0|100|200|a|b\n"
	panes := ParsePaneReports(out, "", sessions, now)
	if len(panes) != 2 {
		t.Fatalf("want 2 panes, got %+v", panes)
	}
	if p := panes[0]; p.Command != "claude" || p.Path != "/Users/me/work/app" || !p.Active || p.WindowName != "reviewer" || p.Status != AgentStatusNeedsInput {
		t.Errorf("pane 0 = %+v", p)
	}
	if p := panes[1]; p.Command != "zsh" || p.Path != "/Users/me/odd|dir" || p.Active || p.WindowName != "a|b" {
		t.Errorf("pane 1 = %+v", p)
	}
}
