package checks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/tui"
)

// These checks drive only keyboard input, rendered screens, CLI output and
// subprocess boundaries. No builder-owned types, fields or source filenames.
// testdata keeps this separately invoked acceptance gate out of go test ./....
func TestMain(m *testing.M) {
	if os.Getenv("SPINE_CARD_FAKE") == "1" {
		os.Exit(fakeTool(filepath.Base(os.Args[0]), os.Args[1:]))
	}
	os.Exit(m.Run())
}

type call struct {
	Tool string    `json:"tool"`
	Args []string  `json:"args"`
	At   time.Time `json:"at"`
}

func fakeTool(tool string, args []string) int {
	root := os.Getenv("SPINE_CARD_WORLD")
	// Never recreate a world that Go is already removing after UI shutdown.
	log, err := os.OpenFile(filepath.Join(root, "calls"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		panic(err)
	}
	b, _ := json.Marshal(call{tool, args, time.Now()})
	_, _ = log.Write(append(b, '\n'))
	_ = log.Close()
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(root, name)); return string(b) }
	switch tool {
	case "spine":
		if len(args) == 2 && args[0] == "bearings" && args[1] == "--json" {
			if read("mode") == "failed" {
				fmt.Fprintln(os.Stderr, "fixture permission denied")
				return 23
			}
			if read("mode") == "invalid" {
				fmt.Print("{ this is not JSON")
				return 0
			}
			fmt.Print(read("snapshot"))
			return 0
		}
		if len(args) == 1 && args[0] == "tui" {
			if read("tui-fails") == "yes" {
				return 19
			}
			return 0
		}
		if len(args) == 1 && args[0] == "bearings" {
			fmt.Println("fixture fleet console")
			return 0
		}
		fmt.Fprintln(os.Stderr, "forbidden spine operation")
		return 91
	case "fake-shell":
		c := exec.Command("/bin/sh")
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if c.Run() != nil {
			return 1
		}
		return 0
	case "sh", "bash", "zsh":
		// Login shells must not restore the user's PATH and reach real tools.
		for i, a := range args {
			if strings.Contains(a, "c") && strings.HasPrefix(a, "-") && i+1 < len(args) {
				c := exec.Command("/bin/sh", "-c", args[i+1])
				c.Stdin = os.Stdin
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				if c.Run() != nil {
					return 1
				}
				return 0
			}
		}
		c := exec.Command("/bin/sh")
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if c.Run() != nil {
			return 1
		}
		return 0
	case "git":
		fmt.Println("git version 2.40.0")
		return 0
	case "gh":
		fmt.Fprintln(os.Stderr, "gh must not be used by these checks")
		return 91
	case "tmux":
		// Emulate tmux's public format language, rather than Cockpit's parser.
		verb := ""
		for _, a := range args {
			switch a {
			case "list-sessions", "list-panes", "list-windows", "display-message", "has-session", "new-session", "new-window", "send-keys", "switch-client", "attach-session", "capture-pane", "-V":
				verb = a
			}
			if verb != "" {
				break
			}
		}
		option := func(flag string) string {
			for i, a := range args {
				if a == flag && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		}
		exists := read("exists") == "yes"
		format := func(name, id, f string) string {
			vals := map[string]string{"session_name": name, "session_id": id, "session_windows": "1", "session_attached": "0", "session_last_attached": strconv.FormatInt(time.Now().Unix(), 10), "pid": "900", "start_time": "901", "window_id": "@1", "window_index": "0", "window_name": "shell", "window_active": "1", "window_panes": "1", "pane_id": "%1", "pane_pid": "100", "pane_dead": "0", "pane_active": "1", "pane_current_command": "sh", "pane_current_path": root, "q:pane_current_path": root}
			return regexp.MustCompile(`#\{([^{}]+)\}`).ReplaceAllStringFunc(f, func(s string) string { return vals[s[2:len(s)-1]] })
		}
		switch verb {
		case "-V":
			fmt.Println("tmux 3.4")
		case "list-sessions":
			f := option("-F")
			if f == "" {
				f = "#{session_name}"
			}
			fmt.Println(format("alpha-session", "$1", f))
			if exists {
				fmt.Println(format("spine", "$2", f))
			}
		case "list-panes", "list-windows":
			f := option("-F")
			fmt.Println(format("alpha-session", "$1", f))
			if exists {
				fmt.Println(format("spine", "$2", f))
			}
		case "display-message":
			f := ""
			if len(args) > 0 {
				f = args[len(args)-1]
			}
			fmt.Println(format("cockpit", "$0", f))
		case "has-session":
			if strings.Contains(option("-t"), "spine") && !exists {
				fmt.Fprintln(os.Stderr, "can't find session: spine")
				return 1
			}
		case "new-session", "new-window":
			if verb == "new-session" {
				_ = os.WriteFile(filepath.Join(root, "exists"), []byte("yes"), 0600)
			}
			// tmux options precede its shell-command; honour both single-string
			// and multiple-argument shell commands at this public boundary.
			var payload []string
			for i := 1; i < len(args); i++ {
				a := args[i]
				if a == ";" {
					break
				}
				if strings.HasPrefix(a, "-") {
					switch a {
					case "-s", "-t", "-n", "-c", "-e", "-F":
						i++
					}
					continue
				}
				payload = append(payload, a)
			}
			{
				c := exec.Command(os.Getenv("SHELL"))
				if len(payload) > 0 {
					c = exec.Command("/bin/sh", "-c", strings.Join(payload, " "))
					if len(payload) > 1 {
						binary := payload[0]
						switch filepath.Base(binary) {
						case "sh", "bash", "zsh":
							binary = filepath.Join(root, "bin", filepath.Base(binary))
						}
						c = exec.Command(binary, payload[1:]...)
					}
				}
				// Prove the final shell accepts input, whether the builder
				// chooses $SHELL, sh, bash or an absolute /bin/sh.
				c.Stdin = strings.NewReader("printf shell-ready > \"$SPINE_CARD_WORLD/shell-reached\"\nexit\n")
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				if c.Run() != nil {
					return 1
				}
			}
		case "send-keys":
			// A builder may create a shell session, then type the console command.
			if len(args) > 2 && (args[len(args)-1] == "Enter" || args[len(args)-1] == "C-m") {
				c := exec.Command("/bin/sh", "-c", args[len(args)-2])
				c.Stdin = strings.NewReader("printf shell-ready > \"$SPINE_CARD_WORLD/shell-reached\"\nexit\n")
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				if c.Run() != nil {
					return 1
				}
			}
		case "capture-pane":
			fmt.Print("ordinary session preview\n")
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown fake tool", tool)
	return 92
}

type world struct {
	t              *testing.T
	root, bin, cfg string
}

func newWorld(t *testing.T, extra string) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir()}
	w.bin = filepath.Join(w.root, "bin")
	w.cfg = filepath.Join(w.root, "config.toml")
	if err := os.Mkdir(w.bin, 0700); err != nil {
		t.Fatal(err)
	}
	w.write("calls", "")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tmux", "spine", "gh", "git", "fake-shell", "sh", "bash", "zsh"} {
		if err := os.Symlink(exe, filepath.Join(w.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SPINE_CARD_FAKE", "1")
	t.Setenv("SPINE_CARD_WORLD", w.root)
	t.Setenv("PATH", w.bin)
	t.Setenv("HOME", w.root)
	t.Setenv("SHELL", filepath.Join(w.bin, "fake-shell"))
	t.Setenv("TMUX", "fixture-socket,900,0")
	t.Setenv("TERM", "xterm-256color")
	w.write("config.toml", "[general]\nsession_name = \"cockpit\"\ndefault_view = \"grid\"\nrefresh_interval = 1\n[github]\nenabled = false\nrefresh_interval = 60\n[daemon]\nenabled = false\n"+extra)
	w.snapshot(fixture(t))
	return w
}

func (w *world) write(name, value string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.root, name), []byte(value), 0600); err != nil {
		w.t.Fatal(err)
	}
}
func (w *world) snapshot(s map[string]any) {
	w.t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		w.t.Fatal(err)
	}
	w.write("snapshot", string(b))
}
func (w *world) calls() []call {
	w.t.Helper()
	b, _ := os.ReadFile(filepath.Join(w.root, "calls"))
	var out []call
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var c call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}
func (w *world) readOnly() {
	w.t.Helper()
	for _, c := range w.calls() {
		if c.Tool == "spine" && strings.Join(c.Args, " ") != "bearings --json" {
			w.t.Errorf("reading Cockpit invoked spine %q", c.Args)
		}
		if c.Tool == "gh" {
			w.t.Error("GitHub was disabled but gh was invoked")
		}
	}
}

