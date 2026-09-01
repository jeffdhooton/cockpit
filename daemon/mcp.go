// Package daemon serves cockpit's local tool server: a JSON-RPC endpoint over
// loopback HTTP that lets agents inspect and drive the workspace.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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
}

// NewServer builds a server with a fresh session id.
func NewServer(tools ToolHandler, version string) *Server {
	return &Server{
		Tools:     tools,
		Version:   version,
		SessionID: fmt.Sprintf("cockpit-%x", time.Now().UnixNano()),
	}
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

// Handler returns the HTTP routes for the server. Both "/" and "/mcp" answer,
// because clients differ on which one they post to.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serve)
	mux.HandleFunc("/mcp", s.serve)
	return mux
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("mcp-session-id", s.SessionID)

	switch r.Method {
	case http.MethodPost:
		s.servePost(w, r)
	case http.MethodDelete:
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
