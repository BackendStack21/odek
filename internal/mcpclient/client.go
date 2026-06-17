// Package mcpclient implements an MCP client that connects to external
// MCP servers over stdio. This allows odek to use tools from any MCP
// server (e.g., Claude Code's MCP servers for web scraping, databases,
// APIs, etc.) alongside its built-in tools.
//
// Protocol: JSON-RPC 2.0 over stdin/stdout
//   - initialize     — protocol handshake
//   - tools/list     — discover available tools
//   - tools/call     — invoke a tool
//   - ping           — health check
//
// Usage:
//
//	client, err := mcpclient.New("some-server", "node", []string{"server.js"})
//	tools, err := client.Discover(ctx)
//	result, err := client.CallTool(ctx, "tool_name", `{"arg":"val"}`)
//	client.Close()
//
// Config in odek.json:
//
//	{
//	  "mcp_servers": {
//	    "my-server": {
//	      "command": "node",
//	      "args": ["/path/to/server.js"]
//	    }
//	  }
//	}
package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ── Protocol Constants ──────────────────────────────────────────────────

const (
	ProtocolVersion    = "2025-03-26"
	DefaultTimeout     = 30 * time.Second
	// maxMCPResponseLine caps the size of a single JSON-RPC response line
	// from an MCP server. A malicious or broken server that emits a huge
	// line without a newline would otherwise be buffered entirely in memory
	// by ReadString, leading to OOM. Lines exceeding this limit are dropped
	// and the connection is closed.
	maxMCPResponseLine = 10 << 20 // 10 MiB
)

// ── JSON-RPC Types ─────────────────────────────────────────────────────

// request is a JSON-RPC 2.0 request sent to the MCP server.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response received from the MCP server.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError represents a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// ── MCP Initialize Types ───────────────────────────────────────────────

// initializeResult is the response to the initialize handshake.
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Capabilities map[string]any `json:"capabilities"`
}

// ── MCP Tool Types ─────────────────────────────────────────────────────

// ToolDef is the definition of a tool from tools/list.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// listToolsResult is the response to tools/list.
type listToolsResult struct {
	Tools []ToolDef `json:"tools"`
}

// callToolParams is the params sent to tools/call.
type callToolParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// callToolResult is the response to tools/call.
type callToolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// contentItem is a single piece of content in a tool result.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ── Server Config ──────────────────────────────────────────────────────

// ServerConfig defines an external MCP server to connect to.
// Matches the Claude Code MCP server config format.
type ServerConfig struct {
	// Command is the executable to run (e.g., "node", "python3", "uvx").
	Command string `json:"command"`
	// Args are the command-line arguments.
	Args []string `json:"args,omitempty"`
	// Env overrides environment variables for the subprocess.
	// Empty strings remove the variable from the environment.
	Env map[string]string `json:"env,omitempty"`
}

// lineResult carries the result of a single readLine from the reader goroutine.
type lineResult struct {
	line string
	err  error
}

// callResponse carries a JSON-RPC response from readLoop to the waiting caller.
type callResponse struct {
	result json.RawMessage
	err    error
}

// ── Client ─────────────────────────────────────────────────────────────

// Client manages a connection to an external MCP server over stdio.
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	lineCh chan lineResult // single-reader goroutine sends lines here
	done   chan struct{}   // closed when process exits

	mu      sync.Mutex
	nextID  int
	pending map[int]chan callResponse // routes responses to waiting callers
}

// New spawns an MCP server process and returns a client connected to it.
// The server process is started immediately and cleaned up on Close().
func New(name string, cfg ServerConfig) (*Client, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)

	// Apply env overrides. Always build a sanitized environment so MCP children
	// do not inherit the full parent environment (API keys, tokens, secrets).
	cmd.Env = buildEnv(cfg.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcpclient %s: stdin pipe: %w", name, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcpclient %s: stdout pipe: %w", name, err)
	}

	// Stderr is inherited from the parent so errors are visible
	cmd.Stderr = nil // nil = inherit os.Stderr by default in exec.Cmd

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcpclient %s: start: %w", name, err)
	}

	c := &Client{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		lineCh:  make(chan lineResult, 10),
		done:    make(chan struct{}),
		pending: make(map[int]chan callResponse),
	}

	// Start single-reader goroutine
	go c.readLoop()

	// Monitor process exit in background
	go func() {
		cmd.Wait()
		close(c.done)
	}()

	return c, nil
}

