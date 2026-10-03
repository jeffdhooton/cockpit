package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

// tree builds a directory layout under a temp dir. Names ending in "/.git"
// mark a checkout; a "@" suffix makes a symlink to the named target.
func tree(t *testing.T, layout map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, target := range layout {
		p := filepath.Join(root, name)
		switch {
		case strings.HasSuffix(name, "@"):
			p = strings.TrimSuffix(p, "@")
			if err := os.Symlink(filepath.Join(root, target), p); err != nil {
				t.Fatal(err)
			}
		case strings.HasSuffix(name, "/.git-file"):
			dir := filepath.Dir(p)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+target), 0o644); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// fakeGit treats any directory containing .git (file or dir) as a toplevel.
func fakeGit(ctx context.Context, dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		if d == filepath.Dir(d) || d == "/" || len(d) < 5 {
			return "", errors.New("not a git repository")
		}
	}
}

func deps(panes []string) Deps {
	return SystemDeps(fakeGit, func(context.Context) ([]string, error) { return panes, nil })
}

func TestScanIsBoundedAndSkipsSymlinkedDirs(t *testing.T) {
	root := tree(t, map[string]string{
		"a/.git":                "",
		"b/inner/.git":          "",
		"deep/x/y/.git":         "", // three levels: beyond MaxDepth
		"node_modules/pkg/.git": "",
		"loop@":                 ".", // symlink to root: must not recurse
		"linked@":               "a",
		"wt/.git-file":          "/main/.git/worktrees/wt",
	})
	got := Scan(context.Background(), deps(nil), root)
	labels := map[string]Candidate{}
	for _, c := range got {
		labels[c.Label] = c
	}
	if _, ok := labels["a"]; !ok {
		t.Errorf("a missing: %+v", got)
	}
	if _, ok := labels["inner"]; !ok {
		t.Errorf("second level missing: %+v", got)
	}
	if _, ok := labels["y"]; ok {
		t.Errorf("third level must not be scanned: %+v", got)
	}
	if _, ok := labels["pkg"]; ok {
		t.Errorf("node_modules must be skipped: %+v", got)
	}
	if c, ok := labels["wt"]; !ok || !c.Worktree {
		t.Errorf("a worktree (.git file) is a candidate: %+v", got)
	}
	for _, c := range got {
		rel, _ := filepath.Rel(root, c.Path)
		if strings.HasPrefix(rel, "linked") || strings.HasPrefix(rel, "loop") || strings.Contains(rel, "/loop/") {
			t.Errorf("symlinked directories must not be traversed: %+v", c)
		}
	}
	if len(got) > MaxCandidates {
		t.Errorf("%d candidates exceeds the bound", len(got))
	}
}

func TestScanStopsAtTheCandidateCap(t *testing.T) {
	layout := map[string]string{}
	for i := 0; i < MaxCandidates+20; i++ {
		layout[filepath.Join("r"+strings.Repeat("0", 3-len(itoa(i)))+itoa(i), ".git")] = ""
	}
	root := tree(t, layout)
	got := Scan(context.Background(), deps(nil), root)
	if len(got) != MaxCandidates {
		t.Errorf("want exactly %d candidates, got %d", MaxCandidates, len(got))
	}
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(i)) }
func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func TestExplicitResolvesSymlinksAndSessionsOfferGitRootsOnly(t *testing.T) {
	root := tree(t, map[string]string{"real/.git": "", "real/sub": "", "link@": "real", "plain": ""})
	real, _ := filepath.EvalSymlinks(filepath.Join(root, "real"))
	c, err := Explicit(context.Background(), deps(nil), filepath.Join(root, "link"))
	if err != nil || !c.Selected || c.Path != real {
		t.Errorf("explicit = %+v %v (want %s)", c, err, real)
	}
	found := FromSessions(context.Background(), deps([]string{filepath.Join(root, "real", "sub"), filepath.Join(root, "plain"), ""}))
	if len(found) != 1 || found[0].Path != filepath.Join(root, "real") || found[0].Selected {
		t.Errorf("sessions offer the git root, unchecked, and skip non-repos: %+v", found)
	}
}

func TestDedupeKeepsDistinctWorktrees(t *testing.T) {
	in := []Candidate{
		{Label: "a", Path: "/x", Root: "/x"},
		{Label: "a2", Path: "/link-to-x", Root: "/x"},
		{Label: "wt", Path: "/x-wt", Root: "/x-wt", Worktree: true},
	}
	got := Dedupe(in)
	if len(got) != 2 || got[0].Label != "a" || got[1].Label != "wt" {
		t.Errorf("got %+v", got)
	}
	// An explicit (selected) add of an already-offered root selects it.
	got = Dedupe([]Candidate{{Label: "a", Root: "/x"}, {Label: "a", Root: "/x", Selected: true, Source: "added"}})
	if len(got) != 1 || !got[0].Selected {
		t.Errorf("selection must survive dedupe: %+v", got)
	}
}

