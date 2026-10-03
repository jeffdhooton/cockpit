package doctor

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// spy records every edge doctor touches and answers from scripts, so a test
// can prove no write, launch or trust change ever happened.
type spy struct {
	mu       sync.Mutex
	files    map[string]string
	dirs     map[string]bool
	execs    [][]string
	execOut  map[string]string // joined argv prefix → stdout
	execErr  map[string]error
	lookPath map[string]string
	dialErr  error
	now      time.Time
}

func newSpy() *spy {
	return &spy{
		files:    map[string]string{},
		dirs:     map[string]bool{},
		execOut:  map[string]string{},
		execErr:  map[string]error{},
		lookPath: map[string]string{"tmux": "/usr/bin/tmux", "git": "/usr/bin/git"},
		now:      time.Unix(1_700_000_000, 0),
	}
}

type fakeInfo struct {
	name string
	dir  bool
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

func (s *spy) stat(p string) (fs.FileInfo, error) {
	if s.dirs[p] {
		return fakeInfo{name: filepath.Base(p), dir: true, mode: fs.ModeDir | 0o755}, nil
	}
	if _, ok := s.files[p]; ok {
		return fakeInfo{name: filepath.Base(p), mode: 0o644}, nil
	}
	return nil, fs.ErrNotExist
}

func (s *spy) readFile(p string) ([]byte, error) {
	if c, ok := s.files[p]; ok {
		return []byte(c), nil
	}
	return nil, fs.ErrNotExist
}

func (s *spy) exec(ctx context.Context, name string, args ...string) (string, int, error) {
	s.mu.Lock()
	s.execs = append(s.execs, append([]string{name}, args...))
	s.mu.Unlock()
	key := name + " " + strings.Join(args, " ")
	for prefix, err := range s.execErr {
		if strings.HasPrefix(key, prefix) {
			return "", 1, err
		}
	}
	for prefix, out := range s.execOut {
		if strings.HasPrefix(key, prefix) {
			return out, 0, nil
		}
	}
	return "", 0, nil
}

func (s *spy) deps() Deps {
	return Deps{
		Stat:         s.stat,
		ReadFile:     s.readFile,
		EvalSymlinks: func(p string) (string, error) { _, err := s.stat(p); return p, err },
		LookPath: func(name string) (string, error) {
			if p, ok := s.lookPath[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Exec: s.exec,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if s.dialErr != nil {
				return nil, s.dialErr
			}
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		},
		Now:        func() time.Time { return s.now },
		Executable: func() (string, error) { return "/usr/local/bin/cockpit", nil },
		Home:       "/home/alex",
		Version:    "1.2.3",
	}
}

func (s *spy) ran(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.execs {
		if strings.HasPrefix(strings.Join(e, " "), prefix) {
			return true
		}
	}
	return false
}

func byID(r Report) map[string]Check {
	m := map[string]Check{}
	for _, c := range r.Checks {
		m[c.Scope+"/"+c.ID] = c
	}
	return m
}

const minimalConfig = "[general]\nsession_name = \"cockpit\"\n\n[github]\nenabled = false\n\n[daemon]\nenabled = false\n"

func TestFreshInstallIsCoreReadyAndTouchesNothing(t *testing.T) {
	s := newSpy()
	s.files["/home/alex/.config/cockpit/config.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running on /tmp/tmux-501/default")

	rep := Run(context.Background(), Options{ConfigPath: "/home/alex/.config/cockpit/config.toml"}, s.deps())
	if !rep.CoreReady || rep.Failed() {
		t.Fatalf("report = %+v", rep)
	}
	ch := byID(rep)
	if ch["local/tmux"].Status != Pass || !strings.Contains(ch["local/tmux"].Summary, "no server") {
		t.Errorf("no server is valid: %+v", ch["local/tmux"])
	}
	if ch["local/repos"].Status != Pass {
		t.Errorf("no repos is valid: %+v", ch["local/repos"])
	}
	if ch["local/github"].Status != Skip || ch["local/daemon"].Status != Skip || ch["local/obsidian"].Status != Skip {
		t.Errorf("disabled integrations skip: %+v %+v %+v", ch["local/github"], ch["local/daemon"], ch["local/obsidian"])
	}
	for _, e := range s.execs {
		joined := strings.Join(e, " ")
		for _, forbidden := range []string{"new-session", "new-window", "respawn", "kill", "send-keys", "set-option", "daemon start", "hook install", "app-server", "launchctl", "git fetch", "git pull"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("doctor ran %q", joined)
			}
		}
	}
	if rep.SchemaVersion != 1 || rep.Version != "1.2.3" {
		t.Errorf("header = %+v", rep)
	}
}

func TestMissingConfigStillChecksTmuxAndSkipsDependents(t *testing.T) {
	s := newSpy()
	delete(s.lookPath, "tmux")
	rep := Run(context.Background(), Options{ConfigPath: "/nope/config.toml"}, s.deps())
	ch := byID(rep)
	if ch["local/config"].Status != Fail || !strings.Contains(ch["local/config"].Remedy, "cockpit init") {
		t.Errorf("config = %+v", ch["local/config"])
	}
	if ch["local/binary"].Status != Fail || !strings.Contains(ch["local/binary"].Summary, "tmux not found") {
		t.Errorf("a missing tmux must not hide behind the config failure: %+v", ch["local/binary"])
	}
	if ch["local/repos"].Status != Skip || !strings.Contains(ch["local/repos"].Summary, "config") {
		t.Errorf("dependent checks skip and say why: %+v", ch["local/repos"])
	}
	if !rep.Failed() || rep.CoreReady {
		t.Error("missing config is a failure")
	}
}

func TestMalformedConfigReportsParseErrorAndUnknownKeysWarn(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = "[general\nsession_name = \"x\"\n"
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	if byID(rep)["local/config"].Status != Fail {
		t.Errorf("config = %+v", byID(rep)["local/config"])
	}

	s = newSpy()
	s.files["/c.toml"] = minimalConfig + "\n[general]\nbogus = 1\n"
	s.files["/c.toml"] = "[general]\nsession_name = \"cockpit\"\nbogus = 1\n[github]\nenabled = false\n[daemon]\nenabled = false\n"
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	rep = Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	ch := byID(rep)["local/config"]
	if ch.Status != Warn || !strings.Contains(strings.Join(ch.Evidence, " "), "general.bogus") {
		t.Errorf("unknown keys warn with their path: %+v", ch)
	}
	if rep.Failed() {
		t.Error("warnings alone are not failures")
	}
}

func TestUnreadableSocketAndProtocolErrorsAreDistinct(t *testing.T) {
	for _, tc := range []struct{ msg, want string }{
		{"error connecting to /tmp/tmux-501/default (Permission denied)", "not accessible"},
		{"protocol version mismatch (client 8, server 7)", "protocol mismatch"},
	} {
		s := newSpy()
		s.files["/c.toml"] = minimalConfig
		s.execErr["/usr/bin/tmux list-sessions"] = errors.New(tc.msg)
		rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
		ch := byID(rep)["local/tmux"]
		if ch.Status != Fail || !strings.Contains(ch.Summary, tc.want) {
			t.Errorf("%s → %+v", tc.msg, ch)
		}
	}
}

func TestDaemonIdentityIsVerifiedNotJustThePort(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not cockpit</html>"))
	}))
	defer other.Close()
	cockpit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"name\":\"cockpit\",\"version\":\"1.2.3\",\"config_path\":\"/elsewhere.toml\",\"sessions\":[]}"}]}}`))
	}))
	defer cockpit.Close()

	for _, tc := range []struct {
		srv  *httptest.Server
		want string
	}{
		{other, "not cockpit"},
		{cockpit, "another config"},
	} {
		s := newSpy()
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(tc.srv.URL, "http://"))
		s.files["/c.toml"] = "[daemon]\nenabled = true\nport = " + port + "\n[github]\nenabled = false\n"
		s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
		deps := s.deps()
		deps.HTTP = tc.srv.Client()
		deps.Dial = (&net.Dialer{}).DialContext
		rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, deps)
		ch := byID(rep)["local/daemon"]
		if ch.Status != Warn || !strings.Contains(ch.Summary, tc.want) {
			t.Errorf("want %q, got %+v", tc.want, ch)
		}
	}
}

func TestHookStatesAreDistinct(t *testing.T) {
	codexCfg := func(trust string) string {
		return "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/usr/local/bin/cockpit hook status --engine codex\"\n" +
			"[[hooks.UserPromptSubmit]]\n[[hooks.UserPromptSubmit.hooks]]\ntype = \"command\"\ncommand = \"/usr/local/bin/cockpit hook status --engine codex\"\n" +
			"[[hooks.PreToolUse]]\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"/usr/local/bin/cockpit hook status --engine codex\"\n" +
			"[[hooks.PermissionRequest]]\n[[hooks.PermissionRequest.hooks]]\ntype = \"command\"\ncommand = \"/usr/local/bin/cockpit hook status --engine codex\"\n" + trust
	}
	trusted := ""
	for _, ev := range []string{"stop", "user_prompt_submit", "pre_tool_use", "permission_request"} {
		trusted += "[hooks.state.\"/home/alex/.codex/config.toml:" + ev + ":0:0\"]\ntrusted_hash = \"sha256:abc\"\n"
	}
	cases := []struct {
		name   string
		codex  string
		panes  string
		status Status
		want   string
		deliv  Status
	}{
		{"untrusted", codexCfg(""), "", Warn, "trust unverified", Skip},
		{"trusted never observed", codexCfg(trusted), "", Pass, "trusted", Skip},
		{"trusted and delivered", codexCfg(trusted), "%1|idle|1699999990\n", Pass, "trusted", Pass},
	}
	for _, tc := range cases {
		s := newSpy()
		s.files["/c.toml"] = minimalConfig
		s.dirs["/home/alex/.codex"] = true
		s.files["/home/alex/.codex/config.toml"] = tc.codex
		s.files["/usr/local/bin/cockpit"] = "bin"
		s.execOut["/usr/bin/tmux list-sessions"] = "app|||\n"
		s.execOut["/usr/bin/tmux list-panes"] = tc.panes
		rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
		ch := byID(rep)
		if ch["local/hooks.codex"].Status != tc.status || !strings.Contains(strings.ToLower(ch["local/hooks.codex"].Summary), tc.want) {
			t.Errorf("%s: codex = %+v", tc.name, ch["local/hooks.codex"])
		}
		if ch["local/hooks.delivery"].Status != tc.deliv {
			t.Errorf("%s: delivery = %+v", tc.name, ch["local/hooks.delivery"])
		}
		if s.ran("codex") {
			t.Errorf("%s: doctor must not spawn codex", tc.name)
		}
	}
	// A stale hook path is reported.
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.dirs["/home/alex/.claude"] = true
	s.files["/home/alex/.claude/settings.json"] = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/old/cockpit hook status"}]}]}}`
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	ch := byID(rep)["local/hooks.claude"]
	if ch.Status != Warn || !strings.Contains(strings.Join(ch.Evidence, " "), "missing: /old/cockpit") {
		t.Errorf("claude = %+v", ch)
	}
}

