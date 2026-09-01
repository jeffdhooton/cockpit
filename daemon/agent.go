package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jhoot/cockpit/config"
	"github.com/jhoot/cockpit/sources"
)

// unsafeWindowChars is everything a tmux window name should not contain.
var unsafeWindowChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// settleOptions tunes how long to wait for a freshly-spawned agent to finish
// booting before typing at it.
type settleOptions struct {
	HeadStart time.Duration
	Poll      time.Duration
	Quiet     time.Duration
	Deadline  time.Duration
}

func defaultSettleOptions() settleOptions {
	return settleOptions{
		HeadStart: 150 * time.Millisecond,
		Poll:      100 * time.Millisecond,
		Quiet:     600 * time.Millisecond,
		Deadline:  10 * time.Second,
	}
}

// waitForSettle reports whether a pane looks ready for input: it has printed
// something and then gone quiet. Terminal programs render their prompt and
// wait, so a pause after output is the best signal available without a stream
// to watch.
func waitForSettle(ctx context.Context, r sources.Runner, target string, o settleOptions) bool {
	// A short head start so we do not sample before the spawn writes anything.
	time.Sleep(o.HeadStart)

	deadline := time.Now().Add(o.Deadline)
	var lastHash string
	var unchangedSince time.Time

	for time.Now().Before(deadline) {
		out, err := r.Run(ctx, sources.CapturePaneArgs(target, 0)...)
		if err != nil {
			return false
		}

		content := strings.TrimSpace(out)
		sum := sha256.Sum256([]byte(content))
		hash := hex.EncodeToString(sum[:])

		switch {
		case content == "":
			// Nothing printed yet — not booted, just empty.
			unchangedSince = time.Time{}
		case hash != lastHash:
			unchangedSince = time.Now()
		case !unchangedSince.IsZero() && time.Since(unchangedSince) >= o.Quiet:
			return true
		}
		lastHash = hash

		time.Sleep(o.Poll)
	}
	return false
}

// --- mutating tools ---

func (t *Tools) startProcess(ctx context.Context, args map[string]any) (any, error) {
	repo, p, err := t.process(args)
	if err != nil {
		return nil, err
	}

	windows, _ := sources.ListWindows(ctx, t.Runner, repo.Label)
	for _, w := range windows {
		if w.Name != p.Name {
			continue
		}
		if !w.Dead {
			return map[string]any{"project": repo.Label, "process": p.Name, "status": "already running"}, nil
		}
		if err := sources.RestartProcess(ctx, t.Runner, repo, p); err != nil {
			return nil, err
		}
		return map[string]any{"project": repo.Label, "process": p.Name, "status": "restarted"}, nil
	}

	if _, err := sources.EnsureSession(ctx, t.Runner, repo); err != nil {
		return nil, err
	}
	if err := sources.StartProcess(ctx, t.Runner, repo, p); err != nil {
		return nil, err
	}
	return map[string]any{"project": repo.Label, "process": p.Name, "status": "started"}, nil
}

func (t *Tools) stopProcess(ctx context.Context, args map[string]any) (any, error) {
	repo, p, err := t.process(args)
	if err != nil {
		return nil, err
	}
	if err := sources.StopProcess(ctx, t.Runner, repo.Label, p.Name); err != nil {
		return nil, err
	}
	return map[string]any{"project": repo.Label, "process": p.Name, "status": "stopped"}, nil
}

func (t *Tools) restartProcess(ctx context.Context, args map[string]any) (any, error) {
	repo, p, err := t.process(args)
	if err != nil {
		return nil, err
	}
	if err := sources.RestartProcess(ctx, t.Runner, repo, p); err != nil {
		return nil, err
	}
	return map[string]any{"project": repo.Label, "process": p.Name, "status": "restarted"}, nil
}

func (t *Tools) writeInput(ctx context.Context, args map[string]any) (any, error) {
	repo, window, err := t.window(ctx, args)
	if err != nil {
		return nil, err
	}
	input, ok := args["input"].(string)
	if !ok || input == "" {
		return nil, fmt.Errorf("input is required")
	}

	target := sources.Target(repo.Label, window)
	if _, err := t.Runner.Run(ctx, sources.SendKeysLiteralArgs(target, input)...); err != nil {
		return nil, err
	}

	submitted := argBool(args, "submit", true)
	if submitted {
		if _, err := t.Runner.Run(ctx, sources.SendKeysEnterArgs(target)...); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"project":   repo.Label,
		"process":   window,
		"submitted": submitted,
	}, nil
}

