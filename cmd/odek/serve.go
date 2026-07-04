package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/skills"
	golangws "golang.org/x/net/websocket"
)

//go:embed ui
var uiFS embed.FS

// maxWSMessageBytes caps the size of an incoming WebSocket text message.
// This prevents a local client from exhausting server memory by sending a
// multi-gigabyte frame.
const maxWSMessageBytes = 8 * 1024 * 1024 // 8 MiB

// maxPromptBytes caps the size of the user prompt accepted through the Web UI.
// Combined with the WebSocket frame cap, this prevents a local client from
// bloating the session file or exhausting the LLM context budget.
const maxPromptBytes = 1 * 1024 * 1024 // 1 MiB

// maxModelIDBytes caps the length of a model ID supplied by the Web UI.
const maxModelIDBytes = 128

// modelIDPattern restricts model IDs to printable ASCII characters commonly
// used by model providers (alphanumeric, punctuation, and path separators).
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/@-]+$`)

// maxWSConnections caps the number of concurrent WebSocket clients. Once the
// limit is reached, further upgrade attempts are rejected with HTTP 503. This
// prevents a local attacker from spawning unlimited connections, each of which
// starts an agent with its own sandbox container and memory buffers.
const maxWSConnections = 20

// wsConnSem is the global connection limiter for /ws.
var wsConnSem = make(chan struct{}, maxWSConnections)

// wsUpgradeLimiter provides per-IP rate limiting for WebSocket upgrades, making
// it more expensive to rapidly churn connections and exhaust wsConnSem.
var wsUpgradeLimiter = newRateLimiter(30, time.Minute)

// promptCancels maps a session ID to the cancel function for the prompt
// currently executing on that session. A mutex protects the map so concurrent
// WebSocket handlers and the HTTP /api/cancel endpoint can access it safely.
// Using session IDs as keys scopes cancellation to the caller's session,
// preventing one connection from cancelling another connection's prompt.
var (
	promptCancelMu sync.Mutex
	promptCancels  = map[string]context.CancelFunc{}
)

// registerPromptCancel records cancel as the active cancel function for
// sessionID. It must be unregistered when the prompt completes.
func registerPromptCancel(sessionID string, cancel context.CancelFunc) {
	if sessionID == "" || cancel == nil {
		return
	}
	promptCancelMu.Lock()
	promptCancels[sessionID] = cancel
	promptCancelMu.Unlock()
}

// unregisterPromptCancel removes any cancel function registered for sessionID.
func unregisterPromptCancel(sessionID string) {
	if sessionID == "" {
		return
	}
	promptCancelMu.Lock()
	delete(promptCancels, sessionID)
	promptCancelMu.Unlock()
}

// cancelPrompt cancels the active prompt for sessionID, if any. It returns
// true if a cancel function was found and invoked.
func cancelPrompt(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	promptCancelMu.Lock()
	cancel, ok := promptCancels[sessionID]
	promptCancelMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// wsConns tracks every active WebSocket connection so serveOnListener can
// close them on shutdown, unblocking handleWS goroutines and allowing their
// defers (sandbox cleanup, agent.Close) to run before the process exits.
var wsConns sync.Map // map[*golangws.Conn]struct{}

// wsHandlerWG counts live handleWS goroutines; serveOnListener waits on it
// after closing all connections to ensure cleanup completes.
var wsHandlerWG sync.WaitGroup

// sessionLookupLimiter provides basic per-IP rate limiting for session detail
// lookups, raising the cost of brute-force enumeration of session IDs.
var sessionLookupLimiter = newRateLimiter(60, time.Minute)

// rateLimiter is a tiny per-key sliding-window rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	max     int
	window  time.Duration
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		windows: make(map[string][]time.Time),
		max:     max,
		window:  window,
	}
}

// allow returns true if the key has not exceeded max requests in the sliding
// window. It prunes stale entries on each call.
func (rl *rateLimiter) allow(key string) bool {
	if rl == nil || rl.max <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-rl.window)
	var times []time.Time
	for _, t := range rl.windows[key] {
		if t.After(cutoff) {
			times = append(times, t)
		}
	}
	if len(times) >= rl.max {
		rl.windows[key] = times
		return false
	}
	times = append(times, now)
	rl.windows[key] = times
	return true
}

// reset clears all tracked windows (useful in tests).
func (rl *rateLimiter) reset() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.windows = make(map[string][]time.Time)
}

// ── Serve Command ───────────────────────────────────────────────────────

func serveCmd(args []string) error {
	addr := "127.0.0.1:8080"
	openBrowser := false

	// Sandbox CLI flags (nil pointers = not set)
	var sandbox *bool
	var sandboxReadonly *bool
	var promptCaching *bool
	var sandboxImage, sandboxNetwork, sandboxMemory, sandboxCPUs, sandboxUser string
	var toolsEnabled, toolsDisabled []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			i++
			if i < len(args) {
				addr = args[i]
			}
		case "--open":
			openBrowser = true
		case "--help", "-h":
			printServeHelp()
			return nil
		case "--sandbox":
			sandbox = boolPtr(true)
		case "--no-sandbox":
			// Explicit opt-out from the serve-mode default-on policy.
			sandbox = boolPtr(false)
		case "--sandbox-image":
			i++
			if i < len(args) {
				sandboxImage = args[i]
			}
		case "--sandbox-network":
			i++
			if i < len(args) {
				sandboxNetwork = args[i]
			}
		case "--sandbox-readonly":
			sandboxReadonly = boolPtr(true)
		case "--sandbox-memory":
			i++
			if i < len(args) {
				sandboxMemory = args[i]
			}
		case "--sandbox-cpus":
			i++
			if i < len(args) {
				sandboxCPUs = args[i]
			}
		case "--sandbox-user":
			i++
			if i < len(args) {
				sandboxUser = args[i]
			}
		case "--prompt-caching":
			promptCaching = boolPtr(true)
		case "--tool":
			i++
			if i < len(args) {
				toolsEnabled = append(toolsEnabled, args[i])
			}
		case "--no-tool":
			i++
			if i < len(args) {
				toolsDisabled = append(toolsDisabled, args[i])
			}
		default:
			return fmt.Errorf("unknown flag %q for serve", args[i])
		}
	}

	resolved := config.LoadConfig(config.CLIFlags{
		Sandbox:         sandbox,
		PromptCaching:   promptCaching,
		SandboxImage:    sandboxImage,
		SandboxNetwork:  sandboxNetwork,
		SandboxReadonly: sandboxReadonly,
		SandboxMemory:   sandboxMemory,
		SandboxCPUs:     sandboxCPUs,
		SandboxUser:     sandboxUser,
		ToolsEnabled:    toolsEnabled,
		ToolsDisabled:   toolsDisabled,
	})
	// Serve mode default-on for sandbox: the Web UI surface is the
	// largest blast radius (browser-driven tool calls, untrusted-page
	// fetches), and the user opted into a long-running process. If no
	// explicit opt-out was passed via --no-sandbox or config, force on.
	// To disable, run `odek serve --no-sandbox` (and accept the warning).
	if sandbox == nil && !resolved.Sandbox {
		resolved.Sandbox = true
		fmt.Fprintln(os.Stderr, "odek serve: sandbox enabled by default (run with --no-sandbox to disable)")
	}
	systemMessage := buildSystemPrompt(resolved)

	// Build sandbox config from resolved settings (serve)
	store, err := session.NewStore()
	if err != nil {
		return fmt.Errorf("session store: %w", err)
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".odek", "sessions")),
	)

	// Per-instance CSRF token for browser-driven WebSocket connections. A
	// random token is issued at server start, injected into the served HTML,
	// and also delivered as a SameSite=Strict HttpOnly cookie. The /ws
	// handshake requires the token via cookie, header, or WebSocket
	// subprotocol, so a page served by another localhost port cannot open
	// an agent-controlling WebSocket.
	wsToken, err := newServeToken()
	if err != nil {
		return fmt.Errorf("CSRF token: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	mux.Handle("/ws", &golangws.Server{
		Handshake: func(cfg *golangws.Config, req *http.Request) error {
			return wsHandshakeWithLimits(cfg, req, wsToken)
		},
		Handler: func(conn *golangws.Conn) {
			handleWS(store, resourceReg, resolved, systemMessage, conn)
		},
	})
	mux.Handle("/api/resources", requireLocalOrigin(handleResourceSearch(resourceReg)))
	mux.Handle("/api/sessions", requireLocalOrigin(handleSessionList(store)))
	mux.Handle("/api/sessions/", requireLocalOrigin(handleSessionByID(store)))
	mux.Handle("/api/models", requireLocalOrigin(handleModelList(resolved.Model)))
	mux.Handle("/api/cancel", requireLocalOrigin(handleCancel(store)))

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(os.Stderr, "odek serve ⚡  http://%s\n", listener.Addr())
	fmt.Fprintf(os.Stderr, "  WebSocket: ws://%s/ws\n", listener.Addr())
	fmt.Fprintf(os.Stderr, "  Type @ to reference files, drop or attach files inline.\n")

	if openBrowser {
		openInBrowser("http://" + listener.Addr().String())
	}

	return serveOnListener(listener, mux)
}

// printServeHelp prints the serve command help text.
func printServeHelp() {
	fmt.Println(`Usage: odek serve [flags]

