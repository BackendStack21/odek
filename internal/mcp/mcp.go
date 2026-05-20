// Package mcp implements a Model Context Protocol server over stdio.
//
// This is a thin adapter over github.com/BackendStack21/go-mcp, which provides
// the core MCP protocol implementation. This package converts odek's tool
// interface to go-mcp's Tool type and manages transport.
//
// Architecture:
//
//	MCP Client (Claude Code)       odek mcp (this package → go-mcp)
//	┌─────────────────────┐        ┌─────────────────────────────────┐
//	│  tools/list ─────────────►  │  go-mcp dispatches to handlers  │
//	│                     │        │                                 │
//	│  tools/call ─────────────►  │  go-mcp calls Tool.Handler      │
//	│                     │        │                                 │
//	│  ◄────── result ────│        │                                 │
//	└─────────────────────┘        └─────────────────────────────────┘
//	       stdin/stdout                   stdin/stdout
//
// Security: uses the same DangerousConfig + Approver system as CLI mode.
// In MCP mode there's no TTY — the NonInteractiveAction fallback applies.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/BackendStack21/go-mcp/gomcp"
)

// ── Tool Adapter ───────────────────────────────────────────────────────

// NativeTool wraps a odek Tool interface for MCP exposure.
type NativeTool struct {
	Name        string
	Description string
	Schema      any
	CallFn      func(args string) (string, error)
}

// ── Server ─────────────────────────────────────────────────────────────

// Server implements the MCP protocol over stdio transport.
// It reads JSON-RPC requests from stdin and writes responses to stdout.
// Internally delegates to gomcp.Server.
type Server struct {
	version string
	tools   []NativeTool
	gmcp    *gomcp.Server
	reader  io.Reader
	writer  io.Writer
}

// NewServer creates an MCP server that reads from the given reader
// and writes responses to the given writer. For stdio transport, pass
// os.Stdin and os.Stdout. Tests can pass pipes or buffers.
func NewServer(version string, tools []NativeTool, reader io.Reader, writer io.Writer) *Server {
	gmcpSrv := gomcp.NewServer("odek", version)
	gmcpSrv.SetProtocolVersion("2025-03-26")

	for _, t := range tools {
		// Capture loop variable
		tool := t
		gmcpSrv.AddTool(gomcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Schema,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				argsJSON, err := json.Marshal(args)
				if err != nil {
					return "", fmt.Errorf("marshal args: %w", err)
				}
				return tool.CallFn(string(argsJSON))
			},
		})
	}

	return &Server{
		version: version,
		tools:   tools,
		gmcp:    gmcpSrv,
		reader:  reader,
		writer:  writer,
	}
}

// Run reads requests from stdin and processes them until EOF.
func (s *Server) Run(ctx context.Context) error {
	// Log startup to stderr (stdin/stdout are for MCP protocol)
	fmt.Fprintf(os.Stderr, "odek mcp ⚡  MCP server starting (v%s)\n", s.version)
	fmt.Fprint(os.Stderr, "  Tools: ")
	for i, t := range s.tools {
		if i > 0 {
			fmt.Fprint(os.Stderr, ", ")
		}
		fmt.Fprint(os.Stderr, t.Name)
	}
	fmt.Fprintln(os.Stderr)

	return s.gmcp.RunWithIO(s.reader, s.writer)
}

// ── ToolCaller Interface ───────────────────────────────────────────────

// ToolCaller is the interface a tool must implement to be exposed via MCP.
type ToolCaller interface {
	Name() string
	Description() string
	Schema() any
	Call(args string) (string, error)
}

// BuildNativeTools wraps a slice of odek.Tool-compatible values as
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
