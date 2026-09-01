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

func TestSelectFirstWindowArgs(t *testing.T) {
	got := SelectFirstWindowArgs("my-app")
	want := []string{"select-window", "-t", "my-app:{start}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
	// Hardcoding index 0 breaks for anyone with base-index 1 set, which is a
	// very common tmux configuration.
	for _, arg := range got {
		if arg == "my-app:0" {
			t.Error("must not assume the first window is index 0")
		}
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
	out := "0|shell|0|111|1\n1|dev|1|222|0\n"
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
	out := "garbage\n1|dev|0|222|0\n"
	got := ParseWindows(out)
	if len(got) != 1 || got[0].Name != "dev" {
		t.Errorf("malformed lines should be skipped, got %+v", got)
	}
}

func TestParseWindowsReadsDeadStatus(t *testing.T) {
	// The exit status is what turns "it died" into "it could not find the
	// command", so a receipt can name the reason.
	out := "1|ghost|1|222|0|127\n"
	got := ParseWindows(out)
	if len(got) != 1 {
		t.Fatalf("want 1 window, got %d", len(got))
	}
	if !got[0].Dead || got[0].DeadStatus != 127 {
		t.Errorf("got %+v, want dead with status 127", got[0])
	}
}

func TestParseWindowsToleratesMissingDeadStatus(t *testing.T) {
	// A live window reports an empty dead status, and older tmux may omit the
	// field entirely. Neither should drop the window.
	for _, line := range []string{
		"1|dev|0|222|1|\n",
		"1|dev|0|222|1\n",
	} {
		got := ParseWindows(line)
		if len(got) != 1 || got[0].Name != "dev" || got[0].DeadStatus != 0 {
			t.Errorf("line %q parsed to %+v", line, got)
		}
	}
}

func TestFormatsAvoidTabSeparators(t *testing.T) {
	// tmux replaces a tab with "_" when the environment has no UTF-8 locale.
	// launchd supplies no locale at all, so a tab-separated format silently
	// became unparseable exactly when the daemon started at login:
	//
	//   full env: b u i l d | 1
	//   env -i:   b u i l d  _  1
	//
	// The separator must survive any locale, so it has to be printable.
	for name, format := range map[string]string{
		"windowFormat":  windowFormat,
		"sessionFormat": sessionFormat,
	} {
		if strings.Contains(format, "\t") {
			t.Errorf("%s still uses a tab: %q", name, format)
		}
	}
}

func TestParseWindowsHandlesSeparatorInWindowName(t *testing.T) {
	// A window name is free-form, so it may contain the separator. The numeric
	// fields around it are fixed, so anchor to those rather than to the count.
	got := ParseWindows("3|weird|name|0|444|1|\n")
	if len(got) != 1 {
		t.Fatalf("want 1 window, got %d", len(got))
	}
	w := got[0]
	if w.Index != 3 || w.Name != "weird|name" || w.Dead || w.PanePID != 444 || !w.Active {
		t.Errorf("got %+v, want index 3 named %q", w, "weird|name")
	}
}

func TestParseTmuxOutputHandlesSeparatorInSessionName(t *testing.T) {
	got, err := parseTmuxOutput("od|d|name|2|1|1700000000\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 session, got %d", len(got))
	}
	if got[0].Name != "od|d|name" || got[0].Windows != 2 || !got[0].Attached {
		t.Errorf("got %+v", got[0])
	}
}
