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
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/artifact"
)

// ── Protocol Constants ──────────────────────────────────────────────────

const (
	ProtocolVersion = "2025-03-26"
	// maxMCPResponseLine is the default per-server cap on the size of a
	// single JSON-RPC response line from an MCP server. A malicious or broken
	// server that emits a huge line without a newline would otherwise be
	// buffered entirely in memory by ReadString, leading to OOM. Lines
	// exceeding the limit are dropped and the connection is closed.
	// Servers may tune it via ServerConfig.MaxResponseBytes.
	maxMCPResponseLine = 10 << 20 // 10 MiB

	// MaxResponseBytesCeiling is the absolute ceiling for
	// ServerConfig.MaxResponseBytes. No configuration may raise the response
	// cap above this value; attempting to do so is an error.
	MaxResponseBytesCeiling = 64 << 20 // 64 MiB

	// MaxTimeoutSeconds is the hard cap for ServerConfig.TimeoutSeconds.
	// Values above it are clamped to this cap with a warning.
	MaxTimeoutSeconds = 3600

	// DefaultMaxResultChars is the default cap on tool result text forwarded
	// to the model. Oversized (but valid) results receive a structured
	// truncation notice instead of being silently cut.
	DefaultMaxResultChars = 200000

	// MaxResultCharsCap is the hard cap for ServerConfig.MaxResultChars.
	// Values above it are clamped to this cap with a warning.
	MaxResultCharsCap = 1000000
)

// DefaultTimeout bounds each MCP request when neither the caller nor the
// server config supplies a deadline. It is a var so tests can temporarily
// lower it. Per-server config never mutates this global; it is only read as
// the default value source.
var DefaultTimeout = 30 * time.Second

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
	Method  string          `json:"method,omitempty"`
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