func fixture(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("../../../../../testdata/spine/bearings.json")
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	// Keep all sample ages; the fixture stays useful after its calendar date.
	at, err := time.Parse(time.RFC3339, s["at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	delta := time.Since(at)
	var rebase func(any)
	rebase = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "at" {
					if ts, ok := x.(string); ok {
						if old, e := time.Parse(time.RFC3339, ts); e == nil {
							v[k] = old.Add(delta).UTC().Format(time.RFC3339)
						}
					}
				} else {
					rebase(x)
				}
			}
		case []any:
			for _, x := range v {
				rebase(x)
			}
		}
	}
	rebase(s)
	return s
}
func empty(t *testing.T) map[string]any {
	s := fixture(t)
	for _, k := range []string{"needs_you", "underway", "charted_next", "landed", "errors"} {
		s[k] = []any{}
	}
	return s
}

// Use the same Bubble Tea input/render surface as Cockpit's own TUI tests.
// A probe samples the public View on the event loop, avoiding races and any
// knowledge of how the implementation stores or delivers its source messages.
type probe chan string
type driverModel struct{ model tea.Model }

func (d driverModel) Init() tea.Cmd { return d.model.Init() }
func (d driverModel) View() string  { return d.model.View() }
func (d driverModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if p, ok := msg.(probe); ok {
		p <- ansi.Strip(d.model.View())
		return d, nil
	}
	m, c := d.model.Update(msg)
	d.model = m
	return d, c
}

