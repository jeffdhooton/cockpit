package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/doctor"
	"github.com/jeffdhooton/cockpit/setup"
	"github.com/jeffdhooton/cockpit/sources"
	"github.com/spf13/cobra"
)

var initInteractive bool

func init() {
	initCmd.Flags().BoolVar(&initInteractive, "interactive", false, "choose projects from running sessions or a directory scan, review the change, then save")
}

// runInit creates the minimal config, or hands off to the wizard.
func runInit(cmd *cobra.Command, args []string) error {
	if initInteractive {
		return runInitInteractive(cmd)
	}
	path := getConfigPath()
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Config already exists at %s.\n", path)
		fmt.Println("It was left unchanged. To add projects to it, run: cockpit init --interactive")
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(getConfigTemplate()), 0o644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("Config created at %s — edit it to add your repos, or run cockpit init --interactive.\n", path)
	return nil
}

// getConfigTemplate returns the config template string. This is injected
// from the main package via SetConfigTemplate; the minimal template is the
// fallback.
var getConfigTemplate = func() string { return setup.MinimalConfig(nil) }

func SetConfigTemplate(fn func() string) {
	getConfigTemplate = fn
}

// isTerminal reports whether f is an interactive terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// runInitInteractive is the guided setup. It requires a TTY; with redirected
// input it exits with usage guidance and writes nothing.
func runInitInteractive(cmd *cobra.Command) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return exitError{2, "cockpit init --interactive needs a terminal; run plain cockpit init to write the template, or run this in a terminal"}
	}
	w := newWizard(os.Stdin, os.Stdout, getConfigPath())
	return w.run(context.Background())
}

// wizard is the line-oriented guided setup. Every step reads; only the
// final Save writes, atomically, after a fresh fingerprint check.
type wizard struct {
	in   *bufio.Reader
	out  io.Writer
	path string
	deps setup.Deps
	now  func() time.Time

	candidates []setup.Candidate
	existing   []byte // the config as reviewed
	reviewed   string // its fingerprint
}

func newWizard(in io.Reader, out io.Writer, path string) *wizard {
	return &wizard{
		in:   bufio.NewReader(in),
		out:  out,
		path: config.ExpandTilde(path),
		deps: setup.SystemDeps(gitTop, paneDirs),
		now:  time.Now,
	}
}

