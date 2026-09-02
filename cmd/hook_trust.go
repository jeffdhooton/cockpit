package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Codex leaves a newly installed hook untrusted and does not run it until
// approved, and the approval is a TUI action. What that action actually does
// is two app-server calls: hooks/list, which returns each hook's current
// hash, and config/batchWrite, which records that hash under hooks.state.
// Making the same two calls here is what turns "install" into "installed".
//
// The hash is Codex's own digest of the normalised hook, read back from
// hooks/list rather than computed here, so it is always the value Codex will
// compare against.

// codexRunner starts `codex app-server` speaking JSON-RPC over stdio. It is a
// seam so tests can substitute a fake.
type codexRunner func(ctx context.Context) *exec.Cmd

func realCodex(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
}

// trustReport says what the trust pass did.
type trustReport struct {
	Trusted int // hooks trusted this run
	Already int // cockpit hooks that were already trusted
}

type listedHook struct {
	Key         string `json:"key"`
	Command     string `json:"command"`
	TrustStatus string `json:"trustStatus"`
	CurrentHash string `json:"currentHash"`
}

// trustCodexHooks trusts every cockpit hook Codex lists as untrusted, and
// nothing else. Trusting a hook someone else installed, on their behalf, is
// exactly the thing the trust step exists to prevent.
func trustCodexHooks(ctx context.Context, start codexRunner, stderr io.Writer) (trustReport, error) {
	var report trustReport

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := start(ctx)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return report, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return report, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return report, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()

	c := &rpcClient{in: stdin, out: bufio.NewReader(stdout)}
	if _, err := c.call("initialize", map[string]any{
		"clientInfo": map[string]any{"name": "cockpit", "title": "cockpit hook install", "version": version},
	}); err != nil {
		return report, err
	}
	c.notify("initialized")

	home, _ := os.UserHomeDir()
	hooks, err := c.listHooks(home)
	if err != nil {
		return report, err
	}

	var edits []map[string]any
	for _, h := range hooks {
		if !strings.Contains(h.Command, hookMarker) {
			continue
		}
		if h.TrustStatus == "trusted" {
			report.Already++
			continue
		}
		edits = append(edits, map[string]any{
			"keyPath":       fmt.Sprintf("hooks.state.%q", h.Key),
			"value":         map[string]any{"enabled": true, "trusted_hash": h.CurrentHash},
			"mergeStrategy": "upsert",
		})
	}
	if len(edits) == 0 {
		return report, nil
	}

	if _, err := c.call("config/batchWrite", map[string]any{"edits": edits, "reloadUserConfig": true}); err != nil {
		return report, fmt.Errorf("record trust: %w", err)
	}

	// Read back rather than assume: the write can succeed and the hash still
	// not match if the file changed underneath.
	after, err := c.listHooks(home)
	if err != nil {
		return report, err
	}
	for _, h := range after {
		if strings.Contains(h.Command, hookMarker) && h.TrustStatus == "trusted" {
			report.Trusted++
		}
	}
	report.Trusted -= report.Already
	return report, nil
}

// rpcClient is the minimum JSON-RPC-over-lines client the trust flow needs.
type rpcClient struct {
	in   io.Writer
	out  *bufio.Reader
	next int
}

func (c *rpcClient) notify(method string) {
	b, _ := json.Marshal(map[string]any{"method": method})
	_, _ = c.in.Write(append(b, '\n'))
}

func (c *rpcClient) call(method string, params any) (json.RawMessage, error) {
	c.next++
	id := c.next
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := c.out.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("%s: app-server closed: %w", method, err)
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil || *msg.ID != id {
			continue // a notification, or someone else's reply
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *rpcClient) listHooks(cwd string) ([]listedHook, error) {
	raw, err := c.call("hooks/list", map[string]any{"cwds": []string{cwd}})
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []struct {
			Hooks []listedHook `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("hooks/list: %w", err)
	}
	var out []listedHook
	for _, d := range result.Data {
		out = append(out, d.Hooks...)
	}
	return out, nil
}