func TestValidateEntriesRejectsCollisions(t *testing.T) {
	cases := []struct {
		entries  []Entry
		existing []string
		wantErr  string
	}{
		{[]Entry{{Label: "cockpit", Path: "/p"}}, nil, "reserved"},
		{[]Entry{{Label: "a", Path: "/p"}, {Label: "a", Path: "/q"}}, nil, "already in use"},
		{[]Entry{{Label: "a", Path: "/p"}}, []string{"a"}, "already in use"},
		{[]Entry{{Label: "bad label", Path: "/p"}}, nil, "letters"},
		{[]Entry{{Label: "ok", Path: "/p"}}, []string{"other"}, ""},
	}
	for _, tc := range cases {
		err := ValidateEntries(tc.entries, tc.existing, "cockpit")
		if tc.wantErr == "" && err != nil {
			t.Errorf("%+v: unexpected %v", tc.entries, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%+v: want %q, got %v", tc.entries, tc.wantErr, err)
		}
	}
}

func TestMinimalConfigLoadsAndDisablesOptionalIntegrations(t *testing.T) {
	raw := MinimalConfig([]Entry{{Label: "site", Path: "/tmp/site"}})
	cfg, unknown, err := config.Parse([]byte(raw))
	if err != nil || len(unknown) != 0 {
		t.Fatalf("minimal config must load cleanly: %v %v", err, unknown)
	}
	if cfg.GitHub.Enabled || cfg.Daemon.IsEnabled() {
		t.Errorf("optional integrations must be off: github=%v daemon=%v", cfg.GitHub.Enabled, cfg.Daemon.IsEnabled())
	}
	if cfg.Obsidian.Enabled() || len(cfg.Hermes) != 0 || len(cfg.Hosts) != 0 {
		t.Errorf("no active example paths: %+v", cfg)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Label != "site" || len(cfg.Repos[0].Processes) != 0 {
		t.Errorf("repos = %+v", cfg.Repos)
	}
	empty, _, err := config.Parse([]byte(MinimalConfig(nil)))
	if err != nil || len(empty.Repos) != 0 {
		t.Errorf("empty minimal config: %v %+v", err, empty)
	}
}

func TestMergePreservesCommentsAndRefusesUnsafeResults(t *testing.T) {
	existing := []byte("# my notes\n[general]\nsession_name = \"hq\"\n\n[[repos]]\npath = \"/a\"\nlabel = \"a\"\n\n[github]\nenabled = true  # keep\n")
	merged, err := Merge(existing, []Entry{{Label: "b", Path: "/b"}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(merged)
	if !strings.Contains(out, "# my notes") || !strings.Contains(out, "enabled = true  # keep") {
		t.Errorf("comments lost:\n%s", out)
	}
	if strings.Index(out, "label = \"b\"") > strings.Index(out, "[github]") {
		t.Errorf("new entries must land before [github]:\n%s", out)
	}
	cfg, _, err := config.Parse(merged)
	if err != nil || len(cfg.Repos) != 2 || cfg.General.SessionName != "hq" {
		t.Errorf("merged config: %v %+v", err, cfg)
	}

	// A document that would not validate after the merge is refused.
	broken := []byte("[[repos]]\npath = \"/a\"\nlabel = \"a\"\n[[repos.processes]]\nname = \"dev\"\ncommand = \"x\"\n")
	if _, err := Merge(broken, []Entry{{Label: "b", Path: "/b"}}); err == nil {
		// Appending [[repos]] after a [[repos.processes]] table is
		// syntactically fine, so this one merges; make a truly unsafe one.
		t.Log("plain append merged as expected")
	}
	unsafe := []byte("[[repos]]\npath = \"/a\"\nlabel = \"a\"\n\n[unclosed\n")
	if _, err := Merge(unsafe, []Entry{{Label: "b", Path: "/b"}}); !errors.Is(err, ErrUnsafeMerge) {
		t.Errorf("want ErrUnsafeMerge, got %v", err)
	}
}

func TestWriteAtomicRefusesConcurrentEditsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reviewed := Fingerprint([]byte("original\n"))
	// Someone edits the file during the review.
	if err := os.WriteFile(path, []byte("edited elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAtomic(path, []byte("proposed\n"), reviewed, time.Now()); !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "edited elsewhere\n" {
		t.Errorf("the concurrent edit was lost: %q", got)
	}

	// Re-review and save.
	reviewed = Fingerprint(got)
	backup, err := WriteAtomic(path, []byte("proposed\n"), reviewed, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "proposed\n" {
		t.Errorf("content = %q", got)
	}
	bak, _ := os.ReadFile(backup)
	if string(bak) != "edited elsewhere\n" {
		t.Errorf("backup = %q", bak)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("temp files left behind: %v", entries)
	}

	// A new file: a reviewed empty fingerprint, and a file that appeared
	// meanwhile is a change.
	fresh := filepath.Join(dir, "sub", "new.toml")
	if _, err := WriteAtomic(fresh, []byte("x\n"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAtomic(fresh, []byte("y\n"), "", time.Now()); !errors.Is(err, ErrChanged) {
		t.Errorf("a file that appeared after review is a change: %v", err)
	}
}

func TestDiffShowsAdditionsOnly(t *testing.T) {
	d := Diff([]byte("a\nb\n"), []byte("a\nnew\nb\n"))
	if !strings.Contains(d, "+ new") || strings.Contains(d, "- ") {
		t.Errorf("diff = %q", d)
	}
}
