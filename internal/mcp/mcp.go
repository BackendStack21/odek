// Package mcp implements a Model Context Protocol server over stdio.
//
// MCP (https://modelcontextprotocol.io) is a standard protocol that allows
// AI agents (Claude Code, Cursor, etc.) to discover and invoke tools.
// This package implements an MCP server that exposes kode's built-in tools
// via the stdio transport — the protocol Claude Code uses natively.
//
// Architecture:
//
//	MCP Client (Claude Code)          kode mcp (this package)
//	┌─────────────────────┐           ┌──────────────────────┐
//	│  tools/list ──────────────►   │  Responds with       │
//	│                     │           │  tool schemas         │
//	│  tools/call ──────────────►   │  Executes tool,       │
//	│                     │           │  returns result       │
//	│  ◄────── result ────│           │                      │
//	└─────────────────────┘           └──────────────────────┘
//	       stdin/stdout                   stdin/stdout
//
// Security: uses the same DangerousConfig + Approver system as CLI mode.
// In MCP mode there's no TTY — the NonInteractiveAction fallback applies.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ── Protocol Constants ──────────────────────────────────────────────────

const (
	ProtocolVersion = "2025-03-26"
)

// ── JSON-RPC Types ─────────────────────────────────────────────────────

// JSONRPCRequest is a generic incoming JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // can be string, number, or null (notifications)
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a generic outgoing JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParse     = -32700
	ErrCodeInvalidReq = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
)

// ── MCP Initialize Types ───────────────────────────────────────────────

// InitializeRequest is the MCP initialize method params.
type InitializeRequest struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// InitializeResult is the response to initialize.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// ── MCP Tool Types ─────────────────────────────────────────────────────

// Tool is an MCP tool definition returned by tools/list.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// ListToolsResult is the response to tools/list.
type ListToolsResult struct {
	Tools []Tool `json:"tools"`
}

// CallToolParams is the params for tools/call.
type CallToolParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// CallToolResult is the response to tools/call.
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ContentItem is a single piece of content in a tool result.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ── Tool Adapter ───────────────────────────────────────────────────────

// NativeTool wraps a kode Tool interface for MCP exposure.
type NativeTool struct {
	Name        string
	Description string
	Schema      any
	CallFn      func(args string) (string, error)
}

// ── Server ─────────────────────────────────────────────────────────────

// Server implements the MCP protocol over stdio transport.
// It reads JSON-RPC requests from stdin and writes responses to stdout.
type Server struct {
	version string
	tools   []NativeTool
	reader  *bufio.Reader
	writer  io.Writer
	mu      sync.Mutex // protects writer
}

// NewServer creates an MCP server that reads from the given reader
// and writes responses to the given writer. For stdio transport, pass
// os.Stdin and os.Stdout. Tests can pass pipes or buffers.
func NewServer(version string, tools []NativeTool, reader io.Reader, writer io.Writer) *Server {
	return &Server{
		version: version,
		tools:   tools,
		reader:  bufio.NewReader(reader),
		writer:  writer,
	}
}

// Run reads requests from stdin and processes them until EOF.
func (s *Server) Run(ctx context.Context) error {
	initialized := false

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("mcp: read: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.writeError(nil, ErrCodeParse, "Parse error", err.Error())
			continue
		}

		// Handle the request
		resp := s.handleRequest(ctx, req, initialized)
		if resp != nil {
			s.writeResponse(req.ID, resp)
		}
		// After successful initialize, mark as initialized
		if req.Method == "initialize" && resp != nil {
			// Check it's a successful initialize result (not an error)
			if _, ok := resp.(InitializeResult); ok {
				initialized = true
			}
		}
	}
}

func (s *Server) handleRequest(ctx context.Context, req JSONRPCRequest, initialized bool) (result any) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params)
	case "initialized":
		// Notification — no response needed
		return nil
	case "tools/list":
		if !initialized {
			return &JSONRPCError{Code: ErrCodeInvalidReq, Message: "Not initialized"}
		}
		return s.handleListTools()
	case "tools/call":
		if !initialized {
			return &JSONRPCError{Code: ErrCodeInvalidReq, Message: "Not initialized"}
		}
		return s.handleCallTool(ctx, req.Params)
	case "ping":
		return map[string]any{}
	default:
		return &JSONRPCError{
			Code:    ErrCodeMethodNotFound,
			Message: fmt.Sprintf("Method not found: %s", req.Method),
		}
	}
}

func (s *Server) handleInitialize(params json.RawMessage) any {
	var req InitializeRequest
	if params != nil {
		json.Unmarshal(params, &req) // best effort
	}

	return InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: map[string]any{
			"tools": map[string]any{},
		},
		ServerInfo: struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{
			Name:    "kode",
			Version: s.version,
		},
	}
}

func (s *Server) handleListTools() any {
	tools := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return ListToolsResult{Tools: tools}
}

func (s *Server) handleCallTool(ctx context.Context, params json.RawMessage) any {
	var req CallToolParams
	if err := json.Unmarshal(params, &req); err != nil {
		return &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: "Invalid tool call params",
			Data:    err.Error(),
		}
	}

	// Find the tool
	for _, t := range s.tools {
		if t.Name == req.Name {
			// Marshal arguments back to JSON string for the tool's Call method
			argsJSON, err := json.Marshal(req.Arguments)
			if err != nil {
				return &CallToolResult{
					Content: []ContentItem{{Type: "text", Text: fmt.Sprintf("Error marshaling arguments: %v", err)}},
					IsError: true,
				}
			}

			result, err := t.CallFn(string(argsJSON))
			if err != nil {
				return &CallToolResult{
					Content: []ContentItem{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
					IsError: true,
				}
			}

			return &CallToolResult{
				Content: []ContentItem{{Type: "text", Text: result}},
			}
		}
	}

	return &CallToolResult{
		Content: []ContentItem{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", req.Name)}},
		IsError: true,
	}
}

func (s *Server) writeResponse(id json.RawMessage, result any) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
	}

	switch r := result.(type) {
	case *JSONRPCError:
		resp.Error = r
	case JSONRPCError:
		e := r
		resp.Error = &e
	default:
		resp.Result = r
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return
	}

	s.mu.Lock()
	fmt.Fprintln(s.writer, string(data))
	s.mu.Unlock()
}

func (s *Server) writeError(id json.RawMessage, code int, message string, data any) {
	s.writeResponse(id, &JSONRPCError{Code: code, Message: message, Data: data})
}

// ── BuildNativeTools ───────────────────────────────────────────────────

// ToolCaller is the interface a tool must implement to be exposed via MCP.
type ToolCaller interface {
	Name() string
	Description() string
	Schema() any
	Call(args string) (string, error)
}

// BuildNativeTools wraps a slice of kode.Tool-compatible values as
// MCP NativeTool entries for the server. Skips tools that don't make
// sense in MCP context (delegate_tasks, memory).
func BuildNativeTools(callers []ToolCaller) []NativeTool {
	var tools []NativeTool
	for _, t := range callers {
		// Skip tools not useful in MCP context
		if t.Name() == "delegate_tasks" || t.Name() == "memory" {
			continue
		}
		tools = append(tools, NativeTool{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
			CallFn:      t.Call,
		})
	}
	return tools
}
