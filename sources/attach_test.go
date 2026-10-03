package sources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAttachLocalRefusesAReplacedPane(t *testing.T) {
	// The pane id exists, but on a newer server generation: a different
	// pane that happens to reuse the number.
	f := &fakeRunner{outputs: map[string]string{
		"list-panes": "$1|@1|%1|10|0|1|zsh|/w|||||900|901|agent\n",
	}}
	err := AttachLocal(context.Background(), f, AttachTarget{Generation: "100-200", SessionID: "$1", WindowID: "@1", PaneID: "%1"})
	if !errors.Is(err, ErrTargetGone) {
		t.Fatalf("want ErrTargetGone, got %v", err)
	}
	if len(f.called("switch-client")) != 0 || len(f.called("new-session")) != 0 {
		t.Errorf("nothing may be switched to or created: %v", f.calls)
	}
}

func TestAttachLocalSelectsWindowAndPaneBeforeSwitching(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list-panes": "$1|@3|%7|10|0|1|claude|/w|needs_input|1|inv|1|100|200|agent\n",
	}}
	err := AttachLocal(context.Background(), f, AttachTarget{Generation: "100-200", SessionID: "$1", WindowID: "@3", PaneID: "%7"})
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, c := range f.calls {
		verbs = append(verbs, c[0])
	}
	if strings.Join(verbs, " ") != "list-panes select-window select-pane switch-client" {
		t.Errorf("order = %v", verbs)
	}
	if got := f.called("switch-client"); !strings.Contains(strings.Join(got[0], " "), "$1") {
		t.Errorf("switch must address the session id: %v", got)
	}
}

func TestAttachRemoteNeverCreatesTheRemoteSession(t *testing.T) {
	remote := &fakeRunner{outputs: map[string]string{
		"list-panes": "$1|@1|%1|10|0|1|zsh|/w|||||100|200|agent\n",
	}}
	local := &fakeRunner{errs: map[string]error{"has-session": errors.New("can't find session: mini")}}
	err := AttachRemote(context.Background(), local, remote, miniHost,
		AttachTarget{Host: "mini", Generation: "100-200", Session: "api", SessionID: "$1", WindowID: "@1", PaneID: "%1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.called("new-session")) != 0 || len(remote.called("new-window")) != 0 {
		t.Errorf("remote creation is forbidden: %v", remote.calls)
	}
	view := local.called("new-session")
	if len(view) != 1 || !strings.Contains(strings.Join(view[0], " "), "attach-session") || strings.Contains(strings.Join(view[0], " "), "new-session' '-A") {
		t.Errorf("the view window must attach, not create: %v", view)
	}

	gone := &fakeRunner{outputs: map[string]string{"list-panes": ""}}
	err = AttachRemote(context.Background(), local, gone, miniHost, AttachTarget{Host: "mini", Session: "api", PaneID: "%1"})
	if !errors.Is(err, ErrTargetGone) {
		t.Errorf("a vanished pane must fail visibly: %v", err)
	}
}

func TestIntegrationAttachSurvivesRenameAndRefusesReplacement(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "work", "-n", "agent"); err != nil {
		t.Fatal(err)
	}
	obs, err := ObserveHost(ctx, r, "", time.Now())
	if err != nil || len(obs.Panes) != 1 {
		t.Fatalf("observe: %v %+v", err, obs)
	}
	p := obs.Panes[0]
	target := AttachTarget{Generation: p.Generation, Session: "work", SessionID: p.SessionID, WindowID: p.WindowID, PaneID: p.PaneID}

	// Rename the window and the session: identity holds.
	if _, err := r.Run(ctx, "rename-window", "-t", p.WindowID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "rename-session", "-t", p.SessionID, "moved"); err != nil {
		t.Fatal(err)
	}
	// Add a second pane and make it active externally; the target still
	// selects the reporting pane.
	if _, err := r.Run(ctx, "split-window", "-t", p.WindowID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTarget(ctx, r, target); err != nil {
		t.Fatalf("a renamed target is the same target: %v", err)
	}
	for _, args := range SelectTargetArgs(target) {
		if _, err := r.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := r.Run(ctx, "display-message", "-p", "-t", p.SessionID, "#{pane_id}")
	if strings.TrimSpace(out) != p.PaneID {
		t.Errorf("active pane = %q, want %s", out, p.PaneID)
	}

	// Kill the pane: gone. Recreate a window: a new id, still gone.
	if _, err := r.Run(ctx, "kill-pane", "-t", p.PaneID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTarget(ctx, r, target); !errors.Is(err, ErrTargetGone) {
		t.Errorf("a killed pane must be gone: %v", err)
	}
	// A server restart reuses ids under a new generation.
	if _, err := r.Run(ctx, "kill-server"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "work", "-n", "agent"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := ObserveHost(ctx, r, "", time.Now())
	same := AttachTarget{Generation: p.Generation, SessionID: fresh.Panes[0].SessionID, WindowID: fresh.Panes[0].WindowID, PaneID: fresh.Panes[0].PaneID}
	if err := ValidateTarget(ctx, r, same); !errors.Is(err, ErrTargetGone) {
		t.Errorf("a reused id on a new server is a different pane: %v", err)
	}
}