type driver struct {
	t    *testing.T
	p    *tea.Program
	done chan error
}

func (w *world) drive(width int) *driver {
	w.t.Helper()
	cfg, err := config.Load(w.cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	d := &driver{t: w.t, done: make(chan error, 1)}
	d.p = tea.NewProgram(driverModel{tui.NewModel(cfg, w.cfg)}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	go func() { _, err := d.p.Run(); d.done <- err }()
	w.t.Cleanup(func() {
		d.p.Quit()
		select {
		case err := <-d.done:
			if err != nil {
				w.t.Errorf("UI exited: %v", err)
			}
		case <-time.After(3 * time.Second):
			d.p.Kill()
			w.t.Error("UI did not exit")
		}
	})
	d.p.Send(tea.WindowSizeMsg{Width: width, Height: 100})
	return d
}
func (d *driver) screen() string {
	d.t.Helper()
	p := make(probe, 1)
	d.p.Send(p)
	select {
	case s := <-p:
		return s
	case err := <-d.done:
		d.t.Fatalf("UI exited before check: %v", err)
	case <-time.After(3 * time.Second):
		d.t.Fatal("UI stopped responding")
	}
	return ""
}
func (d *driver) key(k string) {
	d.t.Helper()
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEscape}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	}
	d.p.Send(msg)
}
func (d *driver) wait(why string, f func(string) bool) string {
	d.t.Helper()
	end := time.Now().Add(4 * time.Second)
	var s string
	for time.Now().Before(end) {
		s = d.screen()
		if f(s) {
			return s
		}
		time.Sleep(25 * time.Millisecond)
	}
	d.t.Fatalf("%s\nScreen:\n%s", why, s)
	return ""
}
func containsAll(s string, values ...string) bool {
	for _, v := range values {
		if !strings.Contains(strings.ToLower(s), strings.ToLower(v)) {
			return false
		}
	}
	return true
}

