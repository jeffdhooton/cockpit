package buildctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testdataDir returns the absolute path to this package's fixture directory.
func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Join(filepath.Dir(file), "testdata")
}

// writeFake installs a fake buildctl executable with the given shell body.
// The body runs with "$@" set to the invocation arguments. The directory
// already contains an argv.log that the helper records every call into.
func writeFake(t *testing.T, body string) (cmdPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	cmdPath = filepath.Join(dir, "buildctl")
	script := "#!/bin/sh\n" +
		"DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
		"printf '%s\\n' \"$@\" >> \"$DIR/argv.log\"\n" +
		body + "\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return cmdPath, dir
}

// readArgv returns the recorded argv elements, one invocation per line group,
// joined by '\x1f' so assertions can compare exact argument boundaries.
func readArgv(t *testing.T, dir string) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "argv.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// Each invocation wrote one line per argument; we cannot separate
	// invocations from the log alone, so callers assert on the flat list.
	return [][]string{lines}
}

// serveFixture returns a script body that cats a fixture and exits 0.
func serveFixture(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf("cat %q", filepath.Join(testdataDir(t), name))
}

func TestVersionOK(t *testing.T) {
	cmd, _ := writeFake(t, serveFixture(t, "valid_version.json"))
	c := &Client{Command: cmd}
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v.CLIVersion != "0.1.0" || !v.ServerAvailable || !v.SupportsV1() {
		t.Errorf("unexpected version info: %+v", v)
	}
}

func TestListSessionsOK(t *testing.T) {
	cmd, dir := writeFake(t, serveFixture(t, "valid_session_list.json"))
	c := &Client{Command: cmd}
	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	live := sessions[0]
	if live.RunID == nil || *live.RunID != "run-uuid" {
		t.Errorf("live session run_id = %v, want run-uuid", live.RunID)
	}
	if !live.Attachable || live.Resumable || live.Status != "idle" {
		t.Errorf("unexpected flags on live session: %+v", live)
	}
	dead := sessions[1]
	if dead.RunID != nil {
		t.Errorf("exited session run_id = %v, want nil", dead.RunID)
	}
	if dead.Attachable || !dead.Resumable {
		t.Errorf("unexpected flags on exited session: %+v", dead)
	}

	argv := readArgv(t, dir)
	want := []string{"session", "list", "--json"}
	if strings.Join(argv[0], " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", argv[0], want)
	}
}

func TestListSessionsForwardCompatible(t *testing.T) {
	cmd, _ := writeFake(t, serveFixture(t, "session_list_forward_compat.json"))
	c := &Client{Command: cmd}
	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("unknown fields must be ignored, got: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Status != "working" {
		t.Errorf("unexpected sessions: %+v", sessions)
	}
}

func TestListProjectsOK(t *testing.T) {
	cmd, _ := writeFake(t, serveFixture(t, "valid_project_list.json"))
	c := &Client{Command: cmd}
	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].Label != "program-health" || !projects[1].Archived {
		t.Errorf("unexpected projects: %+v", projects)
	}
}