// allowedEnvVars is the allowlist of parent environment variables that may be
// forwarded to MCP server subprocesses. It contains only non-sensitive,
// commonly-required variables (e.g. PATH so the server can find binaries).
var allowedEnvVars = map[string]bool{
	"PATH": true,
	"HOME": true,
	"USER": true,
	"LOGNAME": true,
	"SHELL": true,
	"TMPDIR": true,
	"LANG": true,
	"LC_ALL": true,
	"LC_CTYPE": true,
	"LC_MESSAGES": true,
	"LC_NUMERIC": true,
	"LC_TIME": true,
	"LC_COLLATE": true,
	"LC_MONETARY": true,
	"LC_PAPER": true,
	"LC_NAME": true,
	"LC_ADDRESS": true,
	"LC_TELEPHONE": true,
	"LC_MEASUREMENT": true,
	"LC_IDENTIFICATION": true,
	"TZ": true,
	"TERM": true,
}

// isSensitiveEnvVar reports whether a key looks like a secret. These patterns
// are blocked from being forwarded to MCP children even if they are present in
// the parent environment or explicitly supplied as overrides.
func isSensitiveEnvVar(key string) bool {
	upper := strings.ToUpper(key)
	for _, pat := range []string{
		"API_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "CREDS",
		"PRIVATE_KEY", "ACCESS_KEY",
	} {
		if strings.Contains(upper, pat) {
			return true
		}
	}
	return false
}

// buildEnv constructs the environment for the subprocess.
//
// Only a small allowlist of parent environment variables is forwarded, plus any
// overrides from the MCP server config. Keys that look like secrets (e.g.
// *_API_KEY, *_TOKEN, *_SECRET) are always stripped, even when provided as
// overrides, so a compromised or malicious MCP server cannot exfiltrate tokens.
func buildEnv(overrides map[string]string) []string {
	// Start with current env
	env := osEnviron()
	if env == nil {
		env = environ() // fallback for testing
	}

	// Build a map from the allowlist only.
	envMap := make(map[string]string)
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			if allowedEnvVars[k] && !isSensitiveEnvVar(k) {
				envMap[k] = v
			}
		}
	}

	// Apply overrides. Sensitive overrides are dropped; empty values remove the
	// variable.
	for k, v := range overrides {
		if isSensitiveEnvVar(k) {
			continue
		}
		if v == "" {
			delete(envMap, k)
		} else {
			envMap[k] = v
		}
	}

	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}

// Close terminates the MCP server process and cleans up resources.
// Safe to call multiple times.
func (c *Client) Close() error {
	// Close stdin to signal EOF to the server
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Wait for process with timeout
	select {
	case <-c.done:
		// Process already exited
	case <-time.After(5 * time.Second):
		// Force kill
		c.cmd.Process.Kill()
		<-c.done
	}

	return nil
}

// Name returns the server name for this client.
func (c *Client) Name() string { return c.name }

// Discover performs the MCP handshake and returns all available tools.
func (c *Client) Discover(ctx context.Context) ([]ToolDef, error) {
	// Step 1: Initialize
	if _, err := c.call(ctx, "initialize", json.RawMessage(`{"protocolVersion":"`+ProtocolVersion+`","capabilities":{},"clientInfo":{"name":"odek","version":"dev"}}`)); err != nil {
		return nil, fmt.Errorf("mcpclient %s: initialize: %w", c.name, err)
	}

	// Step 2: List tools
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcpclient %s: tools/list: %w", c.name, err)
	}

	var result listToolsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcpclient %s: parse tools/list: %w", c.name, err)
	}

	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server with the given JSON arguments