Start the odek web UI server.

Flags:
  --addr 127.0.0.1:8080    Listen address (default 127.0.0.1:8080)
  --open                   Open browser automatically
  --sandbox                Enable Docker sandbox (default on for serve mode)
  --no-sandbox             Disable the default sandbox (requires explicit opt-out)
  --sandbox-image image    Docker image (default: alpine:latest or Dockerfile.odek)
  --sandbox-network net    Docker network mode (default: none)
  --sandbox-readonly       Mount working directory read-only
  --sandbox-memory limit   Container memory limit (e.g. 512m, 2g)
  --sandbox-cpus limit     Container CPU limit (e.g. 0.5, 2, 4)
  --sandbox-user user      Container user (e.g. 1000:1000)
  --tool name              Enable a tool for the LLM (repeatable)
  --no-tool name           Disable a tool for the LLM (repeatable)
  --help, -h               Show this help`)
}

// serveOnListener serves the odek Web UI on a pre-created listener.
// Extracted for testing — allows E2E tests to pass a listener on a known port.
// Handles SIGINT/SIGTERM for graceful shutdown: stops accepting new connections
// and gives in-flight requests up to 5 seconds to finish.
func serveOnListener(listener net.Listener, mux *http.ServeMux) error {
	srv := &http.Server{Handler: mux}

	// Catch Ctrl-C and SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case sig := <-quit:
		fmt.Fprintf(os.Stderr, "\nodek serve: %s received, shutting down...\n", sig)
	}

	// Phase 1: stop accepting new connections.
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer httpCancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		fmt.Fprintf(os.Stderr, "odek serve: http shutdown: %v\n", err)
	}

	// Phase 2: close active WebSocket connections so their handleWS goroutines
	// unblock from Receive(), run their defers (agent.Close → docker rm -f),
	// and decrement wsHandlerWG.
	//
	// http.Server.Shutdown does not forcibly close WebSocket connections because
	// they are long-lived and never become "idle". Without this step, the
	// process would exit while sandbox containers are still running.
	wsConns.Range(func(key, _ any) bool {
		key.(*golangws.Conn).Close()
		return true
	})

	// Phase 3: wait for all handleWS goroutines to finish (up to 10s).
	// Each goroutine runs defer agent.Close() which calls docker rm -f.
	drained := make(chan struct{})
	go func() { wsHandlerWG.Wait(); close(drained) }()

	select {
	case <-drained:
		fmt.Fprintln(os.Stderr, "odek serve: all connections closed cleanly")
	case <-time.After(10 * time.Second):
		fmt.Fprintln(os.Stderr, "odek serve: drain timeout — some containers may still be running")
	}

	fmt.Fprintln(os.Stderr, "odek serve: stopped")
	return nil
}

// ── Agent Builder ──────────────────────────────────────────────────────

func newServeAgent(resolved config.ResolvedConfig, system string, sendFn func(v any) error) (*odek.Agent, func() error, func(), *wsApprover, error) {
	var sm *skills.SkillManager
	if resolved.Skills.Learn {
		sm = skills.NewSkillManagerWithEmbedding(
			expandHome("~/.odek/skills"),
			"./.odek/skills",
			resolved.Skills.Embedding,
		)
	}

	// Create WebSocket approver for dangerous operations approval
	approver := newWSApprover(sendFn)
	resolved.Dangerous.Approver = approver

	tools := builtinTools(resolved.Dangerous, sm, approver, resolved.MaxConcurrency, resolved.APIKey, toolConfig{WebSearch: resolved.WebSearch}, nil)

	// Apply tool filtering based on configuration.
	tools = filterBuiltinTools(tools, resolved.Tools)

	// Find the delegateTasksTool to wire up sub-agent log streaming
	var subagentTool *delegateTasksTool
	for _, t := range tools {
		if dt, ok := t.(*delegateTasksTool); ok {
			subagentTool = dt
			break
		}
	}
	if subagentTool != nil {
		subagentTool.OnSubagentLog = func(taskIdx int, line string) {
			var event struct {
				Type string `json:"type"`
				Name string `json:"name,omitempty"`
				Data string `json:"data,omitempty"`
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				return
			}
			sendFn(map[string]any{
				"type":     "subagent_log",
				"task_idx": taskIdx,
				"name":     event.Name,
				"event":    event.Type,
				"data":     event.Data,
			})
		}
	}
	var sandboxCleanup func() error

	// MCP server tools
	var mcpCleanup func()
	if len(resolved.MCPServers) > 0 {
		cl, err := loadMCPTools(resolved, &tools)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("mcp: %w", err)
		}
		mcpCleanup = cl
	}

	if resolved.Sandbox {
		cfg := sandboxConfig{
			Image:    resolved.SandboxImage,
			Network:  resolved.SandboxNetwork,
			Readonly: resolved.SandboxReadonly,
			Memory:   resolved.SandboxMemory,
			CPUs:     resolved.SandboxCPUs,
			User:     resolved.SandboxUser,
			Env:      resolved.SandboxEnv,
			Volumes:  resolved.SandboxVolumes,
		}
		var sbContainerName string
		var sandboxErr error
		sbContainerName, sandboxCleanup, sandboxErr = setupSandbox(tools, cfg)
		if sandboxErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("sandbox: %w", sandboxErr)
		}
		_ = sbContainerName // not used in serve mode
	} else {
		warnSandboxDisabled()
	}

	// Build runtime context. In sandbox mode omit the host's hostname/CWD —
	// those values belong to the host machine, not the container the agent
	// actually runs commands in. The agent can discover its own environment
	// by running `hostname` or `pwd` inside the sandbox.
	var runtimeCtx string
	if resolved.Sandbox {
		runtimeCtx = "You are running in a sandboxed web UI. " +
			"Your shell commands execute inside an isolated Docker container — " +
			"the hostname, filesystem paths, and processes you see are container-scoped, " +
			"not the host machine. " +
			"Responses are streamed to the browser via WebSocket in real-time. " +
			"Markdown (headings, lists, code blocks, bold, links) is fully rendered. " +
			"Keep responses concise and visual."
	} else {
		runtimeCtx = odek.BuildRuntimeContext("web")
	}

	agent, err := odek.New(odek.Config{
		Model:            resolved.Model,
		BaseURL:          resolved.BaseURL,
		APIKey:           resolved.APIKey,
		MaxIterations:    resolved.MaxIter,
		MaxToolParallel:  resolved.MaxToolParallel,
		SystemMessage:    system,
		UntrustedWrapper: func(source, content string) string { return wrapUntrusted(context.Background(), source, content) },
		RuntimeContext:   runtimeCtx,
		NoProjectFile:   resolved.NoAgents,
		Thinking:        resolved.Thinking,
		InteractionMode: resolved.InteractionMode,
		Tools:           tools,
		ToolFilter:      odek.ToolFilterConfig{Enabled: resolved.Tools.Enabled, Disabled: resolved.Tools.Disabled},
		// SandboxCleanup is intentionally NOT passed here. In serve mode,
		// cleanup is the caller's responsibility (handleWS defers it).
		// Passing it here would cause agent.Close() to call docker rm -f,
		// and the explicit defer sandboxCleanup() in handleWS to call it
		// again — a harmless but confusing double-call.
		Renderer:     nil, // silent — we stream via WebSocket
		Skills:       &resolved.Skills,
		SkillManager: sm,
		MemoryConfig: resolved.Memory,
		ToolEventHandler: func(event, name, data string) {
			sendFn(map[string]any{
				"type": event,
				"name": name,
				"data": data,
			})
		},
		SkillEventHandler: func(event skills.SkillEvent) {
			sendFn(map[string]any{
				"type":       "skill_event",
				"event":      event.Type,
				"skill_name": event.SkillName,
				"skills":     event.Skills,
				"heuristic":  event.Heuristic,
			})
		},
		MemoryEventHandler: func(event memory.MemoryEvent) {
			sendFn(map[string]any{
				"type":       "memory_event",
				"event":      event.Type,
				"target":     event.Target,
				"session_id": event.SessionID,
				"content":    event.Content,
				"count":      event.Count,
				"new_count":  event.NewCount,
				"untrusted":  event.Untrusted,
			})
		},
		AgentSignalHandler: func(event loop.SignalEvent) {
			sendFn(map[string]any{
				"type":   "agent_signal",
				"event":  event.Type,
				"detail": event.Detail,
				"tool":   event.Tool,
				"count":  event.Count,
			})
		},
		// Stream thinking/reasoning content to the WebUI.
		// Only fire for pre-tool iterations (reasoning before tool calls);
		// post-tool callbacks have no new reasoning to display.
		IterationCallback: func(info loop.IterationInfo) {
			if info.IsPreTool && info.ReasoningContent != "" {
				sendFn(map[string]any{
					"type":    "thinking",
					"content": info.ReasoningContent,
				})
			}
		},
	})
	if err != nil {
		// Container was started but agent construction failed — clean up now
		// so the container doesn't outlive this call.
		if sandboxCleanup != nil {
			sandboxCleanup() //nolint:errcheck
		}
		if mcpCleanup != nil {
			mcpCleanup()
		}
		return nil, nil, nil, nil, err
	}

	return agent, sandboxCleanup, mcpCleanup, approver, nil
}

// ── WebSocket Types ────────────────────────────────────────────────────

type wsAttachment struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type wsClientMsg struct {
	Type        string         `json:"type"`
	Content     string         `json:"content"`
	SessionID   string         `json:"session_id"`
	AuthToken   string         `json:"auth_token,omitempty"`
	Model       string         `json:"model,omitempty"`
	Thinking    string         `json:"thinking,omitempty"` // "enabled" | "" — per-query toggle
	Attachments []wsAttachment `json:"attachments,omitempty"`
}

// ── WebSocket Handler ──────────────────────────────────────────────────

func handleWS(store *session.Store, resources *resource.Registry, resolved config.ResolvedConfig, system string, conn *golangws.Conn) {
	// Release the connection slot acquired by wsHandshakeWithLimits. This runs
	// after the handler exits, whether normally or via panic/close.
	defer func() {
		select {
		case <-wsConnSem:
		default:
		}
	}()

	// Register for graceful-shutdown tracking before anything else.
	// serveOnListener closes all tracked connections on SIGINT/SIGTERM,
	// which unblocks the Receive loop below and lets defers run.
	wsHandlerWG.Add(1)
	wsConns.Store(conn, struct{}{})
	defer func() {
		wsConns.Delete(conn)
		wsHandlerWG.Done()
	}()
	defer conn.Close()

	// Cap incoming message size to prevent a local client from exhausting
	// server memory with a single huge frame.
	conn.MaxPayloadBytes = maxWSMessageBytes

	// Create ONE agent per WebSocket connection — provides buffer
	// continuity across turns within the same session.
	agent, sandboxCleanup, mcpCleanup, approver, err := newServeAgent(resolved, system, func(v any) error {
		writeWSJSON(conn, v)
		return nil
	})
	if err != nil {
		writeWSError(conn, fmt.Sprintf("agent: %v", err))
		return
	}
	defer agent.Close()
	if approver != nil {
		defer approver.Cancel() // release any pending approval on disconnect
	}
	// sandboxCleanup is the primary cleanup path for the Docker container.
	// newServeAgent does NOT pass SandboxCleanup to odek.New (to avoid a
	// double docker rm -f), so agent.Close() does not destroy the container —
	// this defer is the sole cleanup owner.
	if sandboxCleanup != nil {
		defer sandboxCleanup()
	}
	if mcpCleanup != nil {
		defer mcpCleanup()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Connection-scoped context, cancelled the moment the socket reader exits
	// (disconnect or shutdown). Prompts run under this context, so a prompt
	// in flight when the client disconnects is aborted promptly, and any
	// prompts already buffered in promptCh run against a cancelled context
	// instead of making real LLM calls whose output streams to a dead socket.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// Dedicated socket reader.
	//
	// The agent loop (handlePrompt → RunWithMessages) runs synchronously in the
	// processor loop below and BLOCKS whenever a tool needs approval — the
	// wsApprover waits for the browser's approval_response. If the same
	// goroutine both ran the agent and read the socket, it could never read
	// that response: every approval would dead-block until the 60s timeout and
	// be denied, making the Web UI's safety prompt unusable.
	//
	// So a separate goroutine owns the socket. It handles approval responses
	// INLINE (delivering them to the blocked PromptCommand via HandleResponse)
	// and forwards every other message to promptCh for serial processing. The
	// forward is non-blocking: the reader must never stall, or a full queue
	// would re-introduce the deadlock by stopping approval delivery. A buffered
	// queue absorbs the request/reply UI's normal pacing; an overflow only
	// happens under a client flooding prompts, which is reported and dropped.
	promptCh := make(chan []byte, 8)
	go func() {
		defer close(promptCh)
		for {
			var data []byte
			if err := golangws.Message.Receive(conn, &data); err != nil {
				// Socket gone: cancel in-flight/queued prompt work and release
				// any pending approval so a blocked handlePrompt returns now
				// instead of waiting out the 60s timeout.
				connCancel()
				if approver != nil {
					approver.Cancel()
				}
				return
			}

			var msgType struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &msgType); err != nil {
				continue
			}

			// Approval responses are handled here, off the processor goroutine,
			// so a prompt blocked awaiting approval can be unblocked. This is
			// the crux of the deadlock fix.
			if msgType.Type == "approval_response" {
				var resp approvalResponse
				if err := json.Unmarshal(data, &resp); err == nil {
					approver.HandleResponse(resp.ID, resp.Action)
				}
				continue
			}

			select {
			case promptCh <- data:
			default:
				// Processor is busy and the queue is full — only reachable by a
				// client sending prompts faster than they can run.
				if msgType.Type == "prompt" {
					writeWSError(conn, "busy: a prompt is already running")
				}
			}
		}
	}()

	// Track the current session and model across WebSocket messages
	var currentSession *session.Session
	currentModel := resolved.Model // start with configured model

	// Session-level token economics (cumulative across all turns)
	var sessionInputTokens, sessionOutputTokens int

	for data := range promptCh {
		// Peek at the message type without full unmarshal
		var msgType struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msgType); err != nil {
			continue
		}

		// Handle skill prompt responses (Save/Skip from skill suggestions)
		if msgType.Type == "skill_prompt_response" {
			var resp struct {
				Action    string `json:"action"` // "save" or "skip"
				SkillName string `json:"skill_name"`
			}
			if err := json.Unmarshal(data, &resp); err == nil && resp.SkillName != "" {
				if resp.Action == "save" && agent.SkillManager() != nil {
					userDir := expandHome("~/.odek/skills")
					os.MkdirAll(userDir, 0755)
					// We don't have the full suggestion stored — the save needs to
					// happen immediately when suggested. For now, we acknowledge.
					writeWSJSON(conn, map[string]any{
						"type":       "skill_event",
						"event":      "saved",
						"skill_name": resp.SkillName,
					})
				}
			}
			continue
		}

		// Only process prompt messages
		if msgType.Type != "prompt" {
			continue
		}

		var msg wsClientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			writeWSError(conn, "invalid JSON")
			continue
		}

		if msg.Content == "" {
			continue
		}

		// Handle runtime model switching
		if msg.Model != "" && msg.Model != currentModel {
			currentModel = msg.Model
			resolved.Model = msg.Model
			agent.SwitchModel(msg.Model)
		}

		// Handle per-query thinking toggle. The UI sends "enabled" to turn
		// thinking on for this prompt, or "" to use the server default.
		// This is applied before every RunWithMessages call so the model
		// uses the user's current toggle state, not just the startup config.
		agent.SwitchThinking(msg.Thinking)

		// Handle session switch mid-connection (new conversation)
		if msg.SessionID != "" && (currentSession == nil || currentSession.ID != msg.SessionID) {
			sess, err := store.Load(msg.SessionID)
			if err != nil {
				writeWSError(conn, "session not found")
				continue
			}
			if _, ok := validateSessionToken(store, sess, msg.AuthToken); !ok {
				writeWSError(conn, "invalid session token")
				continue
			}
			currentSession = sess
			// Restore buffer from the resumed session
			if mm := agent.Memory(); mm != nil && len(sess.Buffer) > 0 {
				mm.RestoreBuffer(sess.Buffer)
			}
		}

		// Run prompt — passes the persistent agent for buffer continuity
		// Create a cancelable context for this prompt (so POST /api/cancel can abort it).
		// Derived from connCtx so a disconnect also aborts the running prompt.
		promptCtx, promptCancel := context.WithCancel(connCtx)

		currentSession = handlePrompt(promptCtx, conn, store, resources, resolved, agent, currentSession, msg, &sessionInputTokens, &sessionOutputTokens, promptCancel)

		// Cancel the prompt context once the run is complete.
		promptCancel()
	}

	// WebSocket disconnected — extract episode if enough turns
	if currentSession != nil {
		if mm := agent.Memory(); mm != nil {
			msgStrs := makeSessionMessageStrings(currentSession)
			prov := memory.DeriveProvenance(currentSession.Messages)
			mm.OnSessionEndWithProvenance(currentSession.ID, currentSession.Turns, msgStrs, prov)
		}
	}
}

// ── Prompt Handler ─────────────────────────────────────────────────────

// handlePrompt processes a single user prompt within a WebSocket connection.
// Uses the persistent agent (for buffer continuity) and manages session state.
// Returns the updated session (may be a new session for first prompts).
func handlePrompt(
	ctx context.Context,
	conn *golangws.Conn,
	store *session.Store,
	resources *resource.Registry,
	resolved config.ResolvedConfig,
	agent *odek.Agent,
	currSess *session.Session,
	msg wsClientMsg,
	sessionInputTokens, sessionOutputTokens *int,
	promptCancel context.CancelFunc,
) *session.Session {
	prompt := msg.Content
	sessionID := msg.SessionID

	// Server-side cap on prompt size (finding #69). A client can already send
	// up to the WebSocket frame cap; reject anything above a reasonable prompt
	// limit before storing it in the session or forwarding it to the LLM.
	if len(prompt) > maxPromptBytes {
		writeWSError(conn, "prompt exceeds maximum size")
		return currSess
	}

	// Server-side validation of model IDs from the UI (finding #81). Model IDs
	// are passed through to the LLM client and logged; cap length and reject
	// control / unusual characters to prevent oversized payloads or injection.
	if msg.Model != "" {
		if len(msg.Model) > maxModelIDBytes || !modelIDPattern.MatchString(msg.Model) {
			writeWSError(conn, "invalid model ID")
			return currSess
		}
	}

	// Resolve @ references
	refs := resource.ParseRefs(prompt)
	resolvedRefs := make(map[string]string)
	for _, ref := range refs {
		content, err := resources.Load(ctx, ref.Raw)
		if err != nil {
			continue
		}
		resolvedRefs[ref.Raw] = wrapUntrusted(ctx, "resource:"+ref.Raw, content)
	}
	enrichedPrompt := resource.ReplaceRefs(prompt, resolvedRefs)

	// Web UI file attachments cross the browser trust boundary. Wrap each one
	// with the same nonce'd untrusted boundary used for tool output before
	// injecting them into the prompt, so a malicious attachment cannot be
	// mistaken for system instructions.
	if len(msg.Attachments) > 0 {
		const maxAttachmentBytes = 5 * 1024 * 1024
		const maxTotalAttachmentBytes = 10 * 1024 * 1024
		var total int
		var wrapped []string
		for _, att := range msg.Attachments {
			if att.Name == "" || att.Content == "" {
				continue
			}
			if len(att.Content) > maxAttachmentBytes {
				writeWSError(conn, "attachment too large: "+att.Name)
				return currSess
			}
			total += len(att.Content)
			if total > maxTotalAttachmentBytes {
				writeWSError(conn, "total attachment size exceeds 10 MB")
				return currSess
			}
			header := "--- " + att.Name + " ---\n"
			wrapped = append(wrapped, wrapUntrusted(ctx, "attachment:"+att.Name, header+att.Content))
		}
		if len(wrapped) > 0 {
			if enrichedPrompt != "" {
				enrichedPrompt = strings.Join(wrapped, "\n\n") + "\n\n" + enrichedPrompt
			} else {
				enrichedPrompt = strings.Join(wrapped, "\n\n")
			}
		}
	}

	// Load or create session
	var sess *session.Session
	var err error

	if sessionID != "" {
		sess, err = store.Load(sessionID)
		if err != nil {
			sess = nil
		}
	}

	// Build message history
	var messages []llm.Message
	isNewSession := false

	if sess != nil {
		messages = sess.GetMessages()
		messages = append(messages, llm.Message{Role: "user", Content: enrichedPrompt})
	} else {
		isNewSession = true
		messages = []llm.Message{
			{Role: "system", Content: ""},
			{Role: "user", Content: enrichedPrompt},
		}

		// Persist new session
		newSess, err := store.Create(
			[]llm.Message{{Role: "system", Content: ""}},
			resolved.Model,
			shorten(prompt, 60),
		)
		if err == nil {
			sess = newSess
			sess.Sandbox = resolved.Sandbox
			store.Save(sess)
		}
	}

	// Send session info
	sid := ""
	authToken := ""
	if sess != nil {
		sid = sess.ID
		authToken = sess.AuthToken
	}
	// Register the cancel function for this session so the HTTP endpoint can
	// abort this specific prompt. Unregister as soon as the run finishes.
	if sid != "" && promptCancel != nil {
		registerPromptCancel(sid, promptCancel)
		defer unregisterPromptCancel(sid)
	}
	writeWSJSON(conn, map[string]any{"type": "session", "session_id": sid, "auth_token": authToken, "model": resolved.Model, "sandbox": resolved.Sandbox})

	// Append user input to buffer (AppendBuffer summarizes raw text).
	if mm := agent.Memory(); mm != nil {
		mm.AppendBuffer("user", prompt)
	}

	// Run agent. Audit recorder wired around the loop so every
	// wrapUntrusted call inside the agent's tool invocations is logged
	// to this session's audit log under <sessions>/audit/.
	auditStore := session.NewAuditStore(store.Dir())
	var auditSessID string
	var auditTurn int
	if sess != nil {
		auditSessID = sess.ID
		auditTurn = sess.Turns + 1
	} else if currSess != nil {
		auditSessID = currSess.ID
		auditTurn = currSess.Turns + 1
	}
	if auditSessID != "" {
		// Scope the ingest recorder to this prompt's context so concurrent
		// sessions cannot overwrite each other's audit attribution.
		ctx = loop.WithIngestRecorder(ctx, func(source, content string) {
			_ = auditStore.RecordIngest(auditSessID, auditTurn, source, content)
		})
	}

	origLen := len(messages) - 1 // initial estimate: index of the user message we appended
	start := time.Now()
	_, allMessages, err := agent.RunWithMessages(ctx, messages)
	latency := time.Since(start)

	if err != nil {
		writeWSError(conn, err.Error())
		return currSess // return unchanged session on error
	}

	// Dynamic injections (skills, memory, episodes) insert extra system messages
	// BEFORE the user turn during RunWithMessages, shifting its index in allMessages
	// beyond the pre-run origLen estimate. Search forward to find where the new
	// user message actually landed, so newMsgs starts exactly there.
	for i := origLen; i < len(allMessages); i++ {
		if allMessages[i].Role == "user" {
			origLen = i
			break
		}
	}

	// Record per-turn divergence assessment.
	if auditSessID != "" {
		recordTurnAudit(auditStore, auditSessID, auditTurn, prompt, allMessages[origLen:])
	}

	// New messages = user message we added + everything the agent appended.
	newMsgs := allMessages[origLen:]

	// Stream the final assistant response.
	//
	// WHAT IS STREAMED AND WHAT IS NOT
	//
	// Tool events (tool_call / tool_result) already fired live during
	// RunWithMessages via ToolEventHandler — skip them here.
	//
	// Assistant messages with ToolCalls are intermediate "thinking + act"
	// turns. Their Content (e.g. "Let me check that file…") was narrated
	// live via the IterationCallback progress bubble; re-sending it here
	// would make it appear *after* all tool blocks in the response bubble,
	// which is confusing. Skip their Content.
	//
	// The final assistant message (no ToolCalls) carries:
	//   • ReasoningContent — the model's private reasoning for this turn.
	//     The IterationCallback does NOT send reasoning for the final-answer
	//     turn (its callback fires with IsPreTool=false and empty
	//     ReasoningContent per loop.go:719). We send it here as a "thinking"
	//     event so the UI can display it in a collapsible block.
	//   • Content — the actual response text. Send as "token" events.
	for _, msg := range newMsgs {
		if msg.Role != "assistant" {
			continue
		}
		isFinalAnswer := len(msg.ToolCalls) == 0

		if !isFinalAnswer {
			// Intermediate turn — tool_call/tool_result events already streamed.
			// Skip Content to avoid duplicating the narrative after tool blocks.
			continue
		}

		// Final answer: send reasoning as a thinking event first (if present),
		// then stream the response text.
		if msg.ReasoningContent != "" {
			writeWSJSON(conn, map[string]any{"type": "thinking", "content": msg.ReasoningContent})
		}
		if msg.Content != "" {
			writeWSJSON(conn, map[string]any{"type": "token", "content": msg.Content})
		}
	}

	// Find the assistant response for buffer
	if mm := agent.Memory(); mm != nil {
		for _, msg := range newMsgs {
			if msg.Role == "assistant" && msg.Content != "" {
				mm.AppendBuffer("agent", msg.Content)
				break
			}
		}
	}

	contextTokens := agent.TotalInputTokens()
	outputTokens := agent.TotalOutputTokens()
	cacheCreate := agent.TotalCacheCreationTokens()
	cacheRead := agent.TotalCacheReadTokens()
	cached := agent.TotalCachedTokens()
	*sessionInputTokens += contextTokens
	*sessionOutputTokens += outputTokens

	writeWSJSON(conn, map[string]any{
		"type":                 "done",
		"latency":              latency.Seconds(),
		"contextTokens":        contextTokens,
		"outputTokens":         outputTokens,
		"cacheCreationTokens":  cacheCreate,
		"cacheReadTokens":      cacheRead,
		"cachedTokens":         cached,
		"sessionContextTokens": *sessionInputTokens,
		"sessionOutputTokens":  *sessionOutputTokens,
	})

	// Save session — persist messages AND buffer.
	// Filter out dynamically-injected system messages (skills, memory, episodes)
	// so they are not stored in the session and don't corrupt future origLen
	// calculations on subsequent turns. Only user/assistant/tool messages persist.
	if sess != nil {
		if mm := agent.Memory(); mm != nil {
			sess.Buffer = mm.GetBuffer()
		}
		var toStore []llm.Message
		for _, m := range newMsgs {
			if m.Role != "system" {
				toStore = append(toStore, m)
			}
		}
		store.Append(sess.ID, toStore)
	}

	// ── Learn loop: run self-improvement heuristics ──
	if agent.SkillManager() != nil {
		sm := agent.SkillManager()
		suggestions := learnAndSuggest(allMessages, sm, nil, false, resolved.Skills.AutoSave.Enabled)
		if len(suggestions) > 0 {
			userDir := expandHome("~/.odek/skills")
			os.MkdirAll(userDir, 0755)
			filtered, skipped := skills.FilterSkipped(suggestions, userDir,
				resolved.Skills.Curation.SkipThreshold, resolved.Skills.Curation.SkipResetDays)
			_ = skipped
			if resolved.Skills.AutoSave.Enabled {
				result := skills.AutoSaveSuggestions(filtered, userDir, resolved.Skills, false)
				for _, name := range result.Saved {
					sm.Notifier.Notify(skills.SkillEvent{
						Type: "saved", SkillName: name, Timestamp: time.Now().UTC(),
					})
				}
				if len(result.Saved) > 0 {
					sm.MarkDirty()
					sm.Reload()
				}
			}
		}
	}

	// If we started a new session, return it so the WebSocket loop
	// tracks it for future turns and OnSessionEnd.
	if isNewSession && sess != nil {
		return sess
	}
	return currSess
}

// ── WebSocket Stream Writer ─────────────────────────────────────────────

type wsStreamWriter struct {
	conn *golangws.Conn
}

func (w *wsStreamWriter) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	if text == "" {
		return len(p), nil
	}
	writeWSJSON(w.conn, map[string]any{"type": "token", "content": text})
	return len(p), nil
}

// ── WS Helpers ─────────────────────────────────────────────────────────

// checkLocalOrigin rejects WebSocket upgrades from non-local origins so a
// page open elsewhere in the user's browser cannot drive the agent or
// approve dangerous tool calls. The default policy allows any port on
// localhost / 127.0.0.1 / [::1] and an empty Origin (curl, native
// clients). See IMPROVEMENTS_ROADMAP.md S-M1.
//
// Note: this check is now defense-in-depth. The primary CSRF protection is
// the per-instance wsToken validated by validateServeToken.
func checkLocalOrigin(_ *golangws.Config, req *http.Request) error {
	origin := req.Header.Get("Origin")
	if origin == "" {
		return nil // non-browser clients (curl, ws CLI) — no Origin to forge
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid Origin %q", origin)
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("Origin %q not allowed (only localhost is accepted)", origin)
}

const (
	wsTokenCookieName     = "odek_ws_token"
	wsTokenHeaderName     = "X-Odek-Ws-Token"
	wsTokenProtocolPrefix = "odek."
)

// newServeToken generates a 256-bit random token used to authenticate
// browser WebSocket upgrades. The token is issued once per server process.
func newServeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// validateServeToken verifies that the browser has the per-instance CSRF
// token. It accepts the token via:
//   - the HttpOnly SameSite=Strict cookie set when serving index.html
//     (automatic for legitimate same-origin pages),
//   - an X-Odek-Ws-Token header (for non-browser clients), or
//   - a WebSocket subprotocol of the form "odek.<token>" (for clients that
//     can set Sec-WebSocket-Protocol).
func validateServeToken(cfg *golangws.Config, req *http.Request, token string) error {
	if token == "" {
		return fmt.Errorf("server token not configured")
	}

	// Cookie (browser same-origin / same-site requests).
	if c, err := req.Cookie(wsTokenCookieName); err == nil && c.Value != "" {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1 {
			return nil
		}
	}

	// Explicit header (non-browser clients).
	if h := req.Header.Get(wsTokenHeaderName); h != "" {
		if subtle.ConstantTimeCompare([]byte(h), []byte(token)) == 1 {
			return nil
		}
	}

	// WebSocket subprotocol.
	for _, p := range cfg.Protocol {
		if strings.HasPrefix(p, wsTokenProtocolPrefix) {
			got := strings.TrimPrefix(p, wsTokenProtocolPrefix)
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
				return nil
			}
		}
	}

	return fmt.Errorf("missing or invalid server token")
}

// wsHandshakeWithLimits validates the CSRF token and checks the origin, then
// applies a per-IP upgrade rate limit and acquires the global
// concurrent-connection semaphore. The semaphore is acquired before the
// WebSocket handshake completes and released when the handler exits.
func wsHandshakeWithLimits(cfg *golangws.Config, req *http.Request, token string) error {
	if err := validateServeToken(cfg, req, token); err != nil {
		return err
	}
	if err := checkLocalOrigin(nil, req); err != nil {
		return err
	}
	if !wsUpgradeLimiter.allow(clientIP(req)) {
		return fmt.Errorf("WebSocket upgrade rate limit exceeded")
	}
	select {
	case wsConnSem <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("too many concurrent WebSocket connections")
	}
}

// requireLocalOrigin rejects cross-origin state-changing requests to the REST
// API. It is the HTTP counterpart to checkLocalOrigin.
func requireLocalOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStateChangingMethod(r.Method) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil {
					http.Error(w, "invalid Origin", http.StatusForbidden)
					return
				}
				host := u.Hostname()
				if host != "localhost" && host != "127.0.0.1" && host != "::1" {
					http.Error(w, "Origin not allowed", http.StatusForbidden)
					return
				}
			}
		}
		w.Header().Set("Vary", "Origin")
		next.ServeHTTP(w, r)
	})
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// sessionTokenFromRequest returns the session auth token from the
// X-Session-Token header or the session_token cookie, in that order.
func sessionTokenFromRequest(r *http.Request) string {
	if t := r.Header.Get("X-Session-Token"); t != "" {
		return t
	}
	if c, err := r.Cookie("session_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// validateSessionToken checks the provided token against the session. If the
// session has no token (legacy session created before this defense), a token is
// generated and the session is persisted. The returned string is the effective
// token (empty only when validation failed). The bool indicates success.
func validateSessionToken(store *session.Store, sess *session.Session, token string) (string, bool) {
	if sess == nil {
		return "", false
	}
	if sess.AuthToken == "" {
		sess.AuthToken = session.GenerateAuthToken()
		if err := store.Save(sess); err != nil {
			// If we cannot persist the token, still allow this request but do not
			// leak a transient token to the client.
			return "", true
		}
		return sess.AuthToken, true
	}
	// Constant-time comparison so an attacker cannot recover the token byte by
	// byte via response-timing differences.
	if subtle.ConstantTimeCompare([]byte(token), []byte(sess.AuthToken)) == 1 {
		return sess.AuthToken, true
	}
	return "", false
}

func writeWSJSON(conn *golangws.Conn, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	golangws.Message.Send(conn, string(payload))
}

func writeWSError(conn *golangws.Conn, msg string) {
	writeWSJSON(conn, map[string]string{"type": "error", "message": msg})
}

// ── API Handlers ───────────────────────────────────────────────────────

func handleResourceSearch(reg *resource.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 10
		}
		// Registry.Search also enforces a global maximum; this explicit cap
		// keeps the HTTP handler's contract obvious and prevents huge JSON
		// responses even if a caller bypasses the UI.
		const maxResourceLimit = 100
		if limit > maxResourceLimit {
			limit = maxResourceLimit
		}

		results, err := reg.Search(r.Context(), q, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if results == nil {
			results = []resource.Resource{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	}
}

func handleSessionList(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := store.List(50)
		if err != nil {
			sessions = []session.Session{}
		}
		if sessions == nil {
			sessions = []session.Session{}
		}

		// Never leak session-scoped auth tokens in the list endpoint. Tokens are
		// only returned (in the X-Session-Token header) after a valid detail lookup.
		for i := range sessions {
			sessions[i].AuthToken = ""
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}

func handleSessionByID(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		if id == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Rate-limit session detail lookups per IP to slow brute-force
			// enumeration of the 128-bit ID space.
			if !sessionLookupLimiter.allow(clientIP(r)) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			sess, err := store.Load(id)
			if err != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			token := sessionTokenFromRequest(r)
			effectiveToken, ok := validateSessionToken(store, sess, token)
			if !ok {
				http.Error(w, "invalid session token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if effectiveToken != "" {
				w.Header().Set("X-Session-Token", effectiveToken)
			}
			json.NewEncoder(w).Encode(sess)

		case http.MethodDelete:
			sess, err := store.Load(id)
			if err != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			token := sessionTokenFromRequest(r)
			if _, ok := validateSessionToken(store, sess, token); !ok {
				http.Error(w, "invalid session token", http.StatusUnauthorized)
				return
			}
			if err := store.Delete(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPost:
			// Rename session
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			sess, err := store.Load(id)
			if err != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			token := sessionTokenFromRequest(r)
			if _, ok := validateSessionToken(store, sess, token); !ok {
				http.Error(w, "invalid session token", http.StatusUnauthorized)
				return
			}
			sess.Task = body.Name
			store.Save(sess)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(sess)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// clientIP returns a best-effort client identifier for rate limiting. It prefers
// X-Forwarded-For / X-Real-Ip only when the direct remote address is a loopback
// proxy, otherwise uses RemoteAddr. This avoids trusting spoofed headers from
// arbitrary clients while still working behind a local reverse proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			if i := strings.Index(fwd, ","); i > 0 {
				return strings.TrimSpace(fwd[:i])
			}
			return strings.TrimSpace(fwd)
		}
		if real := r.Header.Get("X-Real-Ip"); real != "" {
			return real
		}
	}
	return host
}

func handleModelList(configuredModel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		type modelEntry struct {
			ID          string `json:"id"`
			MaxContext  int    `json:"max_context"`
			Description string `json:"description,omitempty"`
			Current     bool   `json:"current,omitempty"`
		}
		var models []modelEntry

		// Return only the server's configured model. The UI provides an
		// "Other…" free-text input for switching to any arbitrary model ID.
		if configuredModel != "" {
			if p := odek.LookupProfile(configuredModel); p != nil {
				ctx := p.MaxContext / 1024
				label := p.Label
				if label == "" {
					label = configuredModel
				}
				models = append(models, modelEntry{
					ID:          configuredModel,
					MaxContext:  p.MaxContext,
					Description: fmt.Sprintf("%s — %dK ctx", label, ctx),
					Current:     true,
				})
			} else {
				models = append(models, modelEntry{
					ID:          configuredModel,
					Description: configuredModel,
					Current:     true,
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models)
	}
}

// handleCancel cancels the running prompt for the requested session.
// POST /api/cancel?session_id=<id> — cancels the agent execution scoped to
// that session. Requiring the session ID prevents one connection from
// cancelling another connection's prompt.
func handleCancel(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "missing session_id", http.StatusBadRequest)
			return
		}

		sess, err := store.Load(sessionID)
		if err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if _, ok := validateSessionToken(store, sess, sessionTokenFromRequest(r)); !ok {
			http.Error(w, "invalid session token", http.StatusUnauthorized)
			return
		}

		cancelPrompt(sessionID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── Static Handler ─────────────────────────────────────────────────────

// staticFiles maps URL paths to embedded file paths and their content types.
var staticFiles = map[string][2]string{
	"/":          {"ui/index.html", "text/html; charset=utf-8"},
	"/style.css": {"ui/style.css", "text/css; charset=utf-8"},
	"/app.js":    {"ui/app.js", "application/javascript; charset=utf-8"},
}

func handleStatic(wsToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Browsers auto-request favicon.ico — serve a minimal SVG inline.
		if r.URL.Path == "/favicon.ico" {
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><text y="14" font-size="14">⚡</text></svg>`))
			return
		}
		entry, ok := staticFiles[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := uiFS.ReadFile(entry[0])
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// The HTML entry point gets the per-instance CSRF token both as a
		// cookie (sent automatically on same-site WebSocket upgrades) and as a
		// meta tag (read by app.js and sent as a WebSocket subprotocol).
		if r.URL.Path == "/" && wsToken != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     wsTokenCookieName,
				Value:    wsToken,
				Path:     "/",
				SameSite: http.SameSiteStrictMode,
				HttpOnly: true,
				// No explicit MaxAge/Expires so the cookie is a session cookie.
			})
			data = []byte(strings.Replace(string(data), "{{ODEK_WS_TOKEN}}", wsToken, 1))
		}

		w.Header().Set("Content-Type", entry[1])
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Write(data)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

func makeSessionMessageStrings(sess *session.Session) []string {
	msgs := sess.GetMessages()
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role+": "+m.Content)
	}
	return out
}

func openInBrowser(url string) {
	cmds := []string{"xdg-open", "open", "gnome-open"}
	for _, cmd := range cmds {
		if _, err := os.Stat("/usr/bin/" + cmd); err == nil {
			attr := &os.ProcAttr{Files: []*os.File{nil, nil, nil}}
			os.StartProcess(cmd, []string{cmd, url}, attr)
			return
		}
	}
}
