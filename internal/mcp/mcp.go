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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/BackendStack21/go-mcp/gomcp"
)

// ── Tool Adapter ───────────────────────────────────────────────────────

// NativeTool wraps a odek Tool interface for MCP exposure.
type NativeTool struct {
	Name        string
	Description string
	Schema      any
	CallFn      func(args string) (string, error)
	// CallContextFn is preferred when set so request cancellation reaches
	// context-aware odek tools. CallFn remains for context-free callers.
	CallContextFn func(ctx context.Context, args string) (string, error)
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

type protocolState struct {
	legacyInitialized atomic.Bool
	toolCallAllowed   atomic.Bool
}

// NewServer creates an MCP server that reads from the given reader
// and writes responses to the given writer. For stdio transport, pass
// os.Stdin and os.Stdout. Tests can pass pipes or buffers.
func NewServer(version string, tools []NativeTool, reader io.Reader, writer io.Writer) *Server {
	gmcpSrv := gomcp.NewServer("odek", version)
	protocol := &protocolState{}

	for _, t := range tools {
		// Capture loop variable
		tool := t
		gmcpSrv.AddTool(gomcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Schema,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				if !protocol.toolCallAllowed.Load() {
					return "", fmt.Errorf("protocol negotiation required before tools/call")
				}
				argsJSON, err := json.Marshal(args)
				if err != nil {
					return "", fmt.Errorf("marshal args: %w", err)
				}
				return callNativeTool(ctx, tool, string(argsJSON))
			},
		})
	}

	return &Server{
		version: version,
		tools:   tools,
		gmcp:    gmcpSrv,
		reader:  newProtocolReader(reader, protocol),
		writer:  writer,
	}
}

type protocolReader struct {
	reader     *bufio.Reader
	state      *protocolState
	pending    []byte
	pendingErr error
	line       []byte
	overflow   bool
}

func newProtocolReader(reader io.Reader, state *protocolState) io.Reader {
	return &protocolReader{reader: bufio.NewReader(reader), state: state}
}

func (r *protocolReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		fragment, err := r.reader.ReadSlice('\n')
		r.pending = append(r.pending[:0], fragment...)
		if !r.overflow {
			if len(r.line)+len(fragment) <= int(gomcp.DefaultMaxRequestBytes) {
				r.line = append(r.line, fragment...)
			} else {
				r.line = r.line[:0]
				r.overflow = true
			}
		}
		complete := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if err == io.EOF && len(fragment) > 0 {
			complete = true
		}
		if complete {
			if r.overflow {
				r.state.toolCallAllowed.Store(false)
			} else {
				r.state.observe(r.line)
			}
			r.line = r.line[:0]
			r.overflow = false
		}
		if err == bufio.ErrBufferFull {
			err = nil
		}
		r.pendingErr = err
		if len(r.pending) == 0 {
			return 0, err
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if len(r.pending) == 0 {
		err := r.pendingErr
		r.pendingErr = nil
		return n, err
	}
	return n, nil
}

func (s *protocolState) observe(line []byte) {
	s.toolCallAllowed.Store(false)
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Meta struct {
				ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
				ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
			} `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil ||
		req.JSONRPC != "2.0" || req.Method == "" {
		return
	}
	if req.Method == "initialize" {
		if validRequestID(req.ID) {
			version := req.Params.Meta.ProtocolVersion
			if version == "" || supportedProtocolVersion(version) {
				s.legacyInitialized.Store(true)
			}
		}
		return
	}
	if req.Method != "tools/call" {
		return
	}
	version := req.Params.Meta.ProtocolVersion
	if version == "" {
		s.toolCallAllowed.Store(s.legacyInitialized.Load())
		return
	}
	var capabilities map[string]json.RawMessage
	capabilitiesOK := json.Unmarshal(req.Params.Meta.ClientCapabilities, &capabilities) == nil &&
		capabilities != nil
	modern := version == gomcp.ProtocolVersion20260728 && capabilitiesOK
	s.toolCallAllowed.Store(modern)
}

func validRequestID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var id any
	if err := json.Unmarshal(raw, &id); err != nil {
		return false
	}
	switch id.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func supportedProtocolVersion(version string) bool {
	for _, supported := range gomcp.SupportedProtocolVersions {
		if version == supported {
			return true
		}
	}
	return false
}

func callNativeTool(ctx context.Context, tool NativeTool, args string) (result string, err error) {
	defer func() {
		if recover() != nil {
			// Do not let the dependency's outer recovery log a potentially
			// secret-bearing panic value to stderr.
			result = ""
			err = fmt.Errorf("tool handler panicked")
		}
	}()
	if tool.CallContextFn != nil {
		return tool.CallContextFn(ctx, args)
	}
	if tool.CallFn == nil {
		return "", fmt.Errorf("tool has no handler")
	}
	return tool.CallFn(args)
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

	return s.gmcp.RunWithIOContext(ctx, s.reader, s.writer)
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
		tool := t
		native := NativeTool{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
			CallFn:      t.Call,
		}
		if contextTool, ok := t.(interface{ SetContext(context.Context) }); ok {
			native.CallContextFn = func(ctx context.Context, args string) (string, error) {
				contextTool.SetContext(ctx)
				return tool.Call(args)
			}
		}
		tools = append(tools, native)
	}
	return tools
}