func TestReposFollowSymlinksAndNameBrokenOnes(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = "[[repos]]\npath = \"/link\"\nlabel = \"linked\"\n[[repos]]\npath = \"/gone\"\nlabel = \"gone\"\n[[repos]]\npath = \"/wt\"\nlabel = \"wt\"\n[github]\nenabled = false\n[daemon]\nenabled = false\n"
	s.dirs["/real"] = true
	s.dirs["/wt"] = true
	s.execOut["/usr/bin/git -C /real rev-parse"] = ".git\n"
	s.execOut["/usr/bin/git -C /wt rev-parse"] = "/main/.git/worktrees/wt\n"
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	deps := s.deps()
	deps.EvalSymlinks = func(p string) (string, error) {
		if p == "/link" {
			return "/real", nil
		}
		if p == "/gone" {
			return "", fs.ErrNotExist
		}
		return p, nil
	}
	deps.LookPath = func(n string) (string, error) { return "/usr/bin/" + n, nil }
	deps.Exec = func(ctx context.Context, name string, args ...string) (string, int, error) {
		return s.exec(ctx, "/usr/bin/"+filepath.Base(name), args...)
	}
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, deps)
	ch := byID(rep)
	if ch["local/repo.linked"].Status != Pass || !strings.Contains(strings.Join(ch["local/repo.linked"].Evidence, " "), "symlink") {
		t.Errorf("linked = %+v", ch["local/repo.linked"])
	}
	if ch["local/repo.gone"].Status != Fail || !strings.Contains(ch["local/repo.gone"].Summary, "gone") {
		t.Errorf("gone = %+v", ch["local/repo.gone"])
	}
	if ch["local/repo.wt"].Status != Pass || !strings.Contains(strings.Join(ch["local/repo.wt"].Evidence, " "), "worktree") {
		t.Errorf("wt = %+v", ch["local/repo.wt"])
	}
}

