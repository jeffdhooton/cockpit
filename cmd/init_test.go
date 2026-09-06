package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/setup"
)

func testWizard(t *testing.T, input, path string, repos map[string]bool) (*wizard, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	w := newWizard(strings.NewReader(input), out, path)
	w.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	w.deps = setup.SystemDeps(
		func(ctx context.Context, dir string) (string, error) {
			for d := dir; len(d) > 1; d = filepath.Dir(d) {
				if repos[d] {
					return d, nil
				}
			}
			return "", os.ErrNotExist
		},
		func(context.Context) ([]string, error) { return nil, nil },
	)
	return w, out
}

func TestWizardCancelWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	w, out := testWizard(t, "q\n", path, nil)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cancel must not write the config")
	}
	if !strings.Contains(out.String(), "Cancelled") {
		t.Errorf("output = %s", out.String())
	}
}

func TestWizardWritesAMinimalConfigThatLoads(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "myproj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cfg", "config.toml")
	w, out := testWizard(t, "a "+repo+"\np\ns\n", path, map[string]bool{repo: true})
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("saved config must load: %v\n%s", err, out.String())
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Label != "myproj" || cfg.GitHub.Enabled || cfg.Daemon.IsEnabled() {
		t.Errorf("cfg = %+v", cfg)
	}
	if !strings.Contains(out.String(), "Change:") || !strings.Contains(out.String(), "Next steps") {
		t.Errorf("preview and next steps expected:\n%s", out.String())
	}
	if strings.Contains(out.String(), "daemon started") {
		t.Error("the wizard must not start anything")
	}
}

func TestWizardMergesIntoExistingConfigAndKeepsComments(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "second")
	_ = os.MkdirAll(repo, 0o755)
	path := filepath.Join(dir, "config.toml")
	original := "# keep me\n[general]\nsession_name = \"hq\"\n\n[[repos]]\npath = \"/first\"\nlabel = \"first\"\n\n[github]\nenabled = true\n"
	_ = os.WriteFile(path, []byte(original), 0o644)

	w, _ := testWizard(t, "a "+repo+"\np\ns\n", path, map[string]bool{repo: true})
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# keep me") || !strings.Contains(string(raw), "label = \"second\"") || !strings.Contains(string(raw), "enabled = true") {
		t.Errorf("merged = %s", raw)
	}
	backups, _ := filepath.Glob(path + ".bak-*")
	if len(backups) != 1 {
		t.Errorf("want one backup, got %v", backups)
	}
}

func TestWizardDetectsAConcurrentEditAndReviewsAgain(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "r")
	_ = os.MkdirAll(repo, 0o755)
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte("[general]\nsession_name = \"a\"\n"), 0o644)

	// After the first preview the file changes; the second save succeeds
	// against the re-read content.
	w, out := testWizard(t, "a "+repo+"\np\ns\np\ns\n", path, map[string]bool{repo: true})
	w.in = nil
	reader := &editingReader{lines: []string{"a " + repo, "p", "s", "p", "s"}, onLine: map[int]func(){
		2: func() { _ = os.WriteFile(path, []byte("[general]\nsession_name = \"b\"\n"), 0o644) },
	}}
	w2 := newWizard(reader, out, path)
	w2.deps, w2.now = w.deps, w.now
	if err := w2.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "changed while you were reviewing") {
		t.Errorf("expected a re-review:\n%s", out.String())
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.General.SessionName != "b" || len(cfg.Repos) != 1 {
		t.Errorf("the concurrent edit must survive and the entry be merged onto it: %v %+v", err, cfg)
	}
}

// editingReader feeds lines and runs a side effect before a given line is
// read, to simulate an edit racing the wizard.
type editingReader struct {
	lines  []string
	onLine map[int]func()
	i      int
	buf    string
}

func (r *editingReader) Read(p []byte) (int, error) {
	if r.buf == "" {
		if r.i >= len(r.lines) {
			return 0, os.ErrClosed
		}
		if fn, ok := r.onLine[r.i]; ok {
			fn()
		}
		r.buf = r.lines[r.i] + "\n"
		r.i++
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func TestWizardRefusesUnsafeMergeAndLeavesFileIntact(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "r")
	_ = os.MkdirAll(repo, 0o755)
	path := filepath.Join(dir, "config.toml")
	broken := "[general\nsession_name = \"a\"\n"
	_ = os.WriteFile(path, []byte(broken), 0o644)
	w, out := testWizard(t, "a "+repo+"\np\n", path, map[string]bool{repo: true})
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != broken {
		t.Errorf("file changed: %q", raw)
	}
	if !strings.Contains(out.String(), "cannot be merged safely") || !strings.Contains(out.String(), "[[repos]]") {
		t.Errorf("expected the snippet to be shown:\n%s", out.String())
	}
}

func TestPlainInitOnExistingFilePointsToInteractiveReview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte("[general]\n"), 0o644)
	cfgPath = path
	defer func() { cfgPath = "" }()
	initInteractive = false
	if err := runInit(initCmd, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "[general]\n" {
		t.Error("plain init must not touch an existing file")
	}
}
