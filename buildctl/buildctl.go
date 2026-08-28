// Package buildctl is Cockpit's client for the frozen Build session-control
// contract v1 (docs/contracts/session-control-v1.md in the JBuild repo).
//
// Cockpit talks only to the public `buildctl` executable. This package never
// opens Build's private control socket, never reads Build's SQLite registry,
// and never infers Build state from tmux or processes. All values cross the
// boundary as argv elements and JSON data — never as shell fragments.
package buildctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SchemaVersion is the only contract version this client speaks.
const SchemaVersion = 1

// maxEnvelopeSize bounds a single JSON response read from buildctl.
// The contract requires bounded request/response sizes; 4 MiB is generous
// for session/project listings.
const maxEnvelopeSize = 4 << 20

// DefaultTimeout bounds non-interactive buildctl invocations.
const DefaultTimeout = 10 * time.Second

// waitDelay bounds how long cmd.Wait blocks on pipe EOF after the child
// exits or is killed, so a grandchild inheriting the pipes cannot wedge the
// client. Kept short: buildctl writes one bounded object and exits.
const waitDelay = 3 * time.Second

// Kind classifies a client failure so the TUI can degrade visibly without
// parsing message strings.
type Kind int

const (
	KindMalformed         Kind = iota // response was not one valid v1 envelope
	KindUnsupportedSchema             // schema_version missing or != 1
	KindUnavailable                   // buildctl missing/spawn failed/Build down
	KindInvalidRequest                // invalid command, arguments, or agent
	KindNotFound                      // project/conversation/run not found or stale
	KindConflict                      // operation not currently allowed
	KindPermissionDenied              // human/admin authority refused
	KindInternal                      // unexpected Build-side failure
	KindTimeout                       // context deadline exceeded
	KindCanceled                      // caller context canceled
)

// Sentinel errors for errors.Is checks against *Error.Kind.
var (
	ErrMalformed         = errors.New("buildctl: malformed response")
	ErrUnsupportedSchema = errors.New("buildctl: unsupported schema version")
	ErrUnavailable       = errors.New("buildctl: build unavailable")
	ErrInvalidRequest    = errors.New("buildctl: invalid request")
	ErrNotFound          = errors.New("buildctl: not found or stale")
	ErrConflict          = errors.New("buildctl: conflict")
	ErrPermissionDenied  = errors.New("buildctl: permission denied")
	ErrInternal          = errors.New("buildctl: internal error")
	ErrTimeout           = errors.New("buildctl: timed out")
	ErrCanceled          = errors.New("buildctl: canceled")
)

var kindSentinel = map[Kind]error{
	KindMalformed:         ErrMalformed,
	KindUnsupportedSchema: ErrUnsupportedSchema,
	KindUnavailable:       ErrUnavailable,
	KindInvalidRequest:    ErrInvalidRequest,
	KindNotFound:          ErrNotFound,
	KindConflict:          ErrConflict,
	KindPermissionDenied:  ErrPermissionDenied,
	KindInternal:          ErrInternal,
	KindTimeout:           ErrTimeout,
	KindCanceled:          ErrCanceled,
}

// Error is the single error type returned by this package.
type Error struct {
	Kind      Kind
	Code      string // stable v1 contract error code, when Build supplied one
	Message   string
	Retryable bool
	ExitCode  int // buildctl process exit code, when it ran; -1 otherwise
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("buildctl: %s: %s", e.Code, e.Message)
	}
	return "buildctl: " + e.Message
}

// Is maps the error's Kind onto the package sentinel errors.
func (e *Error) Is(target error) bool {
	return kindSentinel[e.Kind] == target
}

// ErrorInfo mirrors the contract's error object.
type ErrorInfo struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// envelope is the wire shape of every --json response. Pointers detect
// missing required fields so partial envelopes are rejected as a whole.
type envelope struct {
	SchemaVersion *int            `json:"schema_version"`
	OK            *bool           `json:"ok"`
	Data          json.RawMessage `json:"data"`
	Error         *ErrorInfo      `json:"error"`
}