func TestSlowHostCannotBlockLocalResults(t *testing.T) {
	old := HostBudget
	HostBudget = 300 * time.Millisecond
	defer func() { HostBudget = old }()
	s := newSpy()
	s.files["/c.toml"] = "[[hosts]]\nname = \"slow\"\ntmux = \"/usr/bin/tmux\"\n[[hosts]]\nname = \"fast\"\ntmux = \"/usr/bin/tmux\"\n[github]\nenabled = false\n[daemon]\nenabled = false\n"
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	deps := s.deps()
	deps.Exec = func(ctx context.Context, name string, args ...string) (string, int, error) {
		if name == "ssh" && strings.Contains(strings.Join(args, " "), " slow ") {
			<-ctx.Done()
			return "", 255, ctx.Err()
		}
		if name == "ssh" {
			return "cockpit-doctor-ok\ntmux 3.6a\n__exit=0\n", 0, nil
		}
		return s.exec(ctx, name, args...)
	}
	start := time.Now()
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml", AllHosts: true}, deps)
	if time.Since(start) > HostBudget+5*time.Second {
		t.Errorf("run took %s", time.Since(start))
	}
	ch := byID(rep)
	if ch["local/tmux"].Status != Pass {
		t.Errorf("local results must land: %+v", ch["local/tmux"])
	}
	if ch["slow/ssh"].Status != Fail || len(rep.Incomplete) == 0 {
		t.Errorf("the slow host must be reported incomplete: %+v %v", ch["slow/ssh"], rep.Incomplete)
	}
	if ch["fast/ssh"].Status != Pass {
		t.Errorf("fast = %+v", ch["fast/ssh"])
	}
	for _, e := range s.execs {
		if e[0] == "ssh" && !strings.Contains(strings.Join(e, " "), "ControlMaster=no") {
			t.Errorf("doctor must not create control sockets: %v", e)
		}
	}
}