func gitTop(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func paneDirs(ctx context.Context) ([]string, error) {
	out, err := sources.DefaultRunner().Run(ctx, "list-panes", "-a", "-F", "#{pane_current_path}")
	if err != nil {
		if sources.IsNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return strings.Split(strings.TrimSpace(out), "\n"), nil
}

func (w *wizard) say(format string, args ...any) { fmt.Fprintf(w.out, format+"\n", args...) }

func (w *wizard) ask(prompt string) (string, error) {
	fmt.Fprint(w.out, prompt)
	line, err := w.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (w *wizard) run(ctx context.Context) error {
	w.say("Cockpit guided setup")
	w.say("")
	w.say("Cockpit is a front door to the tmux sessions you already have. This")
	w.say("wizard writes a minimal config listing the projects you choose. GitHub,")
	w.say("the agent daemon, Obsidian, remote hosts and Hermes are optional and can")
	w.say("be enabled later by editing the file. Nothing is installed or started.")
	w.say("")

	// Step 1: core dependencies, reported only.
	if _, err := exec.LookPath("tmux"); err != nil {
		w.say("Note: tmux was not found on PATH. Cockpit needs it to run; install it")
		w.say("(brew install tmux / apt install tmux). Config can still be written.")
		w.say("")
	}

	// Read the existing file once; its fingerprint is checked again at save.
	raw, err := os.ReadFile(w.path)
	switch {
	case err == nil:
		w.existing = raw
		w.reviewed = setup.Fingerprint(raw)
		w.say("Existing config: %s (will be merged, never overwritten silently)", w.path)
	case errors.Is(err, os.ErrNotExist):
		w.say("Destination: %s (new file)", w.path)
	default:
		return fmt.Errorf("read %s: %w", w.path, err)
	}
	w.say("")

	// Step 2: sessions.
	found := setup.FromSessions(ctx, w.deps)
	if len(found) > 0 {
		w.say("Repositories behind running tmux sessions (unchecked):")
	} else {
		w.say("No repositories found behind running tmux sessions.")
	}
	w.candidates = found
	w.list()

	// Step 3: add / scan / select loop.
	for {
		w.say("")
		w.say("  [number]  toggle a candidate     a <path>   add a repository path")
		w.say("  s <dir>   scan a directory (2 levels, up to 200)     e  edit labels")
		w.say("  p         preview and save                          q  cancel")
		line, err := w.ask("> ")
		if err != nil {
			w.say("Cancelled; nothing written.")
			return nil
		}
		cmdWord, arg, _ := strings.Cut(line, " ")
		arg = strings.TrimSpace(arg)
		switch cmdWord {
		case "", "l":
			w.list()
		case "q":
			w.say("Cancelled; nothing written.")
			return nil
		case "a":
			if arg == "" {
				w.say("usage: a <path>")
				continue
			}
			c, err := setup.Explicit(ctx, w.deps, arg)
			if err != nil {
				w.say("cannot use %s: %v", arg, err)
				continue
			}
			w.candidates = setup.Dedupe(append(w.candidates, c))
			w.list()
		case "s":
			if arg == "" {
				w.say("usage: s <directory>")
				continue
			}
			scanned := setup.Scan(ctx, w.deps, arg)
			if len(scanned) == 0 {
				w.say("no repositories found under %s (2 levels deep)", arg)
				continue
			}
			w.candidates = setup.Dedupe(append(w.candidates, scanned...))
			w.list()
		case "e":
			w.editLabels()
		case "p":
			done, err := w.preview(ctx)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		default:
			if n, err := strconv.Atoi(cmdWord); err == nil && n >= 1 && n <= len(w.candidates) {
				w.candidates[n-1].Selected = !w.candidates[n-1].Selected
				w.list()
			} else {
				w.say("unknown command %q", cmdWord)
			}
		}
	}
}

func (w *wizard) list() {
	if len(w.candidates) == 0 {
		w.say("  (no candidates yet)")
		return
	}
	for i, c := range w.candidates {
		mark := " "
		if c.Selected {
			mark = "x"
		}
		note := ""
		if c.Worktree {
			note = "  (worktree)"
		}
		w.say("  %2d [%s] %-20s %s%s", i+1, mark, c.Label, config.CollapseTilde(c.Path), note)
	}
}

func (w *wizard) editLabels() {
	for i := range w.candidates {
		c := &w.candidates[i]
		if !c.Selected {
			continue
		}
		line, err := w.ask(fmt.Sprintf("label for %s [%s]: ", config.CollapseTilde(c.Path), c.Label))
		if err != nil {
			return
		}
		if line != "" {
			c.Label = line
		}
	}
}

func (w *wizard) entries() []setup.Entry {
	var out []setup.Entry
	for _, c := range w.candidates {
		if c.Selected {
			out = append(out, setup.Entry{Label: c.Label, Path: c.Path})
		}
	}
	return out
}

// preview shows the exact change and asks Save or Cancel. It returns done
// when the wizard should end.
func (w *wizard) preview(ctx context.Context) (bool, error) {
	entries := w.entries()
	sessionName := "cockpit"
	var existingLabels []string
	if w.existing != nil {
		existingLabels = setup.ExistingLabels(w.existing)
		if cfg, _, err := config.Parse(w.existing); err == nil {
			sessionName = cfg.General.SessionName
		}
	}
	if err := setup.ValidateEntries(entries, existingLabels, sessionName); err != nil {
		w.say("cannot save: %v (press e to edit labels)", err)
		return false, nil
	}

	var proposed []byte
	if w.existing == nil {
		proposed = []byte(setup.MinimalConfig(entries))
	} else {
		if len(entries) == 0 {
			w.say("nothing selected; the existing config would be unchanged. Nothing written.")
			return true, nil
		}
		merged, err := setup.Merge(w.existing, entries)
		if err != nil {
			w.say("The existing config cannot be merged safely (%v).", err)
			w.say("It was left unchanged. Add this snippet by hand:")
			w.say("%s", setup.Snippet(entries))
			return true, nil
		}
		proposed = merged
	}

	w.say("")
	w.say("Destination: %s", w.path)
	w.say("Change:")
	for _, l := range strings.Split(strings.TrimRight(setup.Diff(w.existing, proposed), "\n"), "\n") {
		w.say("  %s", l)
	}
	w.say("")
	answer, err := w.ask("Save this change? [s]ave / [c]ancel / [b]ack: ")
	if err != nil {
		w.say("Cancelled; nothing written.")
		return true, nil
	}
	switch strings.ToLower(answer) {
	case "s", "save", "y", "yes":
	case "b", "back":
		return false, nil
	default:
		w.say("Cancelled; nothing written.")
		return true, nil
	}

	backup, err := setup.WriteAtomic(w.path, proposed, w.reviewed, w.now())
	if errors.Is(err, setup.ErrChanged) {
		w.say("The config changed while you were reviewing. Reloading it for a fresh review; nothing was written.")
		raw, rerr := os.ReadFile(w.path)
		if rerr == nil {
			w.existing = raw
			w.reviewed = setup.Fingerprint(raw)
		} else {
			w.existing = nil
			w.reviewed = ""
		}
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("write %s: %w", w.path, err)
	}
	w.say("Saved %s", w.path)
	if backup != "" {
		w.say("Previous version kept at %s", backup)
	}

	// Step 7: diagnostics and next steps; nothing is started.
	w.say("")
	rep := doctor.Run(ctx, doctor.Options{ConfigPath: w.path}, doctor.SystemDeps(version))
	doctor.Render(w.out, rep)
	w.say("")
	w.say("Next steps (each is a separate command; none has been run):")
	w.say("  cockpit                 open the dashboard")
	w.say("  cockpit doctor          re-check this installation")
	w.say("  cockpit daemon start    serve the agent tool server (after enabling [daemon])")
	w.say("  cockpit hook install    report agent status from Claude Code / Codex")
	return true, nil
}
