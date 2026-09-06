// Package setup is the guided initial configuration: discovering candidate
// repositories, editing labels, previewing the config change, and writing
// it safely. It is explicitly chosen (cockpit init --interactive) and
// installs, trusts, registers and starts nothing.
package setup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jeffdhooton/cockpit/config"
)

// Discovery bounds, from the spec.
const (
	MaxDepth      = 2
	MaxCandidates = 200
)

// skipDirs are dependency and cache directories never descended into.
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true, "target": true, ".cache": true,
	"dist": true, "build": true, ".venv": true, "venv": true, "__pycache__": true,
	".next": true, ".turbo": true, ".idea": true, ".vscode": true, "Library": true,
	".Trash": true, "Applications": true,
}

// Candidate is a discovered git root.
type Candidate struct {
	Label    string
	Path     string // as it will be written (symlinks resolved when explicit)
	Root     string // canonical git toplevel, for dedup
	Source   string // "session" or "scan" or "added"
	Worktree bool
	Selected bool
}

// Deps are the injectable edges for discovery.
type Deps struct {
	// GitTop returns the git toplevel for a directory, or "" and an error
	// when it is not inside a checkout. Read-only.
	GitTop func(ctx context.Context, dir string) (string, error)
	// PaneDirs lists the current directory of every pane on the local
	// tmux server; no server means none.
	PaneDirs     func(ctx context.Context) ([]string, error)
	ReadDir      func(string) ([]os.DirEntry, error)
	Stat         func(string) (os.FileInfo, error)
	Lstat        func(string) (os.FileInfo, error)
	EvalSymlinks func(string) (string, error)
}

// SystemDeps are the real edges.
func SystemDeps(git func(ctx context.Context, dir string) (string, error), panes func(ctx context.Context) ([]string, error)) Deps {
	return Deps{
		GitTop:       git,
		PaneDirs:     panes,
		ReadDir:      os.ReadDir,
		Stat:         os.Stat,
		Lstat:        os.Lstat,
		EvalSymlinks: filepath.EvalSymlinks,
	}
}

// FromSessions offers the git roots of running panes' directories as
// unchecked candidates. Not every pane directory is a repository; only
// those git recognises are offered.
func FromSessions(ctx context.Context, d Deps) []Candidate {
	dirs, err := d.PaneDirs(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Candidate
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		top, err := d.GitTop(ctx, dir)
		if err != nil || top == "" {
			continue
		}
		out = append(out, candidate(ctx, d, top, "session"))
	}
	return Dedupe(out)
}

// Scan walks a parent directory at most MaxDepth levels deep, offering at
// most MaxCandidates git roots. Symlinked directories are not traversed, so
// a loop cannot recurse and a link farm cannot escape the bound; the parent
// itself is resolved because the user chose it.
func Scan(ctx context.Context, d Deps, parent string) []Candidate {
	resolved, err := d.EvalSymlinks(config.ExpandTilde(parent))
	if err != nil {
		return nil
	}
	var out []Candidate
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if len(out) >= MaxCandidates || ctx.Err() != nil {
			return
		}
		if top, err := d.GitTop(ctx, dir); err == nil && top != "" {
			// A checkout: offer it and do not descend into it. Nested
			// repositories are unusual and a scan is not a crawl.
			out = append(out, candidate(ctx, d, top, "scan"))
			return
		}
		if depth >= MaxDepth {
			return
		}
		entries, err := d.ReadDir(dir)
		if err != nil {
			return
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			if len(out) >= MaxCandidates {
				return
			}
			if strings.HasPrefix(name, ".") && name != ".git" || skipDirs[name] {
				continue
			}
			child := filepath.Join(dir, name)
			info, err := d.Lstat(child)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue
			}
			walk(child, depth+1)
		}
	}
	walk(resolved, 0)
	return Dedupe(out)
}

// Explicit resolves one user-supplied path: symlinks followed, git root
// found, offered selected.
func Explicit(ctx context.Context, d Deps, path string) (Candidate, error) {
	resolved, err := d.EvalSymlinks(config.ExpandTilde(path))
	if err != nil {
		return Candidate{}, err
	}
	top, err := d.GitTop(ctx, resolved)
	if err != nil || top == "" {
		// Not a checkout: still allowed, as a plain directory the user
		// asked for, labelled by its name.
		info, serr := d.Stat(resolved)
		if serr != nil || !info.IsDir() {
			return Candidate{}, os.ErrNotExist
		}
		c := Candidate{Label: labelFor(resolved), Path: resolved, Root: resolved, Source: "added", Selected: true}
		return c, nil
	}
	c := candidate(ctx, d, top, "added")
	c.Selected = true
	return c, nil
}

func candidate(ctx context.Context, d Deps, top, source string) Candidate {
	c := Candidate{Label: labelFor(top), Path: top, Root: top, Source: source}
	if real, err := d.EvalSymlinks(top); err == nil {
		c.Root = real
	}
	// A worktree's .git is a file, not a directory.
	if info, err := d.Lstat(filepath.Join(top, ".git")); err == nil && !info.IsDir() {
		c.Worktree = true
	}
	return c
}

// labelFor derives a label from the directory name, in the label grammar.
func labelFor(path string) string {
	base := filepath.Base(path)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "project"
	}
	return out
}

// Dedupe keeps one candidate per canonical root. Distinct worktrees have
// distinct roots and are all kept. A duplicate that was selected — an
// explicit add of a path already offered by a scan — selects the kept one,
// so the user's choice is never dropped with the duplicate.
func Dedupe(in []Candidate) []Candidate {
	index := map[string]int{}
	var out []Candidate
	for _, c := range in {
		key := c.Root
		if key == "" {
			key = c.Path
		}
		if i, ok := index[key]; ok {
			if c.Selected {
				out[i].Selected = true
			}
			continue
		}
		index[key] = len(out)
		out = append(out, c)
	}
	return out
}