// TestMalformedResponses proves every structurally bad response is rejected
// as a whole — no partial trust.
func TestMalformedResponses(t *testing.T) {
	cases := map[string]struct {
		body string
		want error
	}{
		"empty stdout":         {`exit 0`, ErrMalformed},
		"not json":             {`echo 'not json'`, ErrMalformed},
		"truncated json":       {`printf '{"schema_version": 1, "ok": tr'`, ErrMalformed},
		"trailing garbage":     {`printf '{"schema_version":1,"ok":true,"data":{"sessions":[]}} EXTRA'`, ErrMalformed},
		"second json object":   {`printf '{"schema_version":1,"ok":true,"data":{"sessions":[]}}\n{"schema_version":1,"ok":true,"data":{"sessions":[]}}'`, ErrMalformed},
		"missing schema":       {`printf '{"ok":true,"data":{"sessions":[]}}'`, ErrUnsupportedSchema},
		"schema version 2":     {`printf '{"schema_version":2,"ok":true,"data":{"sessions":[]}}'`, ErrUnsupportedSchema},
		"schema as string":     {`printf '{"schema_version":"1","ok":true,"data":{"sessions":[]}}'`, ErrMalformed},
		"missing ok":           {`printf '{"schema_version":1,"data":{"sessions":[]}}'`, ErrMalformed},
		"missing data":         {`printf '{"schema_version":1,"ok":true}'`, ErrMalformed},
		"null data":            {`printf '{"schema_version":1,"ok":true,"data":null}'`, ErrMalformed},
		"failure missing code": {`printf '{"schema_version":1,"ok":false,"error":{"message":"x"}}'`, ErrMalformed},
		"wrong field type": {
			`printf '{"schema_version":1,"ok":true,"data":{"sessions":[{"conversation_id":"c","run_id":null,"project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"idle","live":true,"attachable":"yes","resumable":false,"updated_at":"2026-08-27T21:00:00Z"}]}}'`,
			ErrMalformed,
		},
		"unknown status enum": {
			`printf '{"schema_version":1,"ok":true,"data":{"sessions":[{"conversation_id":"c","run_id":null,"project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"sleeping","live":false,"attachable":false,"resumable":true,"updated_at":"2026-08-27T21:00:00Z"}]}}'`,
			ErrMalformed,
		},
		"empty conversation id": {
			`printf '{"schema_version":1,"ok":true,"data":{"sessions":[{"conversation_id":"","run_id":null,"project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"idle","live":false,"attachable":false,"resumable":true,"updated_at":"2026-08-27T21:00:00Z"}]}}'`,
			ErrMalformed,
		},
		"missing updated_at": {
			`printf '{"schema_version":1,"ok":true,"data":{"sessions":[{"conversation_id":"c","run_id":null,"project_id":"p","project_label":"l","title":"t","agent":"codex","host_id":"h","host_label":"L","host_kind":"local","status":"idle","live":false,"attachable":false,"resumable":true,"updated_at":"0001-01-01T00:00:00Z"}]}}'`,
			ErrMalformed,
		},
		"ok with nonzero exit": {
			`printf '{"schema_version":1,"ok":true,"data":{"sessions":[]}}'; exit 3`,
			ErrMalformed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, _ := writeFake(t, tc.body)
			c := &Client{Command: cmd}
			sessions, err := c.ListSessions(context.Background())
			if err == nil {
				t.Fatalf("expected error, got sessions %+v", sessions)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want errors.Is %v", err, tc.want)
			}
			if sessions != nil {
				t.Errorf("partial data leaked on rejection: %+v", sessions)
			}
		})
	}
}

func TestErrorEnvelopeUnavailable(t *testing.T) {
	cmd, _ := writeFake(t, serveFixture(t, "error_unavailable.json")+"; exit 3")
	c := &Client{Command: cmd}
	_, err := c.ListSessions(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	var berr *Error
	if errors.As(err, &berr) {
		if berr.Code != "build_unavailable" || !berr.Retryable || berr.ExitCode != 3 {
			t.Errorf("unexpected error detail: %+v", berr)
		}
	} else {
		t.Fatal("error is not *Error")
	}
}

func TestErrorEnvelopeStaleRun(t *testing.T) {
	cmd, _ := writeFake(t, serveFixture(t, "error_stale_run.json")+"; exit 4")
	c := &Client{Command: cmd}
	_, err := c.Resume(context.Background(), "conversation-uuid-2", "standard")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	var berr *Error
	if errors.As(err, &berr) && berr.Code != "stale_run" {
		t.Errorf("code = %q, want stale_run", berr.Code)
	}
}

// TestExitCodeMapping covers failures with no parseable envelope.
func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		exit int
		want error
	}{
		{2, ErrInvalidRequest},
		{3, ErrUnavailable},
		{4, ErrNotFound},
		{5, ErrConflict},
		{10, ErrInternal},
		{99, ErrInternal},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("exit %d", tc.exit), func(t *testing.T) {
			cmd, _ := writeFake(t, fmt.Sprintf("echo 'human diagnostic' >&2; exit %d", tc.exit))
			c := &Client{Command: cmd}
			_, err := c.ListSessions(context.Background())
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want errors.Is %v", err, tc.want)
			}
		})
	}
}