// spawnAgent launches a command in its own tmux window. Unlike a sub-task, the
// result outlives this call: the window stays in the session, the user can
// watch it, and later tool calls can read from and write to it.
func (t *Tools) spawnAgent(ctx context.Context, args map[string]any) (any, error) {
	command := argString(args, "command")
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	repo, err := t.agentTarget(args)
	if err != nil {
		return nil, err
	}

	name := sanitiseWindowName(argString(args, "name"))
	if name == "" {
		name = generatedAgentName()
	}

	if _, err := sources.EnsureSession(ctx, t.Runner, repo); err != nil {
		return nil, err
	}

	p := config.ProcessConfig{
		Name:       name,
		Command:    command,
		WorkingDir: argString(args, "working_dir"),
		Env:        argMap(args, "env"),
	}
	if err := sources.StartProcess(ctx, t.Runner, repo, p); err != nil {
		return nil, err
	}

	result := map[string]any{
		"project": repo.Label,
		"process": name,
		"target":  sources.Target(repo.Label, name),
		"command": command,
	}

	// Delivering the prompt takes seconds of waiting for the agent to boot.
	// Do it in the background so the caller gets the window handle now.
	if prompt := argString(args, "prompt"); prompt != "" {
		result["prompt_delivery"] = "pending"
		go t.deliverPrompt(sources.Target(repo.Label, name), prompt)
	}
	return result, nil
}

// deliverPrompt waits for a spawned agent to look ready, then types the prompt.
// If it never settles the prompt is sent anyway — a slightly early prompt beats
// no prompt at all.
func (t *Tools) deliverPrompt(target, prompt string) {
	ctx := context.Background()
	waitForSettle(ctx, t.Runner, target, defaultSettleOptions())
	if _, err := t.Runner.Run(ctx, sources.SendKeysLiteralArgs(target, prompt)...); err != nil {
		return
	}
	_, _ = t.Runner.Run(ctx, sources.SendKeysEnterArgs(target)...)
}

// agentTarget resolves where to spawn, defaulting to the first configured repo.
func (t *Tools) agentTarget(args map[string]any) (config.RepoConfig, error) {
	if label := argString(args, "project"); label != "" {
		repo, ok := t.Cfg.Repo(label)
		if !ok {
			return config.RepoConfig{}, fmt.Errorf("unknown project %q", label)
		}
		return repo, nil
	}
	if len(t.Cfg.Repos) == 0 {
		return config.RepoConfig{}, fmt.Errorf("no projects configured — add a [[repos]] entry or pass a project")
	}
	return t.Cfg.Repos[0], nil
}

func sanitiseWindowName(name string) string {
	return strings.Trim(unsafeWindowChars.ReplaceAllString(name, "-"), "-")
}

func generatedAgentName() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("agent-%d", time.Now().UnixNano()%10000)
	}
	return "agent-" + hex.EncodeToString(b[:])
}

// --- vault tools ---

func (t *Tools) capture(args map[string]any) (any, error) {
	text := argString(args, "text")
	if text == "" {
		return nil, fmt.Errorf("text is required")
	}
	if t.Cfg.Obsidian.InboxFile == "" {
		return nil, fmt.Errorf("no inbox_file configured")
	}
	if err := sources.AppendInbox(t.Cfg.Obsidian.InboxFile, text); err != nil {
		return nil, err
	}
	return map[string]any{"captured": text, "file": t.Cfg.Obsidian.InboxFile}, nil
}

func (t *Tools) tasks(args map[string]any) (any, error) {
	file := t.Cfg.Obsidian.TodayFile
	if file == "" {
		return nil, fmt.Errorf("no today_file configured")
	}

	if line := argInt(args, "toggle_line", 0); line > 0 {
		if err := sources.ToggleTask(file, line); err != nil {
			return nil, err
		}
	}

	tasks, err := sources.ReadTasks(file)
	if err != nil {
		return nil, err
	}

	type task struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
		Line int    `json:"line"`
	}
	out := make([]task, 0, len(tasks))
	for _, tk := range tasks {
		out = append(out, task{Text: tk.Text, Done: tk.Done, Line: tk.Line})
	}
	return map[string]any{"file": file, "tasks": out}, nil
}