// Read the tile's visible border box, never the preview or attention badge.
func tile(s string) string {
	lines := strings.Split(s, "\n")
	for row, line := range lines {
		runes := []rune(line)
		idx := strings.Index(line, "spine")
		if idx < 0 {
			continue
		}
		col := len([]rune(line[:idx]))
		left, right := -1, -1
		for i := col - 1; i >= 0; i-- {
			if runes[i] == '│' || runes[i] == '┃' {
				left = i
				break
			}
		}
		for i := col + 5; i < len(runes); i++ {
			if runes[i] == '│' || runes[i] == '┃' {
				right = i
				break
			}
		}
		if left < 0 || right < 0 {
			continue
		}
		var content []string
		for r := row; r < len(lines); r++ {
			rr := []rune(lines[r])
			if len(rr) <= right {
				break
			}
			if rr[left] == '╰' || rr[left] == '└' || rr[left] == '┗' {
				break
			}
			if rr[left] != '│' && rr[left] != '┃' {
				break
			}
			content = append(content, strings.TrimSpace(string(rr[left+1:right])))
		}
		return strings.Join(content, "\n")
	}
	return ""
}
func selectSpine(d *driver) string {
	d.t.Helper()
	d.wait("one spine tile must exist alongside sessions", func(s string) bool { return tile(s) != "" && strings.Contains(s, "alpha-session") })
	// Two tiles fit on one row at 120 columns, regardless of their ordering.
	for _, k := range []string{"", "left", "right", "down", "left", "right", "up"} {
		if k != "" {
			d.key(k)
		}
		s := d.screen()
		if containsAll(s, "Needs you", "Underway", "Charted next", "Landed") {
			return s
		}
		time.Sleep(30 * time.Millisecond)
		s = d.screen()
		if containsAll(s, "Needs you", "Underway", "Charted next", "Landed") {
			return s
		}
	}
	d.t.Fatalf("selecting spine must show the fleet preview\n%s", d.screen())
	return ""
}

func TestHarness(t *testing.T) {
	w := newWorld(t, "")
	d := w.drive(120)
	d.wait("the existing session tile must load", func(s string) bool { return strings.Contains(s, "alpha-session") })
	d.key("a")
	d.wait("the existing attention view must open", func(s string) bool { return containsAll(s, "attention", "housekeeping") })
	w.readOnly()
}

func TestFakeConsoleBoundary(t *testing.T) {
	w := newWorld(t, "")
	w.write("tui-fails", "yes")
	cmd := exec.Command("tmux", "new-session", "-d", "-s", "spine", `spine tui || spine bearings; exec "$SHELL"`)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fake tmux launch failed: %v\n%s", err, b)
	}
	var console []string
	for _, c := range w.calls() {
		if c.Tool == "spine" || c.Tool == "fake-shell" {
			console = append(console, c.Tool+" "+strings.Join(c.Args, " "))
		}
	}
	if strings.Join(console, ",") != "spine tui,spine bearings,fake-shell " {
		t.Fatalf("fake console order: %v", console)
	}
}

func TestSource(t *testing.T) {
	t.Run("polls-on-local-cadence-and-updates", func(t *testing.T) {
		w := newWorld(t, "")
		d := w.drive(120)
		d.key("a")
		d.wait("startup must read spine bearings --json", func(string) bool {
			for _, c := range w.calls() {
				if c.Tool == "spine" && strings.Join(c.Args, " ") == "bearings --json" {
					return true
				}
			}
			return false
		})
		d.wait("initial snapshot must reach the queue", func(s string) bool { return strings.Contains(s, "Allow up to $5") })
		d.wait("spine must be polled on successive automatic local ticks", func(string) bool {
			var last time.Time
			rounds := 0
			for _, c := range w.calls() {
				if c.Tool == "spine" && strings.Join(c.Args, " ") == "bearings --json" && (last.IsZero() || c.At.Sub(last) >= 600*time.Millisecond) {
					rounds++
					last = c.At
				}
			}
			return rounds >= 3
		})
		s := fixture(t)
		s["needs_you"].([]any)[0].(map[string]any)["title"] = "Refreshed request from the fleet"
		w.snapshot(s)
		d.wait("the automatic local refresh must update spine without r", func(s string) bool { return strings.Contains(s, "Refreshed request from the fleet") })
		var spine, local []time.Time
		for _, c := range w.calls() {
			if c.Tool == "spine" {
				spine = append(spine, c.At)
			}
			if c.Tool == "tmux" && len(c.Args) > 0 && c.Args[0] == "list-sessions" {
				local = append(local, c.At)
			}
		}
		if len(spine) < 2 || len(local) < 2 {
			t.Fatalf("need startup plus local refresh: spine=%v local=%v", spine, local)
		}
		for _, at := range spine {
			matched := false
			for _, other := range local {
				if at.Sub(other).Abs() < 500*time.Millisecond {
					matched = true
				}
			}
			if !matched {
				t.Errorf("spine poll at %s had no matching local poll", at)
			}
		}
		w.readOnly()
	})
	for _, mode := range []string{"missing", "failed", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, "")
			w.write("mode", mode)
			if mode == "missing" {
				if err := os.Remove(filepath.Join(w.bin, "spine")); err != nil {
					t.Fatal(err)
				}
			}
			d := w.drive(120)
			d.key("a")
			reason := map[string]string{"missing": `(?i)path|not found|no such|not installed|missing|executable`, "failed": `(?i)permission denied|exit|fail|error`, "invalid": `(?i)json|parse|invalid|syntax|decode|character`}[mode]
			d.wait("attention coverage must name unreadable spine and its reason", func(s string) bool {
				return containsAll(s, "spine") && regexp.MustCompile(`(?i)unreadable|unavailable|could not|failed|coverage`).MatchString(s) && regexp.MustCompile(reason).MatchString(s)
			})
			w.readOnly()
		})
	}
	t.Run("failed-read-recovers", func(t *testing.T) {
		w := newWorld(t, "")
		w.write("mode", "invalid")
		d := w.drive(120)
		d.key("a")
		d.wait("unreadable spine must be shown", func(s string) bool {
			return containsAll(s, "spine") && regexp.MustCompile(`(?i)json|parse|invalid|decode`).MatchString(s)
		})
		w.write("mode", "")
		d.key("r")
		d.wait("a later valid snapshot must recover the queue", func(s string) bool {
			return strings.Contains(s, "Allow up to $5") && !strings.Contains(s, "this is not JSON")
		})
		w.readOnly()
	})
}

