// Package daemon serves cockpit's local tool server: a JSON-RPC endpoint over
// loopback HTTP that lets agents inspect and drive the workspace.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/jhoot/cockpit/sources"
)

// baselineProtocol is the version assumed when a client asks for one this
// server does not recognise.
const baselineProtocol = "2024-11-05"

// supportedProtocols are echoed back verbatim during initialize.
var supportedProtocols = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

// ToolDefinition describes one callable tool.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolHandler supplies the tools this server exposes.
type ToolHandler interface {
	Definitions() []ToolDefinition
	Call(ctx context.Context, name string, args map[string]any) (any, error)
}

// Server speaks JSON-RPC 2.0 over HTTP on behalf of a ToolHandler.
type Server struct {
	Tools     ToolHandler
	Version   string
	SessionID string

	// StatusKey derives per-target hook tokens and Runner writes the status
	// into tmux. Both are nil on a server built by NewServer alone, and the
	// status route then refuses every request rather than accepting one it
	// cannot authenticate.
	StatusKey []byte
	Runner    sources.Runner
}

// NewServer builds a server with a fresh session id.
func NewServer(tools ToolHandler, version string) *Server {
	return &Server{
		Tools:     tools,
		Version:   version,
		SessionID: fmt.Sprintf("cockpit-%x", time.Now().UnixNano()),
	}
}

// newServerWithStatus builds a server that can also record hook status.
func newServerWithStatus(tools ToolHandler, version string, key []byte, r sources.Runner) *Server {
	s := NewServer(tools, version)
	s.StatusKey = key
	s.Runner = r
	return s
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

func success(id json.RawMessage, result any) jsonRPCResponse {
	return jsonRPCResponse{JSONRPC: "2.0", Result: result, ID: id}
}

func failure(id json.RawMessage, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: code, Message: message}, ID: id}
}

// maxRequestBytes caps a request body. Every real call is a few hundred bytes
// of JSON-RPC; the cap only exists so an unbounded reader cannot be pointed at
// the daemon.
const maxRequestBytes = 1 << 20

// Handler returns the HTTP routes for the server. Both "/" and "/mcp" answer,
// because clients differ on which one they post to.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serve)
	mux.HandleFunc("/mcp", s.serve)
	mux.HandleFunc("/hooks/status", s.serveStatus)
	return mux
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("mcp-session-id", s.SessionID)

	if !guard(w, r) {
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.servePost(w, r)
	case http.MethodDelete:
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// guard refuses anything that did not come from a local tool, and reports
// whether the request may proceed.
//
// Binding loopback keeps out another machine, not another program on this one.
// A page the user visits can post here, and by the time CORS hides the reply
// from it a tools/call has already typed into a live pane. Two headers close
// that without a token to manage.
//
// An Origin is attached by a browser and by nothing else — MCP clients send
// none — so its presence disqualifies the request whatever it says, localhost
// included. Requiring JSON then rules out the CORS-simple content types
// (text/plain, the form encodings) that a browser would otherwise send with no
// preflight at all.
func guard(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "cockpit daemon does not answer browser requests", http.StatusForbidden)
		return false
	}
	if r.Method == http.MethodPost && !isJSONContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	return true
}

// isJSONContentType reports whether a Content-Type names JSON, ignoring
// parameters so the usual "; charset=utf-8" still passes.
func isJSONContentType(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	return err == nil && mediaType == "application/json"
}

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// An oversized body is a transport-level refusal, not a malformed
		// document: saying "parse error" would send the caller looking at
		// their JSON.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.write(w, failure(nil, -32700, "Parse error: "+err.Error()))
		return
	}

	// A request without an id is a notification: acknowledge, answer nothing.
	isNotification := len(req.ID) == 0

	if req.JSONRPC != "2.0" {
		if isNotification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.write(w, failure(req.ID, -32600, "Invalid JSON-RPC version"))
		return
	}

	switch req.Method {
	case "initialize":
		s.write(w, success(req.ID, s.initializeResult(req.Params)))

	case "tools/list":
		if isNotification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.write(w, success(req.ID, map[string]any{"tools": s.Tools.Definitions()}))

	case "tools/call":
		if isNotification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.write(w, success(req.ID, s.callTool(r.Context(), req.Params)))

	default:
		if isNotification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.write(w, failure(req.ID, -32601, "Method not found: "+req.Method))
	}
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)

	version := baselineProtocol
	if supportedProtocols[p.ProtocolVersion] {
		version = p.ProtocolVersion
	}

	return map[string]any{
		"protocolVersion": version,
		"serverInfo": map[string]any{
			"name":    "cockpit",
			"version": s.Version,
		},
		"capabilities": map[string]any{"tools": map[string]any{}},
	}
}

// callTool runs a tool and wraps the outcome. A tool that fails still returns a
// successful response — the caller needs to read the message, not a bare
// protocol error.
func (s *Server) callTool(ctx context.Context, params json.RawMessage) map[string]any {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)

	result, err := s.Tools.Call(ctx, p.Name, p.Arguments)
	if err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Error: " + err.Error()}},
			"isError": true,
		}
	}

	text, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Error: " + marshalErr.Error()}},
			"isError": true,
		}
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(text)}},
	}
}

func (s *Server) write(w http.ResponseWriter, resp jsonRPCResponse) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
