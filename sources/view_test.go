package sources

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/config"
)

var (
	miniHost = config.HostConfig{Name: "mini", Tmux: "/opt/homebrew/bin/tmux"}
	miniRepo = config.RepoConfig{Host: "mini", Path: "~/workspace/docket", Label: "docket"}
)

func TestViewAttachCommandIsQuoted(t *testing.T) {
	got := ViewAttachCommand(miniHost, "docket")
	want := "ssh -t -- 'mini' '/opt/homebrew/bin/tmux' 'new-session' '-A' '-s' 'docket'"
	if got != want {
		t.Errorf("attach command = %s\nwant %s", got, want)
	}
}

func TestViewSessionArgsMarksTheSessionAsAView(t *testing.T) {
	args := ViewSessionArgs(miniHost, "docket")
	joined := strings.Join(args, " ")
	if args[0] != "new-session" || !slices.Contains(args, "-s") || !slices.Contains(args, "mini") {
		t.Errorf("must create the host session: %v", args)
	}
	if !strings.Contains(joined, "@cockpit_view_of mini") {
		t.Errorf("a view session must be marked so the grid skips it: %v", args)
	}
	if !strings.Contains(joined, "-n docket") {
		t.Errorf("the first window is the project, not a stray shell: %v", args)
	}
}

func TestJumpRemoteCreatesTheRemoteSessionBeforeAnyLocalWindow(t *testing.T) {
	remote := &fakeRunner{errs: map[string]error{"has-session": errors.New("no such session")}}
	local := &fakeRunner{errs: map[string]error{"has-session": errors.New("no such session")}}

	if err := JumpRemote(context.Background(), local, remote, miniHost, miniRepo); err != nil {
		t.Fatal(err)
	}

	if len(remote.called("new-session")) != 1 {
		t.Errorf("remote session must be created, remote calls: %v", remote.calls)
	}
	if len(local.called("new-session")) != 1 {
		t.Errorf("local view session must be created, local calls: %v", local.calls)
	}
	last := local.calls[len(local.calls)-1]
	prev := local.calls[len(local.calls)-2]
	if prev[0] != "switch-client" || last[0] != "select-window" || !slices.Contains(last, "mini:docket") {
		t.Errorf("must end by switching to the view and selecting the window, got %v then %v", prev, last)
	}
}

func TestJumpRemoteAddsAWindowToAnExistingView(t *testing.T) {
	remote := &fakeRunner{}
	local := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|deepresearch|0|111|1\n",
	}}

	if err := JumpRemote(context.Background(), local, remote, miniHost, miniRepo); err != nil {
		t.Fatal(err)
	}

	if len(local.called("new-session")) != 0 {
		t.Error("the view session already exists and must not be recreated")
	}
	created := local.called("new-window")
	if len(created) != 1 || !slices.Contains(created[0], "docket") {
		t.Errorf("want one new docket window, got %v", created)
	}
}

func TestJumpRemoteRespawnsADeadViewWindow(t *testing.T) {
	// ssh exited — the link dropped — and the window holds a dead pane.
	remote := &fakeRunner{}
	local := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|docket|1|111|1\n",
	}}

	if err := JumpRemote(context.Background(), local, remote, miniHost, miniRepo); err != nil {
		t.Fatal(err)
	}

	if len(local.called("respawn-window")) != 1 || len(local.called("new-window")) != 0 {
		t.Errorf("a dead view window is respawned in place, not duplicated: %v", local.calls)
	}
}

func TestJumpRemoteLeavesALiveViewWindowAlone(t *testing.T) {
	remote := &fakeRunner{}
	local := &fakeRunner{outputs: map[string]string{
		"list-windows": "0|docket|0|111|1\n",
	}}

	if err := JumpRemote(context.Background(), local, remote, miniHost, miniRepo); err != nil {
		t.Fatal(err)
	}

	for _, verb := range []string{"new-session", "new-window", "respawn-window"} {
		if len(local.called(verb)) != 0 {
			t.Errorf("%s ran against a live view window: %v", verb, local.calls)
		}
	}
}

func TestJumpRemoteStopsWhenTheHostIsUnreachable(t *testing.T) {
	remote := &fakeRunner{errs: map[string]error{"has-session": ErrHostUnreachable, "new-session": ErrHostUnreachable}}
	local := &fakeRunner{}

	err := JumpRemote(context.Background(), local, remote, miniHost, miniRepo)

	if !errors.Is(err, ErrHostUnreachable) {
		t.Fatalf("want the transport error surfaced, got %v", err)
	}
	if len(local.calls) != 0 {
		t.Errorf("no local window should open onto a host that cannot be reached: %v", local.calls)
	}
}