func countLabel(s string, n int, label string) bool {
	pat := fmt.Sprintf(`(?i)(\b%d\s*(%s)\b|\b(%s)\s*[:=]?\s*%d\b)`, n, label, label, n)
	return regexp.MustCompile(pat).MatchString(s)
}
func spendAndCap(s string) bool {
	// Accept ratio notation, separate spent/cap labels, and currency badges.
	return regexp.MustCompile(`(^|[^0-9.])20(\.0+)?([^0-9.]|$)`).MatchString(s) && regexp.MustCompile(`(^|[^0-9.])50(\.0+)?([^0-9.]|$)`).MatchString(s)
}
func TestTile(t *testing.T) {
	t.Run("desktop-fleet-rollup", func(t *testing.T) {
		w := newWorld(t, "")
		s := fixture(t)
		// Another goal in another repository. Stream spend and agent spend
		// must not be counted again, and a charted cap is not an underway cap.
		s["underway"] = append(s["underway"].([]any), map[string]any{"repo": "notes", "goal": "export-v1", "kind": "goal", "title": "Export notes", "now": "Writing exporter", "spent": 7.6, "cap": 20, "agents": []any{map[string]any{"id": "exporter", "state": "running", "spent": 2}}})
		w.snapshot(s)
		d := w.drive(120)
		view := d.wait("desktop tile must summarize 1 ask, 2 goals, 3 agents and $20/$50", func(s string) bool {
			box := tile(s)
			return strings.Contains(s, "alpha-session") && regexp.MustCompile(`\b1\b`).MatchString(box) && countLabel(box, 2, `goals?|g`) && countLabel(box, 3, `agents?|ag|a`) && spendAndCap(box)
		})
		box := tile(view)
		if !regexp.MustCompile(`\b1\b`).MatchString(box) {
			t.Errorf("tile must show one needs-you item: %q", box)
		}
		if !countLabel(box, 2, `goals?|g`) || !countLabel(box, 3, `agents?|ag|a`) {
			t.Errorf("tile must show 2 underway goals and 3 agents: %q", box)
		}
		if !spendAndCap(box) {
			t.Errorf("tile must show $20 spent against $50 cap, without double counting: %q", box)
		}
		if strings.Count(box, "spine") != 1 {
			t.Errorf("expected one spine tile: %q", box)
		}
		w.readOnly()
	})
	t.Run("empty-fleet", func(t *testing.T) {
		w := newWorld(t, "")
		w.snapshot(empty(t))
		d := w.drive(120)
		v := d.wait("empty spine tile must say nothing needs Jeff", func(s string) bool {
			return regexp.MustCompile(`(?i)nothing|none|no\s+(asks?|attention|needs?)|0\s*(needs?|asks?|attention)|needs?\s*(you|Jeff)?\s*[:=]?\s*0`).MatchString(tile(s))
		})
		box := tile(v)
		if !regexp.MustCompile(`(?i)nothing|none|no\s+(asks?|attention|needs?)|0\s*(needs?|asks?|attention)|needs?\s*(you|Jeff)?\s*[:=]?\s*0`).MatchString(box) {
			t.Errorf("tile must say nothing needs Jeff: %q", box)
		}
		w.readOnly()
	})
	for _, width := range []int{69, 40} {
		t.Run(fmt.Sprintf("mobile-%d", width), func(t *testing.T) {
			w := newWorld(t, "")
			d := w.drive(width)
			v := d.wait("mobile spine tile must show its needs-you count", func(s string) bool {
				return strings.Contains(tile(s), "spine") && regexp.MustCompile(`\b1\b`).MatchString(tile(s))
			})
			box := tile(v)
			var lines []string
			for _, line := range strings.Split(box, "\n") {
				if strings.TrimSpace(line) != "" {
					lines = append(lines, line)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("mobile tile must have one content line: %q", box)
			}
			if !regexp.MustCompile(`[●○◉◌⚠!•◆◇▶▸]`).MatchString(box) || !strings.Contains(box, "spine") || !regexp.MustCompile(`\b1\b`).MatchString(box) {
				t.Errorf("mobile line needs marker, spine and needs-you count: %q", box)
			}
			if regexp.MustCompile(`(?i)goals?|agents?|\$|12[.,]4|30`).MatchString(box) {
				t.Errorf("desktop detail leaked into mobile tile: %q", box)
			}
			w.readOnly()
		})
	}
	t.Run("existing-spine-session-still-has-one-tile", func(t *testing.T) {
		w := newWorld(t, "")
		w.write("exists", "yes")
		s := fixture(t)
		s["errors"] = []any{}
		w.snapshot(s)
		d := w.drive(69)
		view := d.wait("existing spine session must be folded into the fleet tile", func(s string) bool {
			return strings.Contains(s, "alpha-session") && regexp.MustCompile(`\b1\b`).MatchString(tile(s))
		})
		// Mobile has no preview, so another name here is a duplicate tile.
		if strings.Count(view, "spine") != 1 {
			t.Errorf("expected exactly one spine tile with an existing session\n%s", view)
		}
		w.readOnly()
	})
}

func TestPreview(t *testing.T) {
	t.Run("all-four-sections", func(t *testing.T) {
		w := newWorld(t, "")
		fixture := fixture(t)
		fixture["underway"].([]any)[1].(map[string]any)["now"] = "Reviewing saved cart tests"
		w.snapshot(fixture)
		d := w.drive(120)
		selectSpine(d)
		s := d.wait("selected spine must preview the loaded snapshot", func(s string) bool {
			return containsAll(s, "Allow up to $5", "Reviewing saved cart tests", "search v1: full-text search over notes", "rss v1: feeds for every tag")
		})
		for _, text := range []string{"Needs you", "Underway", "Charted next", "Landed", "Allow up to $5", "checkout-v1", "payments waits on a money ask; carts merging", "Saved carts survive sign-out", "Reviewing saved cart tests", "search v1: full-text search over notes", "Address form validates postcodes", "rss v1: feeds for every tag"} {
			if !strings.Contains(s, text) {
				t.Errorf("preview missing %q\n%s", text, s)
			}
		}
		if !regexp.MustCompile(`(?i)\bnow\b`).MatchString(s) || !regexp.MustCompile(`(?i)24\s*(hours?|hrs?|h)`).MatchString(s) {
			t.Errorf("preview must identify Now and the last 24 hours\n%s", s)
		}
		w.readOnly()
	})
	t.Run("none-in-every-empty-section", func(t *testing.T) {
		w := newWorld(t, "")
		w.snapshot(empty(t))
		d := w.drive(120)
		s := selectSpine(d)
		headings := []string{"needs you", "underway", "charted next", "landed"}
		lower := strings.ToLower(s)
		for i, h := range headings {
			start := strings.Index(lower, h)
			if start < 0 {
				t.Errorf("missing %s", h)
				continue
			}
			end := len(lower)
			if i+1 < len(headings) {
				if pos := strings.Index(lower[start+len(h):], headings[i+1]); pos >= 0 {
					end = start + len(h) + pos
				}
			}
			if !regexp.MustCompile(`\bnone\b`).MatchString(lower[start:end]) {
				t.Errorf("empty %s section must say none\n%s", h, s)
			}
		}
		w.readOnly()
	})
}

func TestAttention(t *testing.T) {
	w := newWorld(t, "")
	s := fixture(t)
	// Two asks for the same goal and a third repo test against deduplication
	// by goal/repository and against a shop-only fleet implementation.
	s["needs_you"] = append(s["needs_you"].([]any), map[string]any{"repo": "shop", "goal": "checkout-v1", "stream": "address", "agent": "reviewer", "kind": "ask", "title": "Review postcode rules", "at": s["at"]}, map[string]any{"repo": "notes", "goal": "search-v1", "kind": "ask", "title": "Choose search ordering", "at": s["at"]})
	w.snapshot(s)
	d := w.drive(240)
	d.key("a")
	v := d.wait("every snapshot ask must appear in attention", func(s string) bool {
		return containsAll(s, "Allow up to $5 for the sandbox payment API?", "Review postcode rules", "Choose search ordering")
	})
	for _, item := range s["needs_you"].([]any) {
		item := item.(map[string]any)
		title := item["title"].(string)
		found := false
		lines := strings.Split(v, "\n")
		for i, line := range lines {
			if !strings.Contains(line, title) {
				continue
			}
			start, end := max(0, i-1), min(len(lines), i+3)
			if containsAll(strings.Join(lines[start:end], "\n"), title, item["repo"].(string), item["goal"].(string)) {
				found = true
			}
		}
		if !found {
			t.Errorf("queue row must name repo, goal, title for %q\n%s", title, v)
		}
	}
	w.readOnly()
}

func TestEnter(t *testing.T) {
	for _, surface := range []string{"tile", "attention"} {
		for _, exists := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-existing-%t", surface, exists), func(t *testing.T) {
				command := `printf configured-console > "$SPINE_CARD_WORLD/custom-ran"; exec "$SHELL"`
				w := newWorld(t, "[spine]\ncommand = "+strconv.Quote(command)+"\n")
				if exists {
					w.write("exists", "yes")
				}
				d := w.drive(120)
				if surface == "tile" {
					selectSpine(d)
				} else {
					d.key("a")
					d.wait("spine ask must be selectable", func(s string) bool { return strings.Contains(s, "Allow up to $5") })
				}
				d.key("enter")
				d.wait("Enter must switch the client to spine", func(string) bool {
					for _, c := range w.calls() {
						if c.Tool == "tmux" && len(c.Args) > 0 && c.Args[0] == "switch-client" {
							for i, a := range c.Args {
								if a == "-t" && i+1 < len(c.Args) && strings.TrimPrefix(c.Args[i+1], "=") == "spine" {
									return true
								}
							}
						}
					}
					return false
				})
				created := 0
				for _, c := range w.calls() {
					if c.Tool == "tmux" && len(c.Args) > 0 && c.Args[0] == "new-session" {
						created++
						if !containsAll(strings.Join(c.Args, " "), "spine") {
							t.Errorf("wrong session created: %q", c.Args)
						}
					}
					if c.Tool == "tmux" {
						for _, a := range c.Args {
							if a == "bind-key" || a == "unbind-key" || a == "bind" || a == "unbind" || a == "prefix" || a == "prefix2" {
								t.Errorf("existing return key must remain unchanged: %q", c.Args)
							}
						}
					}
				}
				if exists && created != 0 {
					t.Errorf("existing spine session must be reused, created %d", created)
				}
				if !exists {
					if created != 1 {
						t.Errorf("missing spine session must be created once, got %d", created)
					}
					b, err := os.ReadFile(filepath.Join(w.root, "custom-ran"))
					if err != nil || string(b) != "configured-console" {
						t.Errorf("new spine session did not run spine.command: %q %v", b, err)
					}
				}
				for _, c := range w.calls() {
					if c.Tool == "spine" && strings.Join(c.Args, " ") != "bearings --json" {
						t.Errorf("custom command must replace default console: %q", c.Args)
					}
				}
			})
		}
	}
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprintf("default-tui-fails-%t", fails), func(t *testing.T) {
			w := newWorld(t, "")
			if fails {
				w.write("tui-fails", "yes")
			}
			d := w.drive(120)
			selectSpine(d)
			d.key("enter")
			d.wait("default console must leave a shell that accepts input", func(string) bool {
				b, _ := os.ReadFile(filepath.Join(w.root, "shell-reached"))
				return string(b) == "shell-ready"
			})
			var console []string
			for _, c := range w.calls() {
				if c.Tool == "spine" && strings.Join(c.Args, " ") != "bearings --json" {
					console = append(console, strings.Join(c.Args, " "))
				}
			}
			want := "tui"
			if fails {
				want = "tui,bearings"
			}
			if strings.Join(console, ",") != want {
				t.Errorf("default console commands = %q, want %s", console, want)
			}
		})
	}
}