// Session is one row of `buildctl session list --json`. Unknown JSON fields
// are ignored for forward compatibility.
type Session struct {
	ConversationID string    `json:"conversation_id"`
	RunID          *string   `json:"run_id"`
	ProjectID      string    `json:"project_id"`
	ProjectLabel   string    `json:"project_label"`
	Title          string    `json:"title"`
	Agent          string    `json:"agent"`
	HostID         string    `json:"host_id"`
	HostLabel      string    `json:"host_label"`
	HostKind       string    `json:"host_kind"`
	Status         string    `json:"status"`
	Live           bool      `json:"live"`
	Attachable     bool      `json:"attachable"`
	Resumable      bool      `json:"resumable"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// validStatuses is the closed v1 status enum.
var validStatuses = map[string]bool{
	"starting":     true,
	"working":      true,
	"idle":         true,
	"needs_input":  true,
	"exited":       true,
	"disconnected": true,
}

// Project is one row of `buildctl project list --json`.
type Project struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	RootPath  string `json:"root_path"`
	HostID    string `json:"host_id"`
	HostLabel string `json:"host_label"`
	HostKind  string `json:"host_kind"`
	Archived  bool   `json:"archived"`
}

// VersionInfo is the data payload of `buildctl version --json`.
type VersionInfo struct {
	CLIVersion              string `json:"cli_version"`
	SupportedSchemaVersions []int  `json:"supported_schema_versions"`
	ServerAvailable         bool   `json:"server_available"`
}

// SupportsV1 reports whether the CLI advertises schema version 1.
func (v VersionInfo) SupportsV1() bool {
	for _, s := range v.SupportedSchemaVersions {
		if s == SchemaVersion {
			return true
		}
	}
	return false
}

// LaunchOptions are the validated inputs to `buildctl session launch`.
type LaunchOptions struct {
	ProjectID  string
	Agent      string // "claude" or "codex"
	Permission string // "standard" (default) or "dangerous" (explicit only)
	Prompt     string // optional
}

// Client invokes the buildctl executable. It is safe for concurrent use.
type Client struct {
	// Command is the resolved path to the buildctl executable.
	Command string
	// Timeout bounds each non-interactive invocation. Zero uses DefaultTimeout.
	Timeout time.Duration
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// ResolveCommand finds the buildctl executable in contract order:
//  1. an explicitly configured command path;
//  2. `buildctl` on PATH;
//  3. ~/.build/bin/buildctl.
//
// Failure is nonfatal for Cockpit as a whole; callers should treat the
// returned *Error (Kind KindUnavailable) as "legacy-only mode".
func ResolveCommand(configured string) (string, error) {
	if configured != "" {
		p := expandTilde(configured)
		if isExecutable(p) {
			return p, nil
		}
		return "", &Error{
			Kind:     KindUnavailable,
			Message:  fmt.Sprintf("configured build command %q is not executable", configured),
			ExitCode: -1,
		}
	}
	if p, err := exec.LookPath("buildctl"); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".build", "bin", "buildctl")
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", &Error{
		Kind:     KindUnavailable,
		Message:  "buildctl not found (set [build].command, add it to PATH, or install ~/.build/bin/buildctl)",
		ExitCode: -1,
	}
}

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// Version runs `buildctl version --json`. Per the contract this succeeds even
// when Build itself is down; ServerAvailable then reports false.
func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	var v VersionInfo
	if err := c.runJSON(ctx, &v, "version", "--json"); err != nil {
		return VersionInfo{}, err
	}
	return v, nil
}

// ListSessions runs `buildctl session list --json` and strictly validates
// every row. Any malformed row rejects the entire response.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var data struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.runJSON(ctx, &data, "session", "list", "--json"); err != nil {
		return nil, err
	}
	for i, s := range data.Sessions {
		if err := validateSession(s); err != nil {
			return nil, &Error{Kind: KindMalformed, Message: fmt.Sprintf("session %d: %s", i, err), ExitCode: 0}
		}
	}
	// Duplicate (conversation, run) pairs are contradictory identities:
	// reject the whole response rather than guess which record is real.
	seen := make(map[string]bool, len(data.Sessions))
	for i, s := range data.Sessions {
		run := ""
		if s.RunID != nil {
			run = *s.RunID
		}
		k := s.ConversationID + "\x00" + run
		if seen[k] {
			return nil, &Error{Kind: KindMalformed, Message: fmt.Sprintf("session %d: duplicate conversation/run identity", i), ExitCode: 0}
		}
		seen[k] = true
	}
	return data.Sessions, nil
}

// validateSession enforces the invariants Cockpit relies on, identically for
// listed, launched, and resumed sessions.
func validateSession(s Session) error {
	if s.ConversationID == "" {
		return fmt.Errorf("empty conversation_id")
	}
	if s.RunID != nil && *s.RunID == "" {
		return fmt.Errorf("empty run_id")
	}
	if !validStatuses[s.Status] {
		return fmt.Errorf("unknown status %q", s.Status)
	}
	if s.UpdatedAt.IsZero() {
		return fmt.Errorf("missing updated_at")
	}
	return nil
}

// ListProjects runs `buildctl project list --json`.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var data struct {
		Projects []Project `json:"projects"`
	}
	if err := c.runJSON(ctx, &data, "project", "list", "--json"); err != nil {
		return nil, err
	}
	for i, p := range data.Projects {
		if p.ID == "" {
			return nil, &Error{Kind: KindMalformed, Message: fmt.Sprintf("project %d: empty id", i), ExitCode: 0}
		}
	}
	return data.Projects, nil
}

// Launch runs `buildctl session launch --json` with validated arguments.
// Every value travels as a separate argv element; nothing is shell-evaluated.
func (c *Client) Launch(ctx context.Context, opts LaunchOptions) (Session, error) {
	if opts.ProjectID == "" {
		return Session{}, &Error{Kind: KindInvalidRequest, Message: "launch: project id is required", ExitCode: -1}
	}
	if opts.Agent != "claude" && opts.Agent != "codex" {
		return Session{}, &Error{Kind: KindInvalidRequest, Message: fmt.Sprintf("launch: unsupported agent %q", opts.Agent), ExitCode: -1}
	}
	perm := opts.Permission
	if perm == "" {
		perm = "standard"
	}
	if perm != "standard" && perm != "dangerous" {
		return Session{}, &Error{Kind: KindInvalidRequest, Message: fmt.Sprintf("launch: unsupported permission %q", opts.Permission), ExitCode: -1}
	}

	args := []string{"session", "launch",
		"--project-id", opts.ProjectID,
		"--agent", opts.Agent,
		"--permission", perm,
	}
	if opts.Prompt != "" {
		args = append(args, "--prompt", opts.Prompt)
	}
	args = append(args, "--json")

	var s Session
	if err := c.runJSON(ctx, &s, args...); err != nil {
		return Session{}, err
	}
	if err := validateSession(s); err != nil {
		return Session{}, &Error{Kind: KindMalformed, Message: "launch: " + err.Error(), ExitCode: 0}
	}
	// A success response that contradicts the request is rejected as a whole.
	if s.ProjectID != opts.ProjectID {
		return Session{}, &Error{Kind: KindMalformed, Message: "launch: response project_id does not match the request", ExitCode: 0}
	}
	return s, nil
}

// Resume runs `buildctl session resume --json` for an existing conversation.
func (c *Client) Resume(ctx context.Context, conversationID, permission string) (Session, error) {
	if conversationID == "" {
		return Session{}, &Error{Kind: KindInvalidRequest, Message: "resume: conversation id is required", ExitCode: -1}
	}
	perm := permission
	if perm == "" {
		perm = "standard"
	}
	if perm != "standard" && perm != "dangerous" {
		return Session{}, &Error{Kind: KindInvalidRequest, Message: fmt.Sprintf("resume: unsupported permission %q", permission), ExitCode: -1}
	}

	var s Session
	err := c.runJSON(ctx, &s,
		"session", "resume",
		"--conversation-id", conversationID,
		"--permission", perm,
		"--json")
	if err != nil {
		return Session{}, err
	}
	if err := validateSession(s); err != nil {
		return Session{}, &Error{Kind: KindMalformed, Message: "resume: " + err.Error(), ExitCode: 0}
	}
	// A success response that contradicts the request is rejected as a whole.
	if s.ConversationID != conversationID {
		return Session{}, &Error{Kind: KindMalformed, Message: "resume: response conversation_id does not match the request", ExitCode: 0}
	}
	return s, nil
}

// AttachCommand builds the interactive `buildctl session attach --run-id`
// command. Attach deliberately uses no JSON envelope and no timeout: the
// child inherits the terminal and buildctl execs tmux itself. The caller
// wires stdin/stdout/stderr (Bubble Tea's ExecProcess does this) and is
// responsible for suspending/restoring the TUI around the child.
func (c *Client) AttachCommand(ctx context.Context, runID string) (*exec.Cmd, error) {
	if runID == "" {
		return nil, &Error{Kind: KindInvalidRequest, Message: "attach: run id is required", ExitCode: -1}
	}
	// #nosec G204 -- Command is a resolved executable path; runID travels as
	// a single argv element and is never shell-evaluated.
	return exec.CommandContext(ctx, c.Command, "session", "attach", "--run-id", runID), nil
}

// runJSON executes a --json command, enforces the one-object stdout rule, and
// decodes the envelope strictly. Any structural defect rejects the whole
// response — Cockpit never partially trusts Build data.
func (c *Client) runJSON(ctx context.Context, dataOut any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	stdout, stderr, exitCode, err := c.run(ctx, args...)
	if err != nil {
		return err // already an *Error
	}

	env, derr := decodeEnvelope(stdout)
	if derr != nil {
		// A non-zero exit with an undecodable envelope still carries meaning:
		// map the contract exit code and keep stderr as diagnostics.
		if exitCode != 0 {
			return &Error{
				Kind:     kindForExitCode(exitCode),
				Message:  fmt.Sprintf("exit %d without a valid envelope (stderr: %s)", exitCode, truncate(string(stderr), 200)),
				ExitCode: exitCode,
			}
		}
		return derr
	}

	if !*env.OK {
		if env.Error == nil || env.Error.Code == "" {
			return &Error{Kind: KindMalformed, Message: "failure envelope missing error.code", ExitCode: exitCode}
		}
		return &Error{
			Kind:      kindForCode(env.Error.Code),
			Code:      env.Error.Code,
			Message:   env.Error.Message,
			Retryable: env.Error.Retryable,
			ExitCode:  exitCode,
		}
	}

	// ok=true carrying an error object is contradictory: reject as a whole.
	if env.Error != nil {
		return &Error{Kind: KindMalformed, Message: "success envelope carries an error object", ExitCode: exitCode}
	}

	// ok=true with a non-zero process exit is inconsistent: reject.
	if exitCode != 0 {
		return &Error{
			Kind:     KindMalformed,
			Message:  fmt.Sprintf("ok envelope but exit code %d (stderr: %s)", exitCode, truncate(string(stderr), 200)),
			ExitCode: exitCode,
		}
	}

	if len(env.Data) == 0 || string(env.Data) == "null" {
		return &Error{Kind: KindMalformed, Message: "success envelope missing data", ExitCode: exitCode}
	}
	if err := json.Unmarshal(env.Data, dataOut); err != nil {
		return &Error{Kind: KindMalformed, Message: "data: " + err.Error(), ExitCode: exitCode}
	}
	return nil
}

// run executes buildctl with bounded output capture. It never returns a
// non-nil stdout alongside a non-nil error.
func (c *Client) run(ctx context.Context, args ...string) (stdout, stderr []byte, exitCode int, err error) {
	// #nosec G204 -- Command is a resolved executable path and args are data.
	cmd := exec.CommandContext(ctx, c.Command, args...)
	// WaitDelay bounds how long Wait blocks on pipe EOF after the process
	// exits or is killed — a grandchild inheriting stdout/stderr must not
	// wedge the client past its timeout.
	cmd.WaitDelay = waitDelay

	var outBuf, errBuf bytes.Buffer
	outW := &boundedWriter{w: &outBuf, limit: maxEnvelopeSize}
	errW := &boundedWriter{w: &errBuf, limit: maxEnvelopeSize}
	cmd.Stdout = outW
	cmd.Stderr = errW

	runErr := cmd.Run()

	// Context termination takes precedence over process exit details.
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil, -1, &Error{Kind: KindTimeout, Message: "buildctl exceeded " + c.timeout().String(), ExitCode: -1}
	}
	if ctx.Err() == context.Canceled {
		return nil, nil, -1, &Error{Kind: KindCanceled, Message: "canceled", ExitCode: -1}
	}

	// A response that overruns the bound is rejected as malformed, however
	// the child reacted to the failed write.
	if outW.overflowed || errW.overflowed {
		return nil, nil, -1, &Error{
			Kind:     KindMalformed,
			Message:  fmt.Sprintf("output exceeds %d byte bound", maxEnvelopeSize),
			ExitCode: -1,
		}
	}

	if runErr != nil {
		// The process exited but a descendant held the pipes past WaitDelay;
		// any output we did capture is untrustworthy as a complete envelope.
		if errors.Is(runErr, exec.ErrWaitDelay) {
			return nil, nil, -1, &Error{
				Kind:     KindMalformed,
				Message:  "buildctl exited but left output pipes open",
				ExitCode: -1,
			}
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			// The process ran and failed; the envelope (if any) decides the kind.
			return outBuf.Bytes(), errBuf.Bytes(), exitErr.ExitCode(), nil
		}
		// Spawn failure: missing executable, permissions, etc.
		return nil, nil, -1, &Error{
			Kind:     KindUnavailable,
			Message:  fmt.Sprintf("cannot run %s: %v", c.Command, runErr),
			ExitCode: -1,
		}
	}
	return outBuf.Bytes(), errBuf.Bytes(), 0, nil
}

// decodeEnvelope parses stdout as exactly one JSON object and validates the
// envelope frame. Unknown fields are ignored (forward compatibility).
func decodeEnvelope(stdout []byte) (*envelope, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, &Error{Kind: KindMalformed, Message: "empty stdout", ExitCode: -1}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var env envelope
	if err := dec.Decode(&env); err != nil {
		return nil, &Error{Kind: KindMalformed, Message: "invalid JSON: " + err.Error(), ExitCode: -1}
	}
	// The contract allows exactly one JSON object on stdout.
	if _, err := dec.Token(); err != io.EOF {
		return nil, &Error{Kind: KindMalformed, Message: "trailing data after JSON envelope", ExitCode: -1}
	}
	if env.SchemaVersion == nil {
		return nil, &Error{Kind: KindUnsupportedSchema, Message: "missing schema_version", ExitCode: -1}
	}
	if *env.SchemaVersion != SchemaVersion {
		return nil, &Error{
			Kind:     KindUnsupportedSchema,
			Message:  fmt.Sprintf("schema_version %d not supported (want %d)", *env.SchemaVersion, SchemaVersion),
			ExitCode: -1,
		}
	}
	if env.OK == nil {
		return nil, &Error{Kind: KindMalformed, Message: "missing ok field", ExitCode: -1}
	}
	return &env, nil
}

// kindForCode maps stable v1 contract error codes to kinds. Unknown codes on
// a well-formed failure envelope map to KindInternal — the response is valid,
// the failure class is simply newer than this client.
func kindForCode(code string) Kind {
	switch code {
	case "invalid_request", "unsupported_agent":
		return KindInvalidRequest
	case "build_unavailable":
		return KindUnavailable
	case "not_found", "stale_run":
		return KindNotFound
	case "already_active", "not_attachable", "not_resumable":
		return KindConflict
	case "permission_denied":
		return KindPermissionDenied
	default:
		return KindInternal
	}
}

// kindForExitCode maps contract process exit codes for failures that arrive
// without a parseable envelope.
func kindForExitCode(code int) Kind {
	switch code {
	case 2:
		return KindInvalidRequest
	case 3:
		return KindUnavailable
	case 4:
		return KindNotFound
	case 5:
		return KindConflict
	case 10:
		return KindInternal
	default:
		return KindInternal
	}
}

// boundedWriter fails the write once limit bytes have been accepted, which
// aborts the child process instead of letting it flood memory.
type boundedWriter struct {
	w          io.Writer
	limit      int
	written    int
	overflowed bool
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.written+len(p) > b.limit {
		b.overflowed = true
		return 0, fmt.Errorf("buildctl: output exceeds %d byte bound", b.limit)
	}
	n, err := b.w.Write(p)
	b.written += n
	return n, err
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