// validateToolName rejects server-supplied tool names that are empty, too long,
// or contain characters that could be used to spoof another tool or escape the
// name-based trust boundary. Only ASCII letters, digits, underscore, and hyphen
// are permitted.
func validateToolName(name string) error {
	if name == "" {
		return fmt.Errorf("tool name is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("tool name %q exceeds 64 characters", name)
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("tool name %q cannot contain %q", name, "__")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("tool name %q contains invalid character %q", name, r)
		}
	}
	return nil
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
// Matches the Claude Code MCP server config format, extended with the
// odek-extension/v1 timeout/limit fields (see docs/EXTENSIONS.md).
type ServerConfig struct {
	// Command is the executable to run (e.g., "node", "python3", "uvx").
	Command string `json:"command"`
	// Args are the command-line arguments.
	Args []string `json:"args,omitempty"`
	// Env overrides environment variables for the subprocess.
	// Empty strings remove the variable from the environment.
	Env map[string]string `json:"env,omitempty"`

	// TimeoutSeconds bounds each request to this server when the caller does
	// not supply a deadline. Zero uses DefaultTimeout (30s). Values above
	// MaxTimeoutSeconds (3600) are clamped to the cap with a warning.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// MaxResponseBytes caps the size of a single JSON-RPC response line from
	// this server. Zero uses the 10 MiB default. The absolute ceiling is
	// MaxResponseBytesCeiling (64 MiB); exceeding it is an error.
	MaxResponseBytes int64 `json:"max_response_bytes,omitempty"`

	// MaxResultChars caps the tool result text forwarded to the model. Zero
	// uses DefaultMaxResultChars (200000). Values above MaxResultCharsCap
	// (1000000) are clamped to the cap with a warning. Valid-but-oversized
	// results get a structured truncation notice (artifact refs retained),
	// never a silent cut.
	MaxResultChars int `json:"max_result_chars,omitempty"`

	// ArtifactRoots lists directories under which file:// artifact refs from
	// this server are accepted. Empty means every artifact ref is rejected
	// (fail closed). Refs in odek.tool-result/v1 envelopes are validated
	// against these roots by the artifact subsystem (internal/artifact)
	// before the result is rendered for the model.
	ArtifactRoots []string `json:"artifact_roots,omitempty"`

	// AutoApprove marks the server as trusted by the operator: it skips the
	// project-server approval prompt and the per-tool registration prompts
	// (schema guard scans still apply). TRUST RULES: the flag is honored
	// only when it comes from the operator-owned global config
	// (~/.odek/config.json) AND the resolved command/args/env/limits/roots
	// still match that global entry. A command-less marker is not a
	// wildcard for a project-defined command. The loader strips
	// auto_approve from project ./odek.json with a warning. Trust metadata
	// only — deliberately excluded from approval keys, which hash
	// execution-relevant fields.
	AutoApprove bool `json:"auto_approve,omitempty"`
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

	// writeCh feeds a single writer goroutine. Requests must be handed to
	// the writer instead of written inline: a server that stops reading
	// stdin fills the pipe buffer, and an inline write would block while
	// holding c.mu — wedging every later call() at the mutex before the
	// ctx/select that is supposed to bound it (audit: a hung server could
	// permanently wedge serve/telegram instances that share one Client per
	// server for the process lifetime). Enqueueing under ctx keeps every
	// caller bounded by the per-server timeout.
	writeCh   chan []byte
	writeDone chan struct{} // closed when the writer goroutine exits
	closed    chan struct{} // closed by Close to unblock an idle writer

	// Per-server limits, resolved from ServerConfig at construction time.
	// A zero timeout means "fall back to DefaultTimeout at call time" (the
	// package var stays the default value source and is never mutated from
	// config). Zero caps are treated as their defaults defensively, so a
	// hand-constructed Client behaves like a default-configured one.
	timeout          time.Duration // per-request deadline fallback
	maxResponseBytes int64         // scanner.Buffer cap in readLoop
	maxResultChars   int           // model-facing result text cap
	artifactRoots    []string      // configured roots for artifact ref validation (internal/artifact)
	warnings         []string      // non-fatal config issues (e.g. clamped values)

	mu        sync.Mutex
	nextID    int
	pending   map[int]chan callResponse // routes responses to waiting callers
	writeErr  error                     // sticky writer failure, set once
	closeOnce sync.Once
}

// normalizeLimits resolves the effective per-server limits from cfg, applying
// defaults, rejecting values that may not be exceeded, and clamping values
// above their hard caps (recording a warning for each clamp).
func normalizeLimits(name string, cfg ServerConfig) (timeout time.Duration, maxResp int64, maxChars int, warnings []string, err error) {
	if cfg.TimeoutSeconds < 0 {
		return 0, 0, 0, nil, fmt.Errorf("mcpclient %s: timeout_seconds must be >= 0, got %d", name, cfg.TimeoutSeconds)
	}
	if cfg.MaxResponseBytes < 0 {
		return 0, 0, 0, nil, fmt.Errorf("mcpclient %s: max_response_bytes must be >= 0, got %d", name, cfg.MaxResponseBytes)
	}
	if cfg.MaxResultChars < 0 {
		return 0, 0, 0, nil, fmt.Errorf("mcpclient %s: max_result_chars must be >= 0, got %d", name, cfg.MaxResultChars)
	}
	if cfg.MaxResponseBytes > MaxResponseBytesCeiling {
		// Absolute ceiling: no configuration may raise the response cap above
		// 64 MiB. Reject rather than clamp so a typo cannot silently weaken or
		// silently differ from the operator's intent.
		return 0, 0, 0, nil, fmt.Errorf("mcpclient %s: max_response_bytes %d exceeds the absolute ceiling of %d bytes", name, cfg.MaxResponseBytes, MaxResponseBytesCeiling)
	}

	// Zero timeout_seconds means "no per-server override": the client falls
	// back to DefaultTimeout at call time.
	timeout = 0
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
		if cfg.TimeoutSeconds > MaxTimeoutSeconds {
			timeout = time.Duration(MaxTimeoutSeconds) * time.Second
			warnings = append(warnings, fmt.Sprintf("mcp server %q: timeout_seconds %d exceeds the hard cap; clamped to %d", name, cfg.TimeoutSeconds, MaxTimeoutSeconds))
		}
	}

	maxResp = maxMCPResponseLine
	if cfg.MaxResponseBytes > 0 {
		maxResp = cfg.MaxResponseBytes
	}

	maxChars = DefaultMaxResultChars
	if cfg.MaxResultChars > 0 {
		maxChars = cfg.MaxResultChars
		if cfg.MaxResultChars > MaxResultCharsCap {
			maxChars = MaxResultCharsCap
			warnings = append(warnings, fmt.Sprintf("mcp server %q: max_result_chars %d exceeds the hard cap; clamped to %d", name, cfg.MaxResultChars, MaxResultCharsCap))
		}
	}
	return timeout, maxResp, maxChars, warnings, nil
}

// validateName checks that an MCP server or tool name is safe to use as part
// of an odek tool identifier. Names must be non-empty, ≤64 chars, contain only
// ASCII letters/digits/underscore/hyphen, and must not contain the double-
// underscore separator used to qualify tool names.
func validateName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name cannot be empty", kind)
	}
	if len(name) > 64 {
		return fmt.Errorf("%s name %q too long (%d > 64)", kind, name, len(name))
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("%s name %q cannot contain %q", kind, name, "__")
	}
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if isLetter || isDigit || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("%s name %q contains invalid character %q at position %d", kind, name, r, i)
	}
	return nil
}

