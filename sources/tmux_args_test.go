package sources

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/config"
)

func TestNewSessionArgs(t *testing.T) {
	got := NewSessionArgs("my-app", "/tmp/my-app")
	want := []string{"new-session", "-d", "-s", "my-app", "-c", "/tmp/my-app"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestNewWindowArgs(t *testing.T) {
	p := config.ProcessConfig{
		Name:       "dev",
		Command:    "npm run dev",
		WorkingDir: "web",
		Env:        map[string]string{"PORT": "3000"},
	}
	got := NewWindowArgs("my-app", p, "/tmp/my-app")
	want := []string{
		"new-window", "-d", "-t", "my-app:", "-n", "dev",
		"-c", "/tmp/my-app/web", "-e", "PORT=3000", "npm run dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestNewWindowArgsWithoutEnvOrWorkingDir(t *testing.T) {
	p := config.ProcessConfig{Name: "dev", Command: "npm run dev"}
	got := NewWindowArgs("my-app", p, "/tmp/my-app")
	want := []string{
		"new-window", "-d", "-t", "my-app:", "-n", "dev",
		"-c", "/tmp/my-app", "npm run dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestNewWindowArgsEnvSorted(t *testing.T) {
	p := config.ProcessConfig{Name: "dev", Command: "x", Env: map[string]string{"B": "2", "A": "1"}}
	joined := strings.Join(NewWindowArgs("s", p, "/r"), " ")
	if !strings.Contains(joined, "-e A=1 -e B=2") {
		t.Errorf("env vars must be sorted for determinism: %q", joined)
	}
}

func TestRespawnWindowArgs(t *testing.T) {
	p := config.ProcessConfig{Name: "dev", Command: "npm run dev", Env: map[string]string{"PORT": "3000"}}
	got := RespawnWindowArgs("my-app", "dev", p, "/tmp/my-app")
	want := []string{
		"respawn-window", "-k", "-t", "my-app:dev",
		"-c", "/tmp/my-app", "-e", "PORT=3000", "npm run dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestKillWindowArgs(t *testing.T) {
	got := KillWindowArgs("my-app", "dev")
	want := []string{"kill-window", "-t", "my-app:dev"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestRemainOnExitArgs(t *testing.T) {
	got := RemainOnExitArgs("my-app", "dev")
	want := []string{"set-window-option", "-t", "my-app:dev", "remain-on-exit", "on"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSelectWindowArgs(t *testing.T) {
	got := SelectWindowArgs("my-app", 0)
	want := []string{"select-window", "-t", "my-app:0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCapturePaneArgs(t *testing.T) {
	got := CapturePaneArgs("my-app:dev", 200)
	want := []string{"capture-pane", "-p", "-t", "my-app:dev", "-S", "-200"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCapturePaneArgsWithoutLineLimit(t *testing.T) {
	got := CapturePaneArgs("my-app:dev", 0)
	want := []string{"capture-pane", "-p", "-t", "my-app:dev"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a zero line count should capture the visible pane: got %q", got)
	}
}

func TestSendKeysArgs(t *testing.T) {
	if got, want := SendKeysLiteralArgs("s:dev", "hello"), []string{"send-keys", "-t", "s:dev", "-l", "hello"}; !reflect.DeepEqual(got, want) {
		t.Errorf("literal: got %q want %q", got, want)
	}
	if got, want := SendKeysEnterArgs("s:dev"), []string{"send-keys", "-t", "s:dev", "Enter"}; !reflect.DeepEqual(got, want) {
		t.Errorf("enter: got %q want %q", got, want)
	}
}

func TestTarget(t *testing.T) {
	if got := Target("my-app", "dev"); got != "my-app:dev" {
		t.Errorf("got %q", got)
	}
}

func TestParseWindows(t *testing.T) {
	out := "0\tshell\t0\t111\t1\n1\tdev\t1\t222\t0\n"
	got := ParseWindows(out)
	if len(got) != 2 {
		t.Fatalf("want 2 windows, got %d", len(got))
	}
	if got[0].Index != 0 || got[0].Name != "shell" || got[0].Dead || !got[0].Active {
		t.Errorf("window 0 parsed wrong: %+v", got[0])
	}
	if got[1].Name != "dev" || !got[1].Dead || got[1].PanePID != 222 || got[1].Active {
		t.Errorf("window 1 parsed wrong: %+v", got[1])
	}
}

func TestParseWindowsEmpty(t *testing.T) {
	if got := ParseWindows("   \n"); got != nil {
		t.Errorf("empty output should parse to nil, got %+v", got)
	}
}

func TestParseWindowsSkipsMalformedLines(t *testing.T) {
	out := "garbage\n1\tdev\t0\t222\t0\n"
	got := ParseWindows(out)
	if len(got) != 1 || got[0].Name != "dev" {
		t.Errorf("malformed lines should be skipped, got %+v", got)
	}
}
