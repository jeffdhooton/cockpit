package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestHelperProcess is a fake `codex app-server`. It speaks just enough
// JSON-RPC over stdio to exercise the trust flow: it answers initialize,
// lists whatever hooks FAKE_HOOKS describes, and records batchWrite edits.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	var hooks []map[string]any
	_ = json.Unmarshal([]byte(os.Getenv("FAKE_HOOKS")), &hooks)

	in := bufio.NewScanner(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for in.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"ok": true}
		case "hooks/list":
			result = map[string]any{"data": []any{map[string]any{"cwd": "/", "hooks": hooks}}}
		case "config/batchWrite":
			// Echo the edits to stderr so the test can see what was written,
			// and flip every listed hook to trusted for the re-list.
			fmt.Fprintf(os.Stderr, "BATCHWRITE %s\n", req.Params)
			for _, h := range hooks {
				h["trustStatus"] = "trusted"
			}
			result = map[string]any{"status": "ok"}
		default:
			result = map[string]any{}
		}
		b, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
}

func fakeCodex(t *testing.T, hooks string) codexRunner {
	t.Helper()
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "FAKE_HOOKS="+hooks)
		return cmd
	}
}

const untrustedHooks = `[
 {"key":"/h/.codex/config.toml:stop:0:0","eventName":"stop","command":"/usr/local/bin/cockpit hook status --engine codex","trustStatus":"untrusted","currentHash":"sha256:aaa"},
 {"key":"/h/.codex/config.toml:pre_tool_use:0:0","eventName":"preToolUse","command":"/usr/local/bin/cockpit hook status --engine codex","trustStatus":"untrusted","currentHash":"sha256:bbb"},
 {"key":"/h/.codex/config.toml:pre_tool_use:1:0","eventName":"preToolUse","command":"python3 /tmp/someone-elses-hook.py","trustStatus":"untrusted","currentHash":"sha256:ccc"}
]`

func TestTrustCodexHooksTrustsOnlyCockpitHooks(t *testing.T) {
	var stderr strings.Builder
	report, err := trustCodexHooks(context.Background(), fakeCodex(t, untrustedHooks), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trusted != 2 {
		t.Errorf("want 2 hooks trusted, got %+v", report)
	}
	writes := stderr.String()
	if !strings.Contains(writes, "sha256:aaa") || !strings.Contains(writes, "sha256:bbb") {
		t.Errorf("both cockpit hashes must be written: %s", writes)
	}
	if strings.Contains(writes, "sha256:ccc") {
		t.Error("someone else's hook must never be trusted on their behalf")
	}
	if !strings.Contains(writes, `"mergeStrategy":"upsert"`) {
		t.Errorf("hooks.state is a nested table and needs upsert: %s", writes)
	}
}

func TestTrustCodexHooksIsIdempotent(t *testing.T) {
	trusted := strings.ReplaceAll(untrustedHooks, `"untrusted"`, `"trusted"`)
	var stderr strings.Builder
	report, err := trustCodexHooks(context.Background(), fakeCodex(t, trusted), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trusted != 0 || report.Already != 2 {
		t.Errorf("already-trusted hooks are reported, not rewritten: %+v", report)
	}
	if strings.Contains(stderr.String(), "BATCHWRITE") {
		t.Error("nothing should be written when everything is already trusted")
	}
}

func TestTrustCodexHooksReportsAMissingCodex(t *testing.T) {
	missing := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "/nonexistent/codex") }
	if _, err := trustCodexHooks(context.Background(), missing, &strings.Builder{}); err == nil {
		t.Error("a codex that cannot start must be reported, not silently skipped")
	}
}
