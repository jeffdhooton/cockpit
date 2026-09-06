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

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/process"
	"github.com/jeffdhooton/cockpit/sources"
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

// Every lifecycle call goes through the shared service: the same lock, the
// same identity re-read, the same stop-override rules the TUI applies. The
// legacy "status" field is kept beside the richer outcome.

func (t *Tools) startProcess(ctx context.Context, args map[string]any) (any, error) {
	repo, p, err := t.process(args)
	if err != nil {
		return nil, err
	}
	return lifecycleResult(t.Svc.Start(ctx, repo.Key(), p.Name))
}

func (t *Tools) stopProcess(ctx context.Context, args map[string]any) (any, error) {
	repo, p, err := t.process(args)
	if err != nil {
		return nil, err
	}
	return lifecycleResult(t.Svc.Stop(ctx, repo.Key(), p.Name))
}

func (t *Tools) restartProcess(ctx context.Context, args map[string]any) (any, error) {
	repo, p, err := t.process(args)
	if err != nil {
		return nil, err
	}
	return lifecycleResult(t.Svc.Restart(ctx, repo.Key(), p.Name))
}

// lifecycleResult renders a service result for the wire. Refused, failed
// and unknown outcomes are errors: a caller must never read them as done.
func lifecycleResult(res process.Result) (any, error) {
	if err := res.Err(); err != nil {
		return nil, err
	}
	legacy := map[process.Outcome]string{
		process.OutcomeStarted:        "started",
		process.OutcomeAlreadyRunning: "already running",
		process.OutcomeRestarted:      "restarted",
		process.OutcomeStopped:        "stopped",
		process.OutcomeNotRunning:     "not running",
		process.OutcomeAdopted:        "adopted",
	}
	out := map[string]any{
		"project": res.Project,
		"process": res.Process,
		"status":  legacy[res.Outcome],
		"outcome": res.Outcome,
		"message": res.Message,
	}
	if res.Observation != nil {
		for _, p := range res.Observation.Processes {
			if p.Name == res.Process && p.Configured {
				out["state"] = p.State
				out["desired_state"] = p.DesiredState
				out["display"] = p.Display
				out["window_id"] = p.WindowID
				if p.ExitCode != nil {
					out["exit_code"] = *p.ExitCode
				}
			}
		}
	}
	return out, nil
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

	r, err := t.Svc.RunnerFor(repo)
	if err != nil {
		return nil, err
	}
	if _, err := r.Run(ctx, sources.SendKeysLiteralArgs(window.target, input)...); err != nil {
		return nil, err
	}

	submitted := argBool(args, "submit", true)
	if submitted {
		if _, err := r.Run(ctx, sources.SendKeysEnterArgs(window.target)...); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"project":   repo.Key(),
		"process":   window.name,
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

	p := config.ProcessConfig{
		Name:       name,
		Command:    command,
		WorkingDir: argString(args, "working_dir"),
		Env:        argMap(args, "env"),
	}
	r, err := t.Svc.Spawn(ctx, repo, p)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"project": repo.Label,
		"process": name,
		"target":  sources.Target(repo.Label, name),
		"command": command,
	}

	// Delivery is synchronous. "pending" would be a receipt, not an answer, and
	// the caller has no second place to look up how it turned out.
	if prompt := argString(args, "prompt"); prompt != "" {
		result["prompt_delivery"] = t.deliverPrompt(ctx, r, repo.Label, name, prompt)
	}
	return result, nil
}

// deliverPrompt waits for a spawned agent to look ready, checks it is still
// alive, then types the prompt. It returns a terminal outcome describing what
// actually happened.
//
// The liveness check is the point. A command that does not exist dies
// instantly, remain-on-exit keeps the corpse, and a corpse settles perfectly —
// so without it the prompt gets typed into a dead pane and silently lost.
func (t *Tools) deliverPrompt(ctx context.Context, r sources.Runner, session, window, prompt string) string {
	target := sources.Target(session, window)
	waitForSettle(ctx, r, target, t.Settle)

	windows, err := sources.ListWindows(ctx, r, session)
	if err != nil {
		return "not delivered: could not check whether the process was still running: " + err.Error()
	}

	var found *sources.Window
	for i := range windows {
		if windows[i].Name == window {
			found = &windows[i]
			break
		}
	}
	switch {
	case found == nil:
		return "not delivered: the window disappeared before the prompt could be sent"
	case found.Dead:
		return fmt.Sprintf("not delivered: the process exited with status %d before the prompt could be sent", found.DeadStatus)
	}

	if _, err := r.Run(ctx, sources.SendKeysLiteralArgs(target, prompt)...); err != nil {
		return "not delivered: " + err.Error()
	}
	if _, err := r.Run(ctx, sources.SendKeysEnterArgs(target)...); err != nil {
		return "not delivered: typed but could not submit: " + err.Error()
	}
	return "delivered"
}

// agentTarget resolves where to spawn, defaulting to the first configured repo.
func (t *Tools) agentTarget(args map[string]any) (config.RepoConfig, error) {
	if key := projectKey(args); key != "" {
		repo, _, err := t.Svc.Resolve(key)
		if err != nil {
			return config.RepoConfig{}, fmt.Errorf("unknown project %q", key)
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
	// Captures land in today_file, the same place `cockpit cap` and the TUI's
	// c key put them.
	file := t.Cfg.Obsidian.TodayFile
	if file == "" {
		return nil, fmt.Errorf("no today_file configured")
	}
	if err := sources.AppendInbox(file, text); err != nil {
		return nil, err
	}
	return map[string]any{"captured": text, "file": file}, nil
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
