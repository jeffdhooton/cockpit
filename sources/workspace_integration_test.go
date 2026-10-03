package sources

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationWorkspaceClassifiesARealAgentCommand runs a command named
// claude on a private server and checks the pane is an agent with unknown
// status, and that its visible screen previews.
func TestIntegrationWorkspaceClassifiesARealAgentCommand(t *testing.T) {
	r := requireTmux(t)
	ctx := context.Background()
	dir := t.TempDir()
	// A real binary named claude, built from testdata: tmux reports the
	// foreground process's own name, so a symlink or a script would read
	// as sleep or sh.
	fake := filepath.Join(dir, "claude")
	build := exec.Command("go", "build", "-o", fake, "./testdata/fakeagent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the fake agent: %v %s", err, out)
	}
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "work", "-n", "agent", "-c", dir, "echo agent-screen; "+fake+" 60"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	rep := ObserveHostReport(ctx, r, nil, "", nil, time.Now(), time.Now())
	ws := BuildWorkspace(AttentionInput{Hosts: []HostReport{rep}, Now: time.Now()})
	if len(ws.Hosts[0].Sessions) != 1 || len(ws.Hosts[0].Sessions[0].Panes) != 1 {
		t.Fatalf("ws = %+v", ws)
	}
	p := ws.Hosts[0].Sessions[0].Panes[0]
	if p.Kind != PaneAgent || p.Agent == nil || p.Agent.Engine != "claude" || p.Agent.Status != "unknown" || p.Agent.Reported {
		t.Errorf("pane = %+v", p)
	}
	if p.Path == "" || p.Command != "claude" {
		t.Errorf("command and path must be observed: %+v", p)
	}
	out, err := r.Run(ctx, CapturePaneVisibleArgs(p.PaneID)...)
	if err != nil || !strings.Contains(out, "agent-screen") {
		t.Errorf("preview: %v %q", err, out)
	}
}