func TestUnknownHostKeyIsADistinctFailure(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = "[[hosts]]\nname = \"box\"\ntmux = \"/usr/bin/tmux\"\n[github]\nenabled = false\n[daemon]\nenabled = false\n"
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.execErr["ssh "] = errors.New("Host key verification failed.")
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml", Host: "box"}, s.deps())
	ch := byID(rep)["box/ssh"]
	if ch.Status != Fail || !strings.Contains(ch.Remedy, "host key") {
		t.Errorf("ssh = %+v", ch)
	}
	if byID(rep)["box/tmux"].Status != Skip {
		t.Errorf("dependent remote checks skip: %+v", byID(rep)["box/tmux"])
	}
}

func TestMissingRemoteBinaryIsNamed(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = "[[hosts]]\nname = \"box\"\ntmux = \"/opt/tmux\"\n[github]\nenabled = false\n[daemon]\nenabled = false\n"
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.execOut["ssh "] = "cockpit-doctor-ok\n__missing\n"
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml", Host: "box"}, s.deps())
	ch := byID(rep)["box/tmux"]
	if ch.Status != Fail || !strings.Contains(ch.Summary, "/opt/tmux") {
		t.Errorf("tmux = %+v", ch)
	}
}

func TestSpinePathFound(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.lookPath["spine"] = "/usr/bin/spine"
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	ch := byID(rep)["local/spine.path"]
	if ch.Status != Pass || !strings.Contains(ch.Summary, "found on PATH") {
		t.Errorf("spine.path = %+v", ch)
	}
}

func TestSpineSnapshotReadable(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.lookPath["spine"] = "/usr/bin/spine"
	bearingsJSON, err := os.ReadFile("../testdata/spine/bearings.json")
	if err != nil {
		t.Fatal(err)
	}
	s.execOut["/usr/bin/spine bearings --json"] = string(bearingsJSON)
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	chPath := byID(rep)["local/spine.path"]
	chSnapshot := byID(rep)["local/spine.snapshot"]
	if chPath.Status != Pass {
		t.Errorf("spine.path = %+v", chPath)
	}
	if chSnapshot.Status != Pass || !strings.Contains(chSnapshot.Summary, "readable") {
		t.Errorf("spine.snapshot = %+v", chSnapshot)
	}
	if !s.ran("/usr/bin/spine bearings --json") {
		t.Errorf("doctor did not run spine bearings --json")
	}
}