func TestDoctor(t *testing.T) {
	// Build before isolating PATH; run the actual public doctor command.
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "cockpit")
	build := exec.Command(goBin, "build", "-o", bin, ".")
	build.Dir = root
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build doctor binary: %v\n%s", err, b)
	}
	for _, mode := range []string{"healthy", "missing", "failed", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, "")
			sample := fixture(t)
			sample["errors"] = []any{}
			w.snapshot(sample)
			w.write("mode", mode)
			if mode == "missing" {
				if err := os.Remove(filepath.Join(w.bin, "spine")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "--config", w.cfg, "doctor", "--json")
			output, runErr := cmd.Output()
			if ctx.Err() != nil {
				t.Fatal("doctor did not finish")
			}
			var report struct {
				Checks []struct {
					ID, Scope, Status, Summary, Remedy string
					Evidence                           []string
				}
			}
			if err := json.Unmarshal(output, &report); err != nil {
				t.Fatalf("doctor output not JSON: %v, exit %v\n%s", err, runErr, output)
			}
			var facts []string
			path, snapshot, pathPass, snapshotPass, remedy := false, false, false, false, false
			pathUnavailable, snapshotUnreadable := false, false
			for _, c := range report.Checks {
				text := c.ID + " " + c.Scope + " " + c.Summary + " " + strings.Join(c.Evidence, " ")
				if !strings.Contains(strings.ToLower(text), "spine") {
					continue
				}
				facts = append(facts, text+" "+c.Remedy)
				isPath := regexp.MustCompile(`(?i)path|installed|binary|executable|not found|missing`).MatchString(text)
				isSnapshot := regexp.MustCompile(`(?i)snapshot|bearings|json|fleet`).MatchString(text)
				path = path || isPath
				snapshot = snapshot || isSnapshot
				pathPass = pathPass || (isPath && c.Status == "pass")
				snapshotPass = snapshotPass || (isSnapshot && c.Status == "pass")
				pathUnavailable = pathUnavailable || (isPath && c.Status != "pass")
				snapshotUnreadable = snapshotUnreadable || (isSnapshot && c.Status != "pass")
				remedy = remedy || strings.TrimSpace(c.Remedy) != ""
			}
			joined := strings.Join(facts, "\n")
			if !path {
				t.Errorf("doctor must report spine PATH availability\n%s", output)
			}
			if !snapshot {
				t.Errorf("doctor must report snapshot readability (or why it cannot probe)\n%s", output)
			}
			if mode == "healthy" {
				if !pathPass || !snapshotPass {
					t.Errorf("readable spine must have a PASS\n%s", output)
				}
			} else {
				if !snapshotUnreadable || (mode == "missing" && !pathUnavailable) {
					t.Errorf("unreadable spine must not be reported as passing\n%s", output)
				}
				if !remedy {
					t.Errorf("unavailable spine needs a smallest next action\n%s", output)
				}
				reason := map[string]string{"missing": `(?i)install|path|not found|missing`, "failed": `(?i)permission|exit|fail|error`, "invalid": `(?i)json|parse|invalid|syntax|decode|character`}[mode]
				if !regexp.MustCompile(reason).MatchString(joined) {
					t.Errorf("doctor must explain %s spine: %q", mode, joined)
				}
			}
			if mode != "missing" {
				read := false
				for _, c := range w.calls() {
					if c.Tool == "spine" && strings.Join(c.Args, " ") == "bearings --json" {
						read = true
					}
				}
				if !read {
					t.Error("doctor must actually probe spine bearings --json")
				}
			}
			w.readOnly()
		})
	}
}