// New spawns an MCP server process and returns a client connected to it.
// The server process is started immediately and cleaned up on Close().
func New(name string, cfg ServerConfig) (*Client, error) {
	if err := validateName("server", name); err != nil {
		return nil, err
	}

	timeout, maxResp, maxChars, warnings, err := normalizeLimits(name, cfg)
	if err != nil {
		return nil, err
	}

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

	// Stderr is inherited from the parent so errors are visible: a nil
	// Stderr would connect the child to os.DevNull, silently swallowing
	// every MCP server startup error and crash message.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcpclient %s: start: %w", name, err)
	}

	c := &Client{
		name:             name,
		cmd:              cmd,
		stdin:            stdin,
		stdout:           bufio.NewReader(stdout),
		lineCh:           make(chan lineResult, 10),
		done:             make(chan struct{}),
		writeCh:          make(chan []byte, 32),
		writeDone:        make(chan struct{}),
		closed:           make(chan struct{}),
		pending:          make(map[int]chan callResponse),
		timeout:          timeout,
		maxResponseBytes: maxResp,
		maxResultChars:   maxChars,
		artifactRoots:    append([]string(nil), cfg.ArtifactRoots...),
		warnings:         warnings,
	}

	// Start single-reader goroutine
	go c.readLoop()

	// Start single-writer goroutine (see writeCh doc comment).
	go c.writeLoop()

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
	"PATH":              true,
	"HOME":              true,
	"USER":              true,
	"LOGNAME":           true,
	"SHELL":             true,
	"TMPDIR":            true,
	"LANG":              true,
	"LC_ALL":            true,
	"LC_CTYPE":          true,
	"LC_MESSAGES":       true,
	"LC_NUMERIC":        true,
	"LC_TIME":           true,
	"LC_COLLATE":        true,
	"LC_MONETARY":       true,
	"LC_PAPER":          true,
	"LC_NAME":           true,
	"LC_ADDRESS":        true,
	"LC_TELEPHONE":      true,
	"LC_MEASUREMENT":    true,
	"LC_IDENTIFICATION": true,
	"TZ":                true,
	"TERM":              true,
}