// and returns the text content of the result.
func (c *Client) CallTool(ctx context.Context, name string, argsJSON string) (string, error) {
	// Parse args as raw JSON
	var args any
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			args = argsJSON // fallback: send as string
		}
	}

	params := callToolParams{Name: name, Arguments: args}
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("mcpclient %s: marshal call params: %w", c.name, err)
	}

	raw, err := c.call(ctx, "tools/call", paramsRaw)
	if err != nil {
		return "", fmt.Errorf("mcpclient %s: tools/call: %w", c.name, err)
	}

	var result callToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("mcpclient %s: parse result: %w", c.name, err)
	}

	if result.IsError {
		msg := "unknown error"
		if len(result.Content) > 0 {
			msg = result.Content[0].Text
		}
		return "", fmt.Errorf("mcpclient %s: tool %s returned error: %s", c.name, name, msg)
	}

	// Concatenate all text content items
	var parts []string
	for _, item := range result.Content {
		if item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// call sends a JSON-RPC request and waits for the matching response.
func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Assign unique ID and register a response channel.
	respCh := make(chan callResponse, 1)

	c.mu.Lock()
	id := c.nextID
	c.nextID++
	req := request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	c.pending[id] = respCh
	c.mu.Unlock()

	// Unregister on exit to prevent map leak.
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	reqRaw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Send
	c.mu.Lock()
	_, err = fmt.Fprintln(c.stdin, string(reqRaw))
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// Wait for response via channel (dispatched by readLoop).
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case cr, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("connection closed before response received")
		}
		if cr.err != nil {
			return nil, cr.err
		}
		return cr.result, nil
	}
}

// readLoop is a single reader goroutine that reads lines from stdout and
// routes each response to the correct waiting caller via the pending map.
// This prevents response misrouting when multiple concurrent call() instances
// are reading from the same connection.
// Exits when stdout returns an error (EOF on pipe close).
func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMCPResponseLine)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue // skip malformed lines
		}

		// Route to the waiting caller, if any.
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()

		if ok && ch != nil {
			// Non-blocking send in case caller has already timed out.
			var rpcErr error
			if resp.Error != nil {
				rpcErr = resp.Error
			}
			select {
			case ch <- callResponse{result: resp.Result, err: rpcErr}:
			default:
			}
		}
	}

	// scanner.Scan returned false because of EOF, an I/O error, or an
	// oversized token. Unblock all waiters; if the line was too long, tell
	// them why before closing the channel.
	oversized := scanner.Err() == bufio.ErrTooLong
	c.mu.Lock()
	pending := make(map[int]chan callResponse, len(c.pending))
	for id, ch := range c.pending {
		pending[id] = ch
		delete(c.pending, id)
	}
	c.mu.Unlock()

	for _, ch := range pending {
		if oversized {
			select {
			case ch <- callResponse{err: fmt.Errorf("mcpclient %s: response line exceeded %d byte limit", c.name, maxMCPResponseLine)}:
			default:
			}
		}
		close(ch)
	}
}

// readLine reads a single line with context-based timeout.
// Uses the single-reader goroutine (readLoop) so no goroutine leaks on context
// cancellation — the goroutine is owned by the connection, not the RPC call.
func (c *Client) readLine(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r, ok := <-c.lineCh:
		if !ok {
			// Channel closed — reader goroutine exited (process gone)
			return "", io.EOF
		}
		return r.line, r.err
	}
}

// ── Platform helpers (replaces os.Environ for testing) ──────────────────

// osEnviron is os.Environ, swapped in tests.
var osEnviron = osEnvironDefault

func osEnvironDefault() []string { return os.Environ() }

// environ is a fallback for tests where os.Environ isn't available.
var environ = environDefault

func environDefault() []string { return os.Environ() }

// ── ToolAdapter ────────────────────────────────────────────────────────

// ToolAdapter wraps an MCP client tool as a odek.Tool-compatible value.
// It implements the Name(), Description(), Schema(), and Call() methods
// that odek's agent loop expects, forwarding calls to the MCP server.
type ToolAdapter struct {
	// Client is the MCP client connection.
	Client *Client

	// ToolName is the name of the tool on the MCP server.
	ToolName string

	// Desc is the tool description.
	Desc string

	// ParamSchema is the JSON schema for the tool's parameters.
	ParamSchema any
}

// Name returns the tool's name, prefixed with the server name to avoid
// collisions when multiple MCP servers expose tools with the same name.
func (a *ToolAdapter) Name() string {
	return a.Client.Name() + "__" + a.ToolName
}

// Description returns the tool's description.
func (a *ToolAdapter) Description() string { return a.Desc }

// Schema returns the tool's input JSON schema.
func (a *ToolAdapter) Schema() any {
	if a.ParamSchema != nil {
		return a.ParamSchema
	}
	return map[string]any{"type": "object"}
}

// Call invokes the tool on the MCP server with the given JSON arguments.
func (a *ToolAdapter) Call(args string) (string, error) {
	return a.Client.CallTool(context.Background(), a.ToolName, args)
}