// TestContractErrorCodeMapping covers every stable v1 error code delivered in
// a well-formed failure envelope.
func TestContractErrorCodeMapping(t *testing.T) {
	cases := map[string]error{
		"invalid_request":   ErrInvalidRequest,
		"unsupported_agent": ErrInvalidRequest,
		"build_unavailable": ErrUnavailable,
		"not_found":         ErrNotFound,
		"stale_run":         ErrNotFound,
		"already_active":    ErrConflict,
		"not_attachable":    ErrConflict,
		"not_resumable":     ErrConflict,
		"permission_denied": ErrPermissionDenied,
		"internal_error":    ErrInternal,
		"future_code":       ErrInternal, // well-formed but newer than v1
	}
	for code, want := range cases {
		t.Run(code, func(t *testing.T) {
			body := fmt.Sprintf(`printf '{"schema_version":1,"ok":false,"error":{"code":%q,"message":"m","retryable":false}}'; exit 1`, code)
			cmd, _ := writeFake(t, body)
			c := &Client{Command: cmd}
			_, err := c.ListSessions(context.Background())
			if !errors.Is(err, want) {
				t.Errorf("code %q: error = %v, want errors.Is %v", code, err, want)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	cmd, _ := writeFake(t, "sleep 30")
	c := &Client{Command: cmd, Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := c.ListSessions(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout did not kill the child promptly")
	}
}

func TestCancellation(t *testing.T) {
	cmd, _ := writeFake(t, "sleep 30")
	c := &Client{Command: cmd}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := c.ListSessions(ctx)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("error = %v, want ErrCanceled", err)
	}
}

func TestMissingExecutable(t *testing.T) {
	c := &Client{Command: filepath.Join(t.TempDir(), "does-not-exist")}
	_, err := c.ListSessions(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestLaunchArgvConstruction(t *testing.T) {
	cmd, dir := writeFake(t, serveFixture(t, "launch_response.json"))
	c := &Client{Command: cmd}

	// The prompt contains shell metacharacters and spaces; it must travel as
	// exactly one argv element and come back unevaluated.
	prompt := "Investigate $(rm -rf /) && `whoami`; the failing test"
	s, err := c.Launch(context.Background(), LaunchOptions{
		ProjectID:  "project-uuid",
		Agent:      "codex",
		Permission: "dangerous",
		Prompt:     prompt,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if s.ConversationID != "conversation-new" {
		t.Errorf("conversation = %q, want conversation-new", s.ConversationID)
	}

	argv := readArgv(t, dir)[0]
	want := []string{
		"session", "launch",
		"--project-id", "project-uuid",
		"--agent", "codex",
		"--permission", "dangerous",
		"--prompt", prompt,
		"--json",
	}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %q)", i, argv[i], want[i], argv)
		}
	}
}

func TestLaunchDefaultsAndValidation(t *testing.T) {
	cmd, dir := writeFake(t, serveFixture(t, "launch_response.json"))
	c := &Client{Command: cmd}

	if _, err := c.Launch(context.Background(), LaunchOptions{ProjectID: "p", Agent: "claude"}); err != nil {
		t.Fatalf("Launch with defaults: %v", err)
	}
	argv := readArgv(t, dir)[0]
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--permission standard") {
		t.Errorf("default permission missing from argv: %q", argv)
	}
	if strings.Contains(joined, "--prompt") {
		t.Errorf("empty prompt should omit the flag: %q", argv)
	}

	invalid := []LaunchOptions{
		{ProjectID: "", Agent: "codex"},
		{ProjectID: "p", Agent: "gpt"},
		{ProjectID: "p", Agent: "codex", Permission: "yolo"},
	}
	for i, opts := range invalid {
		if _, err := c.Launch(context.Background(), opts); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("case %d: error = %v, want ErrInvalidRequest", i, err)
		}
	}
}

func TestResumeArgvConstruction(t *testing.T) {
	cmd, dir := writeFake(t, serveFixture(t, "launch_response.json"))
	c := &Client{Command: cmd}
	if _, err := c.Resume(context.Background(), "conversation-uuid-2", ""); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	argv := readArgv(t, dir)[0]
	want := []string{"session", "resume", "--conversation-id", "conversation-uuid-2", "--permission", "standard", "--json"}
	if strings.Join(argv, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("argv = %q, want %q", argv, want)
	}

	if _, err := c.Resume(context.Background(), "", "standard"); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty conversation: error = %v, want ErrInvalidRequest", err)
	}
}

func TestAttachCommand(t *testing.T) {
	cmd, dir := writeFake(t, "exit 0")
	c := &Client{Command: cmd}

	ecmd, err := c.AttachCommand(context.Background(), "run-uuid")
	if err != nil {
		t.Fatalf("AttachCommand: %v", err)
	}
	// Interactive attach: no --json flag, exact argv, no shell.
	argv := ecmd.Args
	want := []string{cmd, "session", "attach", "--run-id", "run-uuid"}
	if strings.Join(argv, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("attach argv = %q, want %q", argv, want)
	}

	if err := ecmd.Run(); err != nil {
		t.Fatalf("attach child failed: %v", err)
	}
	logged := readArgv(t, dir)[0]
	if strings.Join(logged, "\x1f") != "session\x1fattach\x1f--run-id\x1frun-uuid" {
		t.Errorf("fake recorded argv = %q", logged)
	}

	if _, err := c.AttachCommand(context.Background(), ""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty run id: error = %v, want ErrInvalidRequest", err)
	}
}

func TestResolveCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// 3. ~/.build/bin/buildctl fallback
	buildBin := filepath.Join(home, ".build", "bin")
	if err := os.MkdirAll(buildBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(buildBin, "buildctl")
	if err := os.WriteFile(fallback, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing on PATH: restrict PATH to an empty dir.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	got, err := ResolveCommand("")
	if err != nil || got != fallback {
		t.Errorf("fallback resolution = %q, %v; want %q", got, err, fallback)
	}

	// 2. PATH beats the home fallback.
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, "buildctl")
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	got, err = ResolveCommand("")
	if err != nil || got != onPath {
		t.Errorf("PATH resolution = %q, %v; want %q", got, err, onPath)
	}

	// 1. Explicit configuration beats everything.
	configured := filepath.Join(t.TempDir(), "custom-buildctl")
	if err := os.WriteFile(configured, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveCommand(configured)
	if err != nil || got != configured {
		t.Errorf("configured resolution = %q, %v; want %q", got, err, configured)
	}

	// Configured but not executable → unavailable, never a silent fallback.
	nonExec := filepath.Join(t.TempDir(), "not-exec")
	if err := os.WriteFile(nonExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCommand(nonExec); !errors.Is(err, ErrUnavailable) {
		t.Errorf("non-executable configured command: err = %v, want ErrUnavailable", err)
	}

	// Tilde expansion in the configured command.
	tildeCmd := filepath.Join(home, "bin", "my-buildctl")
	if err := os.MkdirAll(filepath.Dir(tildeCmd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tildeCmd, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveCommand("~/bin/my-buildctl")
	if err != nil || got != tildeCmd {
		t.Errorf("tilde resolution = %q, %v; want %q", got, err, tildeCmd)
	}
}

func TestResolveCommandMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveCommand("")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestBoundedOutput(t *testing.T) {
	// Emit more than the 4 MiB bound; the client must fail rather than
	// buffer unbounded output.
	cmd, _ := writeFake(t, "dd if=/dev/zero bs=1024 count=5000 2>/dev/null | tr '\\0' 'x'")
	c := &Client{Command: cmd}
	_, err := c.ListSessions(context.Background())
	if err == nil {
		t.Fatal("expected error for unbounded output")
	}
}