// isSensitiveEnvVar reports whether a key looks like a secret. These patterns
// are blocked from being forwarded to MCP children even if they are present in
// the parent environment or explicitly supplied as overrides.
//
// The name is normalised (uppercased, "-" and "_" stripped) before matching so
// non-underscore spellings — API-KEY, APIKEY, Private-Key — cannot slip a
// secret past the filter; environment variable names legally contain hyphens
// or no separator at all.
func isSensitiveEnvVar(key string) bool {
	norm := strings.NewReplacer("-", "", "_", "").Replace(strings.ToUpper(key))
	for _, pat := range []string{
		"APIKEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "CREDS",
		"PRIVATEKEY", "ACCESSKEY",
	} {
		if strings.Contains(norm, pat) {
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
	// Unblock the writer goroutine if it is idle, then signal EOF to the
	// server. Guarded so repeated Close calls stay safe.
	c.closeOnce.Do(func() { close(c.closed) })
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Wait for the writer to exit so no Write races the process teardown.
	select {
	case <-c.writeDone:
	case <-time.After(time.Second):
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

// Warnings returns non-fatal configuration issues recorded at construction
// time (e.g. timeout_seconds or max_result_chars clamped to their hard caps).
// Callers should surface these to the operator.
func (c *Client) Warnings() []string { return append([]string(nil), c.warnings...) }

// ArtifactRoots returns the configured artifact roots for this server. Empty
// means every artifact ref from this server must be rejected (fail closed).
// Validation against these roots is performed by the artifact subsystem.
func (c *Client) ArtifactRoots() []string { return append([]string(nil), c.artifactRoots...) }

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

	for i := range result.Tools {
		if err := validateToolName(result.Tools[i].Name); err != nil {
			return nil, fmt.Errorf("mcpclient %s: tools/list: %w", c.name, err)
		}
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
		// The per-server result cap applies to the error channel too: a
		// server must not be able to forward more text to the model by
		// marking its payload isError than a successful result allows.
		msg = c.applyResultLimit(name, msg)
		return "", fmt.Errorf("mcpclient %s: tool %s returned error: %s", c.name, name, msg)
	}

	// Concatenate all text content items
	var parts []string
	for _, item := range result.Content {
		if item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	text := strings.Join(parts, "\n")

	// odek.tool-result/v1 envelope: validate every artifact ref against the
	// server's configured roots (fail closed on any violation) and render the
	// compact model-facing form (text + metadata lines, never raw paths or
	// artifact content).
	env, err := artifact.ParseEnvelope(text)
	if err != nil {
		return "", fmt.Errorf("mcpclient %s: tool %s: %w", c.name, name, err)
	}
	if env == nil && len(parts) > 1 {
		// A multi-item result carrying an envelope plus trailing notes: the
		// joined text no longer parses as one JSON document, so the probe
		// above treats it as plain text — delivering the RAW envelope JSON to
		// the model and bypassing artifact-ref validation. Parse each item
		// for the envelope instead; a schema-matched but malformed item still
		// fails closed.
		var envErr error
		for _, p := range parts {
			e, eerr := artifact.ParseEnvelope(p)
			if eerr != nil {
				envErr = eerr
				continue
			}
			if e != nil {
				env = e
				break
			}
		}
		if env == nil && envErr != nil {
			return "", fmt.Errorf("mcpclient %s: tool %s: %w", c.name, name, envErr)
		}
		// Preserve the non-envelope items after the rendered form: the
		// trailing notes are ordinary text the server meant the model to
		// see alongside the artifact metadata.
		if env != nil {
			var extras []string
			for _, p := range parts {
				if e, eerr := artifact.ParseEnvelope(p); eerr == nil && e != nil {
					continue // the envelope item(s) render above
				}
				if strings.TrimSpace(p) != "" {
					extras = append(extras, p)
				}
			}
			suffix := ""
			if len(extras) > 0 {
				suffix = "\n" + strings.Join(extras, "\n")
			}
			for i := range env.Artifacts {
				if _, err := artifact.Validate(env.Artifacts[i], c.artifactRoots); err != nil {
					return "", fmt.Errorf("mcpclient %s: tool %s: artifact ref rejected: %w", c.name, name, err)
				}
			}
			return c.applyResultLimit(name, c.renderCappedEnvelope(name, env)+suffix), nil
		}
	}
	if env != nil {
		for i := range env.Artifacts {
			// The resolved path is intentionally discarded here: it is
			// local bookkeeping for a future event log (WP4) and must
			// never reach the model-facing result.
			if _, err := artifact.Validate(env.Artifacts[i], c.artifactRoots); err != nil {
				return "", fmt.Errorf("mcpclient %s: tool %s: artifact ref rejected: %w", c.name, name, err)
			}
		}
		// The per-server result cap applies to the FULL rendered envelope
		// output — compact text plus metadata lines — not just the text
		// field (audit 2026-08: oversized id/summary fields rode past the
		// cap that only ever bounded env.Text).
		return c.renderCappedEnvelope(name, env), nil
	}

	return c.applyResultLimit(name, text), nil
}

// truncationNotice builds the structured marker appended to (or replacing part
// of) an oversized but valid tool result. It names the server, the tool, the
// configured limit, and the observed size so the model and the operator can
// tell exactly what happened. The marker text deliberately contains no angle
// brackets so it cannot be confused with the untrusted-content wrapper.
func truncationNotice(server, tool string, limit, observed int) string {
	return fmt.Sprintf("\n[odek: result truncated — server %q tool %q produced %d chars, exceeding the configured max_result_chars=%d; the full result is available via the retained artifact references, if any]", server, tool, observed, limit)
}

// applyResultLimit enforces the per-server max_result_chars cap on a valid,
// fully parsed piece of result text. Text within the limit passes through
// unchanged. Oversized text is never silently cut: it gets a structured
// truncation notice naming the server, tool, configured limit, and observed
// size. odek.tool-result/v1 envelopes are detected before this function runs
// (see CallTool), so their artifact refs are validated and rendered as
// metadata lines; this cap applies to plain (non-envelope) results and to
// tool-level error text (isError results), so the error channel cannot be
// used to stuff context past the cap. Envelope results are capped on their
// FULL rendered output by renderCappedEnvelope instead.
func (c *Client) applyResultLimit(tool, text string) string {
	limit := c.maxResultChars
	if limit <= 0 {
		limit = DefaultMaxResultChars
	}
	observed := utf8.RuneCountInString(text)
	if observed <= limit {
		return text
	}

	notice := truncationNotice(c.name, tool, limit, observed)
	budget := limit - utf8.RuneCountInString(notice)
	if budget < 0 {
		budget = 0
	}
	return truncateRunes(text, budget) + notice
}

// renderCappedEnvelope renders a validated envelope and enforces the
// per-server max_result_chars cap on the FULL model-facing output — the
// envelope text, the per-artifact metadata lines, and (when truncating) the
// notice combined. Render bounds each server-controlled field to
// artifact.MaxFieldRunes, but bounded fields add up across the capped
// artifact count, so when the total exceeds the limit the envelope text —
// the part the cap was sized for — is shrunk first and the metadata lines,
// the compact resolvable payload, are always preserved (they are appended
// after the capped text by design; their size is bounded by field bounds ×
// MaxArtifactsPerEnvelope). Only when the bounded metadata block alone
// crowds out the text does the text give way entirely.
func (c *Client) renderCappedEnvelope(tool string, env *artifact.Envelope) string {
	limit := c.maxResultChars
	if limit <= 0 {
		limit = DefaultMaxResultChars
	}
	rendered := artifact.Render(env)
	observed := utf8.RuneCountInString(rendered)
	if observed <= limit {
		return rendered
	}

	notice := truncationNotice(c.name, tool, limit, observed)
	// Everything Render appends beyond the text (the separating newline and
	// the metadata lines) plus the notice has to fit alongside the text.
	textBudget := limit - (observed - utf8.RuneCountInString(env.Text)) - utf8.RuneCountInString(notice)
	if textBudget < 0 {
		textBudget = 0
	}
	capped := *env
	capped.Text = truncateRunes(env.Text, textBudget)
	return artifact.Render(&capped) + notice
}

// truncateRunes returns s cut to at most n runes (never splitting a multi-byte
// character).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// call sends a JSON-RPC request and waits for the matching response.
func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the request with the per-server timeout (or DefaultTimeout when
	// the server has no override) unless the caller already supplied a
	// deadline. A hung MCP server must not deadlock the agent loop or startup
	// discovery.
	if _, ok := ctx.Deadline(); !ok {
		timeout := c.timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
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

	// Hand the request to the single writer goroutine. Enqueueing is
	// ctx-bounded: when a malicious server stops reading stdin and the
	// pipe + channel buffers fill, callers fail with the per-server
	// timeout instead of wedging on a mutex (see writeCh doc comment).
	select {
	case c.writeCh <- append(reqRaw, '\n'):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Surface a sticky writer failure promptly (the enqueued request would
	// otherwise just time out later).
	c.mu.Lock()
	werr := c.writeErr
	c.mu.Unlock()
	if werr != nil {
		return nil, fmt.Errorf("write: %w", werr)
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

// writeLoop is the single writer goroutine owning c.stdin. It keeps write
// ordering (one syscall per request) while ensuring no caller can block on
// a full pipe — callers enqueue under their own ctx deadline instead (see
// writeCh doc comment). On the first write error it records the sticky
// failure and closes stdin so the server sees EOF, readLoop exits, and all
// pending waiters unblock ("connection closed before response received").
// Senders that are still blocked on a full channel unblock via their own
// ctx deadline or the sticky writeErr fast path in call().
func (c *Client) writeLoop() {
	defer close(c.writeDone)
	for {
		select {
		case req := <-c.writeCh:
			if _, err := c.stdin.Write(req); err != nil {
				c.mu.Lock()
				if c.writeErr == nil {
					c.writeErr = err
				}
				c.mu.Unlock()
				// EOF to the server → readLoop exits → pending waiters
				// unblock. A second Close error is irrelevant here.
				c.stdin.Close()
				return
			}
		case <-c.closed:
			return
		}
	}
}

// readLoop is a single reader goroutine that reads lines from stdout and
// routes each response to the correct waiting caller via the pending map.
// This prevents response misrouting when multiple concurrent call() instances
// are reading from the same connection.
// Exits when stdout returns an error (EOF on pipe close).
func (c *Client) readLoop() {
	maxResp := c.maxResponseBytes
	if maxResp <= 0 {
		maxResp = maxMCPResponseLine
	}
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxResp))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue // skip malformed lines
		}
		// A line carrying a "method" field is a server→client request or
		// notification (JSON-RPC 2.0), never a response to one of our calls.
		// Routing it by id would deliver {result:null} to a waiting caller
		// whose id collides — and drop the real response when it arrives.
		if resp.Method != "" {
			continue
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
			case ch <- callResponse{err: fmt.Errorf("mcpclient %s: response line exceeded configured max_response_bytes limit of %d bytes", c.name, maxResp)}:
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

	ctxMu sync.RWMutex
	ctx   context.Context
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

// SetContext records the request context used by the next Call.
func (a *ToolAdapter) SetContext(ctx context.Context) {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()
}

// Call invokes the tool on the MCP server with the given JSON arguments.
func (a *ToolAdapter) Call(args string) (string, error) {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return a.Client.CallTool(ctx, a.ToolName, args)
}