func TestSpineMissing(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	delete(s.lookPath, "spine")
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	chPath := byID(rep)["local/spine.path"]
	chSnapshot := byID(rep)["local/spine.snapshot"]
	if chPath.Status != Warn || !strings.Contains(chPath.Summary, "not found") {
		t.Errorf("spine.path = %+v", chPath)
	}
	if !strings.Contains(chPath.Remedy, "Install") && !strings.Contains(chPath.Remedy, "PATH") {
		t.Errorf("remedy does not suggest install or PATH: %s", chPath.Remedy)
	}
	if chSnapshot.Status != Skip || !strings.Contains(chSnapshot.Summary, "skipped") {
		t.Errorf("spine.snapshot should be skipped when spine is missing: %+v", chSnapshot)
	}
}

func TestSpineBearingsInvalidJSON(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.lookPath["spine"] = "/usr/bin/spine"
	s.execOut["/usr/bin/spine bearings --json"] = "not json"
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	ch := byID(rep)["local/spine.snapshot"]
	if ch.Status != Warn || !strings.Contains(ch.Summary, "not valid JSON") {
		t.Errorf("spine.snapshot = %+v", ch)
	}
	if len(ch.Evidence) == 0 {
		t.Errorf("no error evidence for invalid JSON")
	}
	if !strings.Contains(ch.Remedy, "spine bearings --json") {
		t.Errorf("remedy should suggest spine bearings --json: %s", ch.Remedy)
	}
}

func TestSpineBearingsExecError(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.lookPath["spine"] = "/usr/bin/spine"
	s.execErr["/usr/bin/spine bearings --json"] = errors.New("permission denied")
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	ch := byID(rep)["local/spine.snapshot"]
	if ch.Status != Warn || !strings.Contains(ch.Summary, "failed") {
		t.Errorf("spine.snapshot = %+v", ch)
	}
	if len(ch.Evidence) == 0 || !strings.Contains(strings.Join(ch.Evidence, " "), "permission") {
		t.Errorf("error not captured in evidence: %v", ch.Evidence)
	}
}

func TestSpineBearingsNonZeroExit(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.lookPath["spine"] = "/usr/bin/spine"
	deps := s.deps()
	deps.Exec = func(ctx context.Context, name string, args ...string) (string, int, error) {
		if strings.Join(args, " ") == "bearings --json" {
			return "", 1, nil
		}
		return s.exec(ctx, name, args...)
	}
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, deps)
	ch := byID(rep)["local/spine.snapshot"]
	if ch.Status != Warn || !strings.Contains(ch.Summary, "failed") {
		t.Errorf("spine.snapshot = %+v", ch)
	}
	if !strings.Contains(strings.Join(ch.Evidence, " "), "exit code 1") {
		t.Errorf("exit code not reported in evidence: %v", ch.Evidence)
	}
}

func TestSpineBearingsMissingFields(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	s.lookPath["spine"] = "/usr/bin/spine"
	bearingsJSON := `{"needs_you":[]}`
	s.execOut["/usr/bin/spine bearings --json"] = bearingsJSON
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	ch := byID(rep)["local/spine.snapshot"]
	if ch.Status != Warn || !strings.Contains(ch.Summary, "missing required fields") {
		t.Errorf("spine.snapshot = %+v", ch)
	}
	if !strings.Contains(ch.Remedy, "spine bearings --json") {
		t.Errorf("remedy should suggest spine bearings --json: %s", ch.Remedy)
	}
}
func TestJSONRoundTrips(t *testing.T) {
	s := newSpy()
	s.files["/c.toml"] = minimalConfig
	s.execErr["/usr/bin/tmux list-sessions"] = errors.New("no server running")
	rep := Run(context.Background(), Options{ConfigPath: "/c.toml"}, s.deps())
	var buf strings.Builder
	Render(&buf, rep)
	if !strings.Contains(buf.String(), "Core ready") {
		t.Errorf("render = %s", buf.String())
	}
	_ = os.Stderr
}
