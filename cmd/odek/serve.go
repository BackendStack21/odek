package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/redact"
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

// compactionDigestPrefix mirrors the unexported digestMsgPrefix in
// internal/loop/loop.go (kept as a literal here to avoid widening the
// loop package's API surface). Rolling-compaction digest system messages
// must survive the per-turn persist filter — dropping them would make a
// resumed serve session lose its compacted history.
const compactionDigestPrefix = "[Compacted earlier context:"

// planMessagePrefix mirrors the unexported planMsgPrefix in
// internal/loop/plan.go. Protected plan system messages must survive the
// per-turn persist filter just like digests — dropping them would make a
// resumed serve session lose its forward-state plan (docs/PLANNING.md).
const planMessagePrefix = "[Current plan:"

// filterPersistSnapshot selects which messages of a per-turn snapshot are
// persisted for a serve session. The session's own leading system message
// (head) is preserved; dynamically-injected system messages (skills,
// memory, episodes, trim warnings) are dropped so persisted snapshots
// don't accumulate internal injections or corrupt future origLen
// calculations; compaction digest and protected plan system messages are
// kept so a resumed session retains its compacted history and its plan.
func filterPersistSnapshot(head, snapshot []llm.Message) []llm.Message {
	filtered := make([]llm.Message, 0, len(snapshot))
	filtered = append(filtered, head...)
	for i, m := range snapshot {
		if i == 0 && len(head) > 0 {
			continue // replaced by the session's original head
		}
		if m.Role == "system" &&
			!strings.HasPrefix(m.Content, compactionDigestPrefix) &&
			!strings.HasPrefix(m.Content, planMessagePrefix) {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}

// providerFailureSummary renders a short, safe failure classification for
// the persisted turn-abort note. It deliberately excludes the provider's raw
// error body — it can be large and may echo prompt content back into the
// transcript. Typed llm errors (rate limit) get a precise, actionable line;
// everything else is truncated to a single line.
func providerFailureSummary(err error) string {
	var rle *llm.RateLimitError
	if errors.As(err, &rle) {
		return fmt.Sprintf("provider rate limit (HTTP %d) after %d attempts", rle.StatusCode, rle.Attempts)
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return strings.ToValidUTF8(msg, "")
}

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
// promptCancelEntry pairs a cancel func with a generation counter so an
// earlier prompt's unregister cannot delete a newer prompt's registration.
type promptCancelEntry struct {
	cancel context.CancelFunc
	gen    int64
}

var (
	promptCancelMu  sync.Mutex
	promptCancels   = map[string]*promptCancelEntry{}
	promptCancelGen int64
)

// registerPromptCancel records cancel as the active cancel function for
// sessionID. The returned unregister func removes it ONLY if it is still
// the live registration — when two prompts run on the same session, the
// first finisher must not strip the second's cancel func.
func registerPromptCancel(sessionID string, cancel context.CancelFunc) (unregister func()) {
	if sessionID == "" || cancel == nil {
		return func() {}
	}
	gen := atomic.AddInt64(&promptCancelGen, 1)
	promptCancelMu.Lock()
	promptCancels[sessionID] = &promptCancelEntry{cancel: cancel, gen: gen}
	promptCancelMu.Unlock()

	return func() {
		promptCancelMu.Lock()
		if cur, ok := promptCancels[sessionID]; ok && cur.gen == gen {
			delete(promptCancels, sessionID)
		}
		promptCancelMu.Unlock()
	}
}

// unregisterPromptCancel removes whatever cancel function is currently
// registered for sessionID. Prefer the unregister closure returned by
// registerPromptCancel; this variant is kept for callers that don't track
// their registration generation.
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
	entry, ok := promptCancels[sessionID]
	promptCancelMu.Unlock()
	if !ok {
		return false
	}
	var cancel context.CancelFunc
	if entry != nil {
		cancel = entry.cancel
	}
	if cancel == nil {
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
	if key == "" {
		// No identifiable client (e.g. a request with no usable RemoteAddr):
		// do not track a shared "" bucket — its map entry would never be
		// evicted, and unidentified callers would exhaust each other's
		// budget. Skip limiting instead of inserting an empty key.
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
	var compaction *bool
	var planning *bool
	var stream *bool
	var sandboxImage, sandboxNetwork, sandboxMemory, sandboxCPUs, sandboxUser string
	var toolsEnabled, toolsDisabled, trustedProxies []string
	var logFile string

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
		case "--compaction":
			compaction = boolPtr(true)
		case "--planning":
			planning = boolPtr(true)
		case "--no-planning":
			planning = boolPtr(false)
		case "--stream":
			stream = boolPtr(true)
		case "--no-stream":
			stream = boolPtr(false)
		case "--tool":
			i++
			if i >= len(args) {
				return fmt.Errorf("--tool requires a value")
			}
			toolsEnabled = append(toolsEnabled, args[i])
		case "--no-tool":
			i++
			if i >= len(args) {
				return fmt.Errorf("--no-tool requires a value")
			}
			toolsDisabled = append(toolsDisabled, args[i])
		case "--trusted-proxies":
			i++
			if i >= len(args) {
				return fmt.Errorf("--trusted-proxies requires a value")
			}
			trustedProxies = strings.Split(args[i], ",")
			for j := range trustedProxies {
				trustedProxies[j] = strings.TrimSpace(trustedProxies[j])
			}
		case "--log-file":
			i++
			if i >= len(args) {
				return fmt.Errorf("--log-file requires a value")
			}
			logFile = args[i]
		default:
			return fmt.Errorf("unknown flag %q for serve", args[i])
		}
	}

	resolved := config.LoadConfig(config.CLIFlags{
		Sandbox:         sandbox,
		PromptCaching:   promptCaching,
		Compaction:      compaction,
		Planning:        planning,
		Stream:          stream,
		SandboxImage:    sandboxImage,
		SandboxNetwork:  sandboxNetwork,
		SandboxReadonly: sandboxReadonly,
		SandboxMemory:   sandboxMemory,
		SandboxCPUs:     sandboxCPUs,
		SandboxUser:     sandboxUser,
		ToolsEnabled:    toolsEnabled,
		ToolsDisabled:   toolsDisabled,
		TrustedProxies:  trustedProxies,
	})
	if err := approveProjectSandbox(resolved, os.Stdin, os.Stdout); err != nil {
		return err
	}
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

	// Durable run/turn log (fix 4 of the sub-agent reliability work): every
	// serve turn and headless run is recorded with IDs, statuses, and short
	// failure classifications so provider failures (429 saturation) are
	// visible after the fact. Default ~/.odek/serve.log — a sibling of
	// telegram.log/schedule.log so the storage janitor's rotation covers it.
	logPath := logFile
	if logPath == "" {
		logPath = filepath.Join(home, ".odek", "serve.log")
	}
	if sl, err := openServeLog(logPath); err == nil {
		setServeLog(sl)
		defer sl.Close()
		sl.logf("serve_started addr=%s pid=%d", addr, os.Getpid())
	} else {
		fmt.Fprintf(os.Stderr, "odek serve: durable run log disabled (%v)\n", err)
	}

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

	memoryDir := expandHome("~/.odek/memory")
	state := &serveState{startedAt: time.Now(), resolved: resolved}

	// ONE background-command manager for the whole serve process: agents
	// are per connection/run, but jobs must outlive them. Nil when the
	// feature is disabled (or in sandbox mode — see newServeBGManager).
	bgMgr := newServeBGManager(resolved)
	setServeBGManager(bgMgr)

	mux := newServeMux(serveMuxDeps{
		Store:         store,
		Resources:     resourceReg,
		Resolved:      resolved,
		SystemMessage: systemMessage,
		State:         state,
		WsToken:       wsToken,
		MemoryDir:     memoryDir,
		BGManager:     bgMgr,
	})

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// Print the WebSocket token to the console only (Jupyter-style). It is
	// never served over plain HTTP, so a network attacker who can only make
	// `GET /` cannot retrieve it. Browser clients get it via the token URL;
	// non-browser clients can supply it as a header or subprotocol.
	tokenURL := "http://" + listener.Addr().String() + "/?token=" + wsToken
	fmt.Fprintf(os.Stderr, "odek serve ⚡  %s\n", tokenURL)
	fmt.Fprintf(os.Stderr, "  WebSocket: ws://%s/ws\n", listener.Addr())
	fmt.Fprintf(os.Stderr, "  WS token:  %s\n", wsToken)
	fmt.Fprintf(os.Stderr, "  Type @ to reference files, drop or attach files inline.\n")

	if !isLoopbackAddr(listener.Addr()) {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: odek serve is bound to a non-loopback address.\n")
		fmt.Fprintf(os.Stderr, "   Anyone who can reach this port can control the agent once they have the token above.\n")
		fmt.Fprintf(os.Stderr, "   Use --addr 127.0.0.1:<port> (or a firewall) to restrict access.\n\n")
	}

	if openBrowser {
		openInBrowser(tokenURL)
	}

	// Start the storage-maintenance janitor (expired sessions, audit records,
	// plans, skill skips, log rotation) for the life of the server. The
	// context is cancelled when serveOnListener returns on shutdown.
	maintCtx, maintCancel := context.WithCancel(context.Background())
	defer maintCancel()
	startStorageMaintenance(maintCtx, resolved)

	return serveOnListener(listener, mux)
}

// serveMuxDeps carries everything newServeMux needs. serveCmd builds it
// from resolved config; tests build it directly to exercise the EXACT
// production mounting (same routes, same auth wrappers) — there is no
// second mux definition to drift out of sync.
type serveMuxDeps struct {
	Store         *session.Store
	Resources     *resource.Registry
	Resolved      config.ResolvedConfig
	SystemMessage string
	State         *serveState
	WsToken       string
	MemoryDir     string
	// BGManager is the process-scoped background-command manager shared by
	// all agents of this serve instance (nil = feature disabled). Injected
	// so tests can mount the exact production routes with their own manager.
	BGManager *bgproc.Manager
}

// newServeMux builds the odek serve HTTP mux: static UI + WebSocket + the
// full REST management surface, with the per-instance token, loopback Host,
// and local-origin wrappers applied to every /api route.
func newServeMux(d serveMuxDeps) *http.ServeMux {
	resolved := d.Resolved
	store := d.Store
	resourceReg := d.Resources
	wsToken := d.WsToken
	state := d.State
	memoryDir := d.MemoryDir
	systemMessage := d.SystemMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	// serveWSUpgrades closes the handshake-window slot leak: if
	// x/net/websocket fails the upgrade after our Handshake callback (which
	// acquires wsConnSem) returned nil, the Handler — and with it
	// handleWS's release defer — never runs, and the wrapper releases.
	mux.Handle("/ws", serveWSUpgrades(serveWSReal,
		func(cfg *golangws.Config, req *http.Request) error {
			return wsHandshakeWithLimits(cfg, req, wsToken, resolved.TrustedProxies)
		},
		func(conn *golangws.Conn) {
			handleWS(store, resourceReg, resolved, systemMessage, state, conn)
		},
	))
	// All API endpoints require the per-instance CSRF token, a loopback Host,
	// and (for state-changing methods) a local Origin. This blocks DNS-rebinding
	// and cross-site reads of sessions/resources/models.
	apiAuth := func(h http.Handler) http.Handler {
		return requireServeToken(wsToken)(requireLocalHost(requireLocalOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// API responses are dynamic and authenticated — never cacheable.
			// Without this, a browser's heuristic caching can hand a client
			// a stale list right after a mutation (e.g. delete → refresh).
			w.Header().Set("Cache-Control", "no-store")
			h.ServeHTTP(w, r)
		}))))
	}
	mux.Handle("/api/resources", apiAuth(handleResourceSearch(resourceReg)))
	mux.Handle("/api/sessions", apiAuth(handleSessionListPaged(store)))
	mux.Handle("/api/sessions/", apiAuth(handleSessionByID(store, resolved.TrustedProxies, wsToken)))
	mux.Handle("/api/models", apiAuth(handleModelList(resolved.Model)))
	mux.Handle("/api/limits", apiAuth(handleLimits(resolved.Model, resolved.Limits)))
	mux.Handle("/api/cancel", apiAuth(handleCancel(store)))
	mux.Handle("/api/health", apiAuth(handleHealth(state)))
	mux.Handle("/api/memory", apiAuth(handleMemoryGet(memoryDir, resolved.Memory)))
	mux.Handle("/api/memory/facts", apiAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleMemoryFactsAdd(memoryDir, resolved.Memory)(w, r)
		case http.MethodDelete:
			handleMemoryFactsRemove(memoryDir, resolved.Memory)(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})))
	mux.Handle("/api/memory/episodes/promote", apiAuth(handleMemoryEpisodePromote(memoryDir)))
	mux.Handle("/api/skills", apiAuth(handleSkills(resolved.Skills)))
	mux.Handle("/api/skills/promote", apiAuth(handleSkillPromote()))
	mux.Handle("/api/tools", apiAuth(handleTools(resolved)))
	mux.Handle("/api/profiles", apiAuth(handleProfiles()))
	mux.Handle("/api/config", apiAuth(handleConfigView(resolved)))
	mux.Handle("/api/mcp", apiAuth(handleMCPServers(resolved)))

	// Headless runs — same handlePrompt path as WebSocket prompts, with a
	// REST approval bridge. See serve_runs.go.
	mux.Handle("/api/prompt", apiAuth(handlePromptStart(state, store, resourceReg, systemMessage)))
	mux.Handle("/api/runs", apiAuth(handleRunList()))
	mux.Handle("/api/runs/", apiAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/runs/")
		switch {
		case strings.HasSuffix(rest, "/approvals") && r.Method == http.MethodGet:
			handleRunApprovalList()(w, r)
		case strings.Contains(rest, "/approvals/"):
			handleRunApprovalAnswer()(w, r)
		case strings.HasSuffix(rest, "/cancel"):
			handleRunCancel()(w, r)
		default:
			handleRunByID()(w, r)
		}
	})))

	// Background jobs — list/output/stop for the session's background
	// commands (the bg_* tool family). Same apiAuth chain as every /api
	// route; the session scope comes from session_id + X-Session-Token,
	// like /api/cancel. See serve_jobs.go.
	mux.Handle("/api/jobs", apiAuth(handleJobsList(store, d.BGManager)))
	mux.Handle("/api/jobs/", apiAuth(handleJobByID(store, d.BGManager)))

	// Observability + lifecycle.
	mux.Handle("/api/events", apiAuth(handleEvents()))
	mux.Handle("/api/subagents", apiAuth(handleSubagentRegistry()))
	mux.Handle("/api/usage", apiAuth(handleUsage(resolved)))
	mux.Handle("/api/connections", apiAuth(handleConnections()))
	mux.Handle("/api/connections/", apiAuth(handleConnectionKick()))
	mux.Handle("/api/memory/consolidate", apiAuth(handleMemoryConsolidate(memoryDir, resolved)))
	mux.Handle("/api/shutdown", apiAuth(handleShutdown()))
	return mux
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
  --stream                 Stream LLM responses live to the Web UI (token deltas)
  --no-stream              Disable streaming (config/env may enable it)
  --tool name              Enable a tool for the LLM (repeatable)
  --no-tool name           Disable a tool for the LLM (repeatable)
  --trusted-proxies list   Comma-separated IPs/CIDRs whose X-Forwarded-For headers are trusted
  --log-file path          Durable run/turn log (default: ~/.odek/serve.log, mode 0600)
  --help, -h               Show this help`)
}

// serveShutdownCh lets POST /api/shutdown trigger the same graceful drain
// as SIGINT/SIGTERM. Closed at most once.
var (
	serveShutdownOnce sync.Once
	serveShutdownCh   = make(chan struct{})
)

// requestServeShutdown starts the graceful shutdown sequence (stop
// accepting, close WebSockets, drain containers).
func requestServeShutdown() {
	serveShutdownOnce.Do(func() { close(serveShutdownCh) })
}

// serveOnListener serves the odek Web UI on a pre-created listener.
// newServeHTTPServer builds the serve HTTP server. Extracted so tests can
// pin the unauthenticated-connection hardening (audit 2026-08): without a
// header deadline, any local (or remote, on a non-loopback --addr) client
// can hold thousands of half-open connections forever — the rate limiter
// and CSRF token only apply after the request line arrives. Body reads
// stay unbounded so long agent runs and uploads are unaffected (only the
// pre-request header phase and idle keep-alives are timed).
func newServeHTTPServer(mux *http.ServeMux) *http.Server {
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Extracted for testing — allows E2E tests to pass a listener on a known port.
// Handles SIGINT/SIGTERM for graceful shutdown: stops accepting new connections
// and gives in-flight requests up to 5 seconds to finish.
func serveOnListener(listener net.Listener, mux *http.ServeMux) error {
	srv := newServeHTTPServer(mux)

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
	case <-serveShutdownCh:
		fmt.Fprintln(os.Stderr, "\nodek serve: shutdown requested via API, shutting down...")
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

	// Phase 2.5: kill every background job across all sessions — the
	// process-exit contract (no detach). Done before the drain so the job
	// reapers finish inside the drain window.
	if killedBG := shutdownServeBG(); len(killedBG) > 0 {
		fmt.Fprintf(os.Stderr, "odek serve: killed %d background job(s)\n", len(killedBG))
	}

	// Phase 3: wait for all handleWS goroutines and headless REST-run
	// goroutines to finish (up to 10s). Each handleWS goroutine runs defer
	// agent.Close() which calls docker rm -f; run goroutines do the same
	// via their cleanup func.
	if drainServeWork(10 * time.Second) {
		fmt.Fprintln(os.Stderr, "odek serve: all connections closed cleanly")
	} else {
		fmt.Fprintln(os.Stderr, "odek serve: drain timeout — some containers may still be running")
	}

	fmt.Fprintln(os.Stderr, "odek serve: stopped")
	return nil
}

// drainServeWork waits (bounded) for all live WebSocket handler goroutines
// and headless REST-run goroutines to finish. It reports whether the drain
// completed within the timeout. Headless runs are tracked in serveRunsWG —
// without that, a blocking run (approval wait, long agent turn) outlives
// listener shutdown and dies at process exit with its cleanup defers never
// running. Extracted for testing.
func drainServeWork(timeout time.Duration) bool {
	drained := make(chan struct{})
	go func() {
		wsHandlerWG.Wait()
		serveRunsWG.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ── Agent Builder ──────────────────────────────────────────────────────

// wsDeltaCounters tracks streamed-fragment activity for the prompt currently
// executing on a WebSocket connection. Reset at prompt start; consulted by
// the IterationCallback (to suppress the per-iteration reasoning echo while
// deltas are flowing) and after the run (to skip the post-run bulk re-send
// of content the client already received live). When a provider rejects SSE
// and the LLM client falls back to the buffered path, no deltas fire, the
// counters stay zero, and the legacy bulk events are sent as before.
type wsDeltaCounters struct {
	mu        sync.Mutex
	reasoning int
	content   int
}

func (c *wsDeltaCounters) reset() {
	c.mu.Lock()
	c.reasoning, c.content = 0, 0
	c.mu.Unlock()
}

func (c *wsDeltaCounters) addReasoning() {
	c.mu.Lock()
	c.reasoning++
	c.mu.Unlock()
}

func (c *wsDeltaCounters) addContent() {
	c.mu.Lock()
	c.content++
	c.mu.Unlock()
}

func (c *wsDeltaCounters) snapshot() (reasoning, content int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reasoning, c.content
}

func newServeAgent(resolved config.ResolvedConfig, system string, runKey string, sendFn func(v any) error, deltas *wsDeltaCounters) (*odek.Agent, *bgRuntime, func() error, func(), func() error, guard.Guard, *wsApprover, error) {
	sm := skills.NewSkillManagerWithEmbedding(
		expandHome("~/.odek/skills"),
		"./.odek/skills",
		resolved.Skills.Embedding,
	)

	// Create WebSocket approver for dangerous operations approval
	approver := newWSApprover(sendFn)
	resolved.Dangerous.Approver = approver

	// Background commands: bind this agent to the process-scoped manager
	// (nil when the feature is disabled — the bg_* tools then stay absent
	// from the registry). The runtime's session is bound per prompt by
	// handlePrompt, once the stable store session id is known.
	bgRT := newServeBGRuntime(serveBG.Load(), backgroundSettingsFrom(resolved).Notify)
	tools := builtinTools(resolved.Dangerous, sm, approver, resolved.MaxConcurrency, resolved.APIKey, toolConfigFromResolved(resolved), nil, bgRT)

	// MCP server tools
	var mcpCleanup func()
	if len(resolved.MCPServers) > 0 {
		cl, err := loadMCPTools(resolved, &tools)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, approver, fmt.Errorf("mcp: %w", err)
		}
		mcpCleanup = cl
	}

	// Apply tool filtering based on configuration (after MCP tools are loaded
	// so disabled/enabled lists can reference MCP tool names too).
	tools = filterBuiltinTools(tools, resolved.Tools, nil)

	// Find the delegateTasksTool to wire up sub-agent log streaming
	var subagentTool *delegateTasksTool
	for _, t := range tools {
		if dt, ok := t.(*delegateTasksTool); ok {
			subagentTool = dt
			break
		}
	}
	if subagentTool != nil {
		subagentTool.OnSubagentLog = newSubagentTelemetryRelay(sendFn, runKey)
		subagentTool.OnSubagentDone = newSubagentDoneRelay(sendFn, runKey)
	}
	var sandboxCleanup func() error

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
			return nil, nil, nil, nil, nil, nil, approver, fmt.Errorf("sandbox: %w", sandboxErr)
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

	// Build the shared prompt-injection guard for this connection.
	injectionGuard, err := guard.New(&resolved.Guard)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, approver, fmt.Errorf("guard: %w", err)
	}
	guardCleanup := func() error {
		if injectionGuard != nil {
			return injectionGuard.Close()
		}
		return nil
	}
	if injectionGuard != nil {
		SetToolOutputGuard(injectionGuard, resolved.Guard)
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
		NoProjectFile:    resolved.NoAgents,
		Thinking:         resolved.Thinking,
		InteractionMode:  resolved.InteractionMode,
		// Live streaming: forward SSE fragments to the browser as
		// thinking_delta / token_delta events (docs/STREAMING.md). Off by
		// default; enabled with --stream / ODEK_STREAM / config "stream".
		Stream:       resolved.Stream,
		DeltaHandler: serveDeltaHandler(sendFn, deltas),
		Tools:        tools,
		ToolFilter:   odek.ToolFilterConfig{Enabled: resolved.Tools.Enabled, Disabled: resolved.Tools.Disabled},
		// SandboxCleanup is intentionally NOT passed here. In serve mode,
		// cleanup is the caller's responsibility (handleWS defers it).
		// Passing it here would cause agent.Close() to call docker rm -f,
		// and the explicit defer sandboxCleanup() in handleWS to call it
		// again — a harmless but confusing double-call.
		Renderer:     nil, // silent — we stream via WebSocket
		Skills:       &resolved.Skills,
		SkillManager: sm,
		MemoryConfig: resolved.Memory,
		MemoryDir:    expandHome("~/.odek/memory"),
		Guard:        injectionGuard,
		GuardConfig:  resolved.Guard,
		// Runtime event stream (odek.event/v1) — feeds /api/events. The
		// emitter is panic-isolated upstream; args are hashed + redacted
		// before they reach this handler, so the ring holds no raw data.
		EventHandler: func(ev events.Event) {
			serveEvents.add(ev)
		},
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
		// post-tool callbacks have no new reasoning to display. Skipped when
		// delta streaming is delivering reasoning live (thinking_delta) —
		// the echo would duplicate it after the fact.
		IterationCallback: func(info loop.IterationInfo) {
			if info.IsPreTool && info.ReasoningContent != "" {
				if reasoningDeltas, _ := deltas.snapshot(); reasoningDeltas == 0 {
					sendFn(map[string]any{
						"type":    "thinking",
						"content": info.ReasoningContent,
					})
				}
			}
			// Stream per-iteration token usage so clients can refresh their
			// context gauge live during a run instead of waiting for "done"
			// (which only fires once, after the whole agent loop).
			if info.InputTokens > 0 {
				sendFn(map[string]any{
					"type":          "usage",
					"contextTokens": info.InputTokens,
					"outputTokens":  info.OutputTokens,
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
		return nil, nil, nil, nil, nil, nil, approver, err
	}

	// Background-job completion notices are drained at iteration start
	// (unless background.notify == "off", which leaves the provider nil).
	if bgRT != nil && bgRT.provider != nil {
		agent.SetBackgroundNoticeProvider(bgRT.provider)
	}

	return agent, bgRT, sandboxCleanup, mcpCleanup, guardCleanup, injectionGuard, approver, nil
}

// serveDeltaHandler builds the loop DeltaHandler for a serve connection:
// reasoning fragments go out as thinking_delta, answer fragments as
// token_delta. Counted in deltas so handlePrompt can suppress the post-run
// bulk re-send. Tool-argument fragments are already suppressed by the engine.
func serveDeltaHandler(sendFn func(v any) error, deltas *wsDeltaCounters) func(llm.Delta) error {
	if deltas == nil {
		return nil
	}
	return func(d llm.Delta) error {
		switch d.Kind {
		case llm.DeltaReasoning:
			deltas.addReasoning()
			sendFn(map[string]any{"type": "thinking_delta", "content": d.Text})
		case llm.DeltaContent:
			deltas.addContent()
			sendFn(map[string]any{"type": "token_delta", "content": d.Text})
		}
		return nil
	}
}

// serveStateStartedAt returns the tracked start time, or the process start
// as a fallback when no serveState was wired (tests pass nil).
func serveStateStartedAt(st *serveState) time.Time {
	if st == nil {
		return processStart
	}
	return st.startedAt
}

// processStart is stamped at init for uptime reporting when no serveState
// is available.
var processStart = time.Now()

// wsServerSnapshot is the immutable per-connection slice of the resolved
// configuration used by the socket-reader goroutine. The processor loop may
// mutate resolved.Model on a per-prompt model switch while the reader
// answers ping heartbeats; reading the live struct from both goroutines is
// a data race. The snapshot is taken once, before the reader starts, and is
// never written afterwards.
type wsServerSnapshot struct {
	model   string
	sandbox bool
	stream  bool
}

// snapshotServerConfig copies the reader-visible config fields out of the
// mutable resolved config.
func snapshotServerConfig(resolved config.ResolvedConfig) wsServerSnapshot {
	return wsServerSnapshot{
		model:   resolved.Model,
		sandbox: resolved.Sandbox,
		stream:  resolved.Stream,
	}
}

// wsServerInfoEvent is the compact server snapshot carried by server_info
// (sent on connect) and pong (heartbeat replies). It is built from the
// immutable wsServerSnapshot, never from the live resolved config.
func wsServerInfoEvent(startedAt time.Time, snap wsServerSnapshot) map[string]any {
	return map[string]any{
		"version":        version,
		"model":          snap.model,
		"sandbox":        snap.sandbox,
		"stream":         snap.stream,
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
		"ws_connections": atomic.LoadInt64(&serveWSConnections),
	}
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
	TaskID      string         `json:"task_id,omitempty"` // subagent_cancel target
	// SystemInitiated marks server-initiated turns (wake-on-complete).
	// handlePrompt trusts it ONLY on Type=="bg_wake" items — the prompt
	// path sanitizes both fields below, so a client cannot forge system
	// provenance by sending the flag on a normal prompt.
	SystemInitiated bool `json:"system_initiated,omitempty"`
	// WakeToken authenticates bg_wake items: a per-connection random
	// secret the socket reader never forwards and the client never sees.
	WakeToken string `json:"wake_token,omitempty"`
}

// ── Turn identity (turn_started wire frame) ───────────────────────────

// newTurnID returns a fresh turn identifier ("t_" + 128-bit random hex).
// Clients upsert streaming-card state by this id, so a collision would
// merge two distinct turns; entropy failure panics like newWakeToken —
// an empty id would silently break card reconciliation.
func newTurnID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("odek: crypto/rand unavailable for turn id: " + err.Error())
	}
	return "t_" + hex.EncodeToString(b)
}

// turnTaggedFrames lists the frame types that carry turn_id while a turn
// is active (R3). Lifecycle and sub-agent frames stay untouched so old
// clients see byte-identical shapes for them.
var turnTaggedFrames = map[string]bool{
	"thinking":    true,
	"token":       true,
	"tool_call":   true,
	"tool_result": true,
	"done":        true,
	"error":       true,
}

// wsTurnAnnotator tags outbound frames with the active turn id (R3) so a
// client that attached mid-turn can attribute strays after a reconnect.
// One per connection: begin/end bracket each turn on the processor
// goroutine; wrap is shared with every emitter, including the agent's
// live tool-event and delta callbacks. Frames sent outside a turn and
// frame types outside turnTaggedFrames pass through unmodified.
type wsTurnAnnotator struct {
	mu     sync.Mutex
	turnID string
}

func (a *wsTurnAnnotator) begin(id string) {
	a.mu.Lock()
	a.turnID = id
	a.mu.Unlock()
}

func (a *wsTurnAnnotator) end() {
	a.mu.Lock()
	a.turnID = ""
	a.mu.Unlock()
}

func (a *wsTurnAnnotator) wrap(send func(map[string]any)) func(map[string]any) {
	return func(m map[string]any) {
		a.mu.Lock()
		id := a.turnID
		a.mu.Unlock()
		if typ, ok := m["type"].(string); ok && id != "" && turnTaggedFrames[typ] {
			m["turn_id"] = id
		}
		send(m)
	}
}

// ── WebSocket Handler ──────────────────────────────────────────────────

func handleWS(store *session.Store, resources *resource.Registry, resolved config.ResolvedConfig, system string, state *serveState, conn *golangws.Conn) {
	// Release the connection slot acquired by wsHandshakeWithLimits. This runs
	// after the handler exits, whether normally or via panic/close.
	defer func() {
		select {
		case <-wsConnSem:
		default:
		}
	}()
	// Track the live connection for /api/health and pong info payloads.
	atomic.AddInt64(&serveWSConnections, 1)
	defer atomic.AddInt64(&serveWSConnections, -1)

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
	defer releaseConnWriter(conn) // drop the per-connection write state

	// Connection registry for /api/connections — live management state
	// (session, busy, prompts). Unregistered on every exit path.
	// The HTTP peer address (e.g. "127.0.0.1:54321"). Preferred over
	// Conn.RemoteAddr(), whose Addr.String() panics when the handshake
	// config carries no Origin URL (in-process test handlers).
	remote := ""
	if req := conn.Request(); req != nil {
		remote = req.RemoteAddr
	}
	connInfo := &wsConnInfo{
		ID:          newWSConnID(),
		RemoteAddr:  remote,
		ConnectedAt: time.Now().UTC(),
		conn:        conn,
	}
	wsConnRegister(connInfo)
	defer wsConnUnregister(connInfo.ID)

	// Cap incoming message size to prevent a local client from exhausting
	// server memory with a single huge frame.
	conn.MaxPayloadBytes = maxWSMessageBytes

	// Per-connection streamed-fragment counters (see wsDeltaCounters).
	var deltas wsDeltaCounters

	// Create ONE agent per WebSocket connection — provides buffer
	// continuity across turns within the same session.
	// turnTag tags streamed frames with the active turn id (R3); the same
	// annotated sender backs the agent's live callbacks (newServeAgent)
	// and the processor-loop wsSend below, so every frame of a turn is
	// attributed to it.
	turnTag := &wsTurnAnnotator{}
	wsSend := turnTag.wrap(func(m map[string]any) { writeWSJSON(conn, m) })
	agent, bgRT, sandboxCleanup, mcpCleanup, guardCleanup, injectionGuard, approver, err := newServeAgent(resolved, system, connInfo.ID, func(v any) error {
		if m, ok := v.(map[string]any); ok {
			wsSend(m)
			return nil
		}
		writeWSJSON(conn, v)
		return nil
	}, &deltas)
	if err != nil {
		writeWSError(conn, fmt.Sprintf("agent: %v", err))
		return
	}

	// Immutable snapshot for the socket-reader goroutine: the pong
	// heartbeat below runs on the reader while the processor loop may write
	// resolved.Model (per-prompt model switch) — reading the live struct
	// from both goroutines is a data race.
	snap := snapshotServerConfig(resolved)

	// Server hello: let the client learn version/model/sandbox/stream state
	// without sending a prompt first.
	if state != nil {
		info := wsServerInfoEvent(state.startedAt, snap)
		info["type"] = "server_info"
		writeWSJSON(conn, info)
	}
	defer agent.Close()
	if guardCleanup != nil {
		defer guardCleanup()
	}
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
	// Wake-on-complete delivery slot: the dispatcher posts bg_wake items
	// here from timer goroutines; the slot's lock guarantees no post lands
	// after the channel close below (see cmd/odek/bg_wake.go, W2). The
	// secret wake token makes wire-injected bg_wake items invalid (P1-2).
	connInfo.wakeToken = newWakeToken()
	connInfo.wakeSlot = newConnWakeSlot(promptCh, connInfo.wakeToken)
	go func() {
		defer func() {
			// slot.close() closes promptCh under the slot's lock (the
			// teardown pair): a concurrent slot.post either lands before
			// the close (buffered, non-blocking) or observes closed and
			// drops — never a send on a closed channel, and never a double
			// close. Do NOT close(promptCh) here as well.
			connInfo.wakeSlot.close()
		}()
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

			// Application-level heartbeat. Handled inline in the reader so it
			// is answered even while a prompt occupies the processor loop.
			if msgType.Type == "ping" {
				pong := wsServerInfoEvent(serveStateStartedAt(state), snap)
				pong["type"] = "pong"
				pong["t"] = time.Now().UnixMilli()
				writeWSJSON(conn, pong)
				continue
			}

			// Cancel the running prompt over the WebSocket itself (same
			// session-scoped auth as POST /api/cancel — a cancel may only
			// target a session whose token the caller holds).
			if msgType.Type == "cancel" {
				var msg wsClientMsg
				if err := json.Unmarshal(data, &msg); err == nil {
					handleWSCancel(store, conn, msg)
				}
				continue
			}

			// Stop ONE running sub-agent (WebUI card stop button). Handled
			// inline like "cancel": the prompt processor is busy inside
			// delegate_tasks while the target sub-agent runs.
			if msgType.Type == "subagent_cancel" {
				var msg wsClientMsg
				if err := json.Unmarshal(data, &msg); err == nil {
					handleWSSubagentCancel(store, conn, msg)
				}
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

		// Session switch without sending a prompt: loads the session into
		// this connection's agent (buffer restore) and emits the standard
		// session event so the client's state converges immediately.
		if msgType.Type == "session_switch" {
			var msg wsClientMsg
			if err := json.Unmarshal(data, &msg); err == nil && msg.SessionID != "" {
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
				connInfo.setLive(sess.ID, false)
				// Background jobs started from here on belong to the newly
				// attached session (same binding rule as handlePrompt).
				bindBGRuntime(bgRT, sess.ID)
				if mm := agent.Memory(); mm != nil {
					mm.ClearBuffer()
					if len(sess.Buffer) > 0 {
						mm.RestoreBuffer(sess.Buffer)
					}
				}
				writeWSJSON(conn, map[string]any{"type": "session", "session_id": sess.ID, "auth_token": sess.AuthToken, "model": resolved.Model, "sandbox": resolved.Sandbox})
			}
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

		// Wake-on-complete (cmd/odek/bg_wake.go): a synthetic system-initiated
		// turn for a session whose background job finished while idle. It is
		// enqueued by the dispatcher into this connection's prompt queue and
		// runs HERE, on the processor goroutine, so every handlePrompt call
		// stays serialized (same budgets, approvals, usage as any prompt).
		if msgType.Type == "bg_wake" {
			var wake wsClientMsg
			if err := json.Unmarshal(data, &wake); err != nil || wake.SessionID == "" {
				continue
			}
			// Spend-control gate (review P1-2): the socket reader forwards
			// every non-inline client message into promptCh, so a client
			// could inject bg_wake items and bypass max_wakes_per_hour.
			// Only items stamped by the connection's slot carry the secret
			// wake token — constant-time compared, never sent to the client.
			if !validWakeToken(wake.WakeToken, connInfo.wakeToken) {
				continue
			}
			// The dispatcher resolved the session→connection binding at fire
			// time, but only this processor goroutine changes bindings (the
			// session_switch case below runs here too). A mid-flight switch
			// must not redirect a turn to a foreign session: drop and let
			// the payload path cover it on the next turn for that session.
			if currentSession == nil || currentSession.ID != wake.SessionID {
				continue
			}
			wakeMsg := wsClientMsg{
				Type:            "bg_wake",
				Content:         bgWakePreamble,
				SessionID:       wake.SessionID,
				SystemInitiated: true,
			}
			promptCtx, promptCancel := context.WithCancel(connCtx)
			promptCancelWithApproval := func() {
				promptCancel()
				if approver != nil {
					approver.Cancel()
				}
			}
			connInfo.setLive(wake.SessionID, true)
			func() {
				// Panic-safe Busy pairing (review F2): a panic unwinding
				// through handlePrompt must not latch Busy=true forever.
				defer connInfo.setLive(wake.SessionID, false)
				currentSession = handlePrompt(promptCtx, wsSend, store, resources, resolved, agent, injectionGuard, currentSession, wakeMsg, &sessionInputTokens, &sessionOutputTokens, promptCancelWithApproval, &deltas, bgRT, turnTag)
			}()
			promptCancel()
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
		// Client prompts are never system-initiated (review P1-1): the flag
		// and token are server-side provenance; strip them unconditionally.
		msg.SystemInitiated = false
		msg.WakeToken = ""

		if msg.Content == "" {
			continue
		}

		// Handle runtime model switching. Validate first (audit 2026-08):
		// applying the switch before handlePrompt's length/charset check
		// left a rejected model ID active on the agent, silently reused by
		// the next prompt sent without a model field.
		if msg.Model != "" && msg.Model != currentModel {
			if len(msg.Model) > maxModelIDBytes || !modelIDPattern.MatchString(msg.Model) {
				writeWSJSON(conn, map[string]any{"type": "error", "message": "invalid model ID"})
				continue
			}
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
			// Reset the in-memory buffer to the resumed session's state.
			// Without the clear, switching to a session whose saved buffer is
			// empty keeps the PREVIOUS session's lines live: the turn runs with
			// the old context and the post-turn save persists those lines into
			// the new session's file (cross-session buffer bleed). Mirrors the
			// session_switch handler above.
			if mm := agent.Memory(); mm != nil {
				mm.ClearBuffer()
				if len(sess.Buffer) > 0 {
					mm.RestoreBuffer(sess.Buffer)
				}
			}
		}

		// Run prompt — passes the persistent agent for buffer continuity
		// Create a cancelable context for this prompt (so POST /api/cancel can abort it).
		// Derived from connCtx so a disconnect also aborts the running prompt.
		promptCtx, promptCancel := context.WithCancel(connCtx)
		// Approval waits are ctx-blind: wsApprover.PromptCommand selects only
		// on {response, its own cancel channel, timeout} and never observes
		// promptCtx, so a bare context cancel leaves the loop blocked until
		// the 60s approval timeout. Compose the approver interrupt into the
		// cancel func registered by handlePrompt so EVERY cancel path (WS
		// cancel message, POST /api/cancel) also breaks a pending approval.
		// Cancel() re-arms, so later prompts on this connection can still
		// prompt for approval.
		promptCancelWithApproval := func() {
			promptCancel()
			if approver != nil {
				approver.Cancel()
			}
		}

		connInfo.setLive(msg.SessionID, true)
		func() {
			// Panic-safe Busy pairing (review F2).
			defer connInfo.setLive(msg.SessionID, false)
			currentSession = handlePrompt(promptCtx, wsSend, store, resources, resolved, agent, injectionGuard, currentSession, msg, &sessionInputTokens, &sessionOutputTokens, promptCancelWithApproval, &deltas, bgRT, turnTag)
		}()
		connInfo.recordPrompt()
		sid := ""
		if currentSession != nil {
			sid = currentSession.ID
		}
		connInfo.setLive(sid, false)

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

// handleWSCancel processes a WebSocket cancel message. Auth mirrors
// POST /api/cancel: the caller must present the target session's auth token,
// which prevents one connection from cancelling another connection's prompt.
// The registered cancel entry carries the approver interrupt alongside the
// context cancel (approval waits are ctx-blind — see handleWS), so this
// also breaks a pending approval wait instead of leaving the loop blocked
// until the 60s approval timeout.
func handleWSCancel(store *session.Store, conn *golangws.Conn, msg wsClientMsg) {
	if msg.SessionID == "" {
		writeWSError(conn, "cancel: missing session_id")
		return
	}
	sess, err := store.Load(msg.SessionID)
	if err != nil {
		writeWSError(conn, "cancel: session not found")
		return
	}
	if _, ok := validateSessionToken(store, sess, msg.AuthToken); !ok {
		writeWSError(conn, "cancel: invalid session token")
		return
	}
	if cancelPrompt(msg.SessionID) {
		writeWSJSON(conn, map[string]any{"type": "cancelled", "session_id": msg.SessionID})
	} else {
		writeWSJSON(conn, map[string]any{"type": "cancelled", "session_id": msg.SessionID, "idle": true})
	}
}

// handleWSSubagentCancel processes a WebSocket subagent_cancel message:
// stop ONE running sub-agent by task id (WebUI card stop button). Session
// auth mirrors handleWSCancel — the caller must present the session's
// auth token. Accepted=false is a normal race (the task finished before
// the stop arrived), not an error.
func handleWSSubagentCancel(store *session.Store, conn *golangws.Conn, msg wsClientMsg) {
	if msg.SessionID == "" {
		writeWSError(conn, "subagent_cancel: missing session_id")
		return
	}
	if msg.TaskID == "" {
		writeWSError(conn, "subagent_cancel: missing task_id")
		return
	}
	sess, err := store.Load(msg.SessionID)
	if err != nil {
		writeWSError(conn, "subagent_cancel: session not found")
		return
	}
	if _, ok := validateSessionToken(store, sess, msg.AuthToken); !ok {
		writeWSError(conn, "subagent_cancel: invalid session token")
		return
	}
	accepted := cancelSubagentTask(msg.TaskID)
	writeWSJSON(conn, map[string]any{
		"type":       "subagent_cancelled",
		"session_id": msg.SessionID,
		"task_id":    msg.TaskID,
		"accepted":   accepted,
	})
}

// ── Prompt Handler ─────────────────────────────────────────────────────

// handlePrompt processes a single user prompt within a WebSocket connection.
// Uses the persistent agent (for buffer continuity) and manages session state.
// Returns the updated session (may be a new session for first prompts).
func handlePrompt(
	ctx context.Context,
	send func(map[string]any),
	store *session.Store,
	resources *resource.Registry,
	resolved config.ResolvedConfig,
	agent *odek.Agent,
	g guard.Guard,
	currSess *session.Session,
	msg wsClientMsg,
	sessionInputTokens, sessionOutputTokens *int,
	promptCancel context.CancelFunc,
	deltas *wsDeltaCounters,
	bg *bgRuntime,
	turn *wsTurnAnnotator,
) *session.Session {
	prompt := msg.Content
	sessionID := msg.SessionID

	// Register the cancel entry BEFORE any session I/O so a cancel that
	// races the setup window (session load/create, @-ref resolution,
	// attachment wrapping) is honored instead of silently dropped — the
	// registry lookup would otherwise miss and the prompt would run to
	// completion. Chosen over a tombstone/pending-cancel flag because it
	// reuses the generation-guarded registry as-is: the late registration
	// below (needed for newly-created sessions whose ID exists only after
	// store.Create) replaces this entry, and both unregister defers are
	// generation-safe. Brand-new sessions keep a small window, but no
	// client can learn their ID — and thus target a cancel — before the
	// "session" event is sent.
	if sessionID != "" && promptCancel != nil {
		defer registerPromptCancel(sessionID, promptCancel)()
	}

	// Server-side cap on prompt size (finding #69). A client can already send
	// up to the WebSocket frame cap; reject anything above a reasonable prompt
	// limit before storing it in the session or forwarding it to the LLM.
	if len(prompt) > maxPromptBytes {
		sendError(send, "prompt exceeds maximum size")
		return currSess
	}

	// Server-side validation of model IDs from the UI (finding #81). Model IDs
	// are passed through to the LLM client and logged; cap length and reject
	// control / unusual characters to prevent oversized payloads or injection.
	if msg.Model != "" {
		if len(msg.Model) > maxModelIDBytes || !modelIDPattern.MatchString(msg.Model) {
			sendError(send, "invalid model ID")
			return currSess
		}
	}

	originalPrompt := prompt
	atomic.AddInt64(&serveStats.PromptsStarted, 1)

	// Load or create session early so the audit recorder can be attached
	// before @-references and Web-UI attachments are wrapped.
	var sess *session.Session
	var err error
	if sessionID != "" {
		sess, err = store.Load(sessionID)
		if err != nil {
			sess = nil
		}
	}

	// Run agent. Audit recorder wired around the loop so every
	// wrapUntrusted call (including @-refs and attachments below) is logged
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

	// Resolve @ references (now recorded if a session is active)
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
				sendError(send, "attachment too large: "+att.Name)
				return currSess
			}
			total += len(att.Content)
			if total > maxTotalAttachmentBytes {
				sendError(send, "total attachment size exceeds 10 MB")
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

	// Build message history
	var messages []llm.Message
	isNewSession := false
	// System-initiated wake turns carry a Name marker ("bg-wake") so the
	// loop's user-input hooks skip them exactly like drained bg-notice
	// messages, and so the transcript exposes their provenance. The gate is
	// Type-based (wakeInitiated): a client prompt that forges the flag on
	// Type "prompt" can never claim system provenance (review P1-1).
	userName := ""
	if wakeInitiated(msg) {
		userName = "bg-wake"
	}

	if sess != nil {
		messages = sess.GetMessages()
		messages = append(messages, llm.Message{Role: "user", Content: enrichedPrompt, Name: userName})
	} else {
		isNewSession = true
		messages = []llm.Message{
			{Role: "system", Content: ""},
			{Role: "user", Content: enrichedPrompt, Name: userName},
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

	cwd, _ := os.Getwd()
	if agent.Memory() != nil && sess != nil {
		agent.Memory().SetSessionContext(sess.ID, cwd)
	}
	// Stamp the session onto subsequent odek.event/v1 events so /api/events
	// can filter by session.
	if sess != nil {
		agent.SetEventSessionID(sess.ID)
		// Bind background jobs to the stable store session id so they
		// outlive this turn, survive across runs in the same session, and
		// stay visible to /api/jobs under this session's token.
		bindBGRuntime(bg, sess.ID)
	}

	// Send session info
	sid := ""
	authToken := ""
	if sess != nil {
		sid = sess.ID
		authToken = sess.AuthToken
	}
	// Register the cancel function for this session so the HTTP endpoint can
	// abort this specific prompt. Needed even though handlePrompt registers
	// msg.SessionID at the top: a newly-created session only gets its ID
	// here. The generation-guarded unregister only removes OUR registration
	// — a concurrent newer prompt on the same session keeps its own cancel
	// func when we finish first.
	if sid != "" && promptCancel != nil {
		defer registerPromptCancel(sid, promptCancel)()
	}
		sessFrame := map[string]any{"type": "session", "session_id": sid, "auth_token": authToken, "model": resolved.Model, "sandbox": resolved.Sandbox}
	if wakeInitiated(msg) {
		sessFrame["system_initiated"] = true // absent on operator turns
	}
	send(sessFrame)

	// turn_started (protocol R1): every turn — wake and operator alike —
	// announces itself immediately after the session frame and before the
	// first streamed frame, so a client that misses the session frame
	// (socket raced a reconnect) can still open the streaming card. The
	// initiated label is computed by the wakeInitiated type gate; client
	// input cannot influence it (R5). The session frame keeps its legacy
	// system_initiated stamp for old clients.
	turnID := newTurnID()
	send(map[string]any{
		"type":       "turn_started",
		"turn_id":    turnID,
		"session_id": sid,
		"initiated":  turnInitiatedLabel(msg),
		"model":      resolved.Model,
	})
	if turn != nil {
		turn.begin(turnID)
		defer turn.end() // streamed frames stop carrying this id at return
	}
	sl := activeServeLog()
	if sl != nil {
		sl.logf("turn_started session=%s model=%s", sid, resolved.Model)
	}

	// Append user input to buffer (AppendBuffer summarizes raw text).
	if mm := agent.Memory(); mm != nil {
		mm.AppendBuffer("user", prompt)
	}

	// Reset the streamed-fragment counters for this run; they decide below
	// whether the post-run bulk re-send can be skipped.
	if deltas != nil {
		deltas.reset()
	}

	origLen := len(messages) - 1 // initial estimate: index of the user message we appended

	// Persist per-turn progress so an interrupted run can be resumed from
	// the last completed step instead of losing the whole in-progress turn.
	// Mirror the store path below: dynamically-injected system messages
	// (skills, memory, episodes) are filtered out so persisted snapshots
	// don't accumulate internal injections or corrupt future origLen
	// calculations. The session's own leading system message and rolling-
	// compaction digest system messages are preserved (see
	// filterPersistSnapshot).
	if sess != nil {
		var head []llm.Message
		if len(sess.Messages) > 0 && sess.Messages[0].Role == "system" {
			head = sess.Messages[:1]
		}
		agent.SetMessagesPersistCallback(func(snapshot []llm.Message) {
			if len(snapshot) < len(sess.Messages) {
				// The loop trimmed history in place — keep the richer state
				// already persisted instead of overwriting it.
				return
			}
			sess.Messages = filterPersistSnapshot(head, snapshot)
			_ = store.SaveNoIndex(sess)
		})
	}

	// Persist the user turn BEFORE the run (dead-prompt fix). The per-step
	// persist callback above only fires after a completed step, so a provider
	// failure on the very first LLM call (rate-limit saturation, auth error,
	// outage) would return the session unchanged and the prompt would vanish
	// — observed repeatedly on 2026-08-29. SaveNoIndex skips the remote
	// vector index; a successful turn re-indexes on the final save below.
	if sess != nil {
		sess.Messages = append(sess.Messages, llm.Message{Role: "user", Content: enrichedPrompt, Name: userName})
		_ = store.SaveNoIndex(sess)
	}

	start := time.Now()
	_, allMessages, err := agent.RunWithMessages(ctx, messages)
	latency := time.Since(start)
	if sl != nil {
		sl.logf("turn_completed session=%s latency_ms=%d", sid, latency.Milliseconds())
	}
	streamedReasoning, streamedContent := 0, 0
	if deltas != nil {
		streamedReasoning, streamedContent = deltas.snapshot()
	}

	if err != nil {
		atomic.AddInt64(&serveStats.PromptsFailed, 1) // B3-SERVE-1: failed prompts must reach the usage aggregate
		sendError(send, err.Error())
		if sl != nil {
			sl.logf("turn_failed session=%s summary=%s", sid, providerFailureSummary(err))
		}
		if sess == nil {
			return currSess
		}
		// Soft-fail: close the turn with an assistant-visible note so the
		// transcript never ends on a dangling tool result and the next turn
		// sees coherent context. The user prompt is already persisted above;
		// returning sess (not currSess) also keeps the caller's run record
		// and in-memory session pointer in sync with the persisted state.
		note := fmt.Sprintf("[Turn aborted: %s. The prompt above was preserved — send another message to retry or continue.]", providerFailureSummary(err))
		sess.Messages = append(sess.Messages, llm.Message{Role: "assistant", Content: note})
		_ = store.SaveNoIndex(sess)
		return sess
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

	// Record per-turn divergence assessment. Use the original prompt so
	// injected resources from @-refs/attachments do not count as user-mentioned.
	if auditSessID != "" {
		recordTurnAudit(auditStore, auditSessID, auditTurn, originalPrompt, allMessages[origLen:])
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
		// then stream the response text. Both bulk sends are skipped when the
		// client already received this run's fragments live (token_delta /
		// thinking_delta) — re-sending would duplicate the whole answer. A
		// provider that rejected SSE leaves the counters at zero and takes
		// this bulk path unchanged.
		if msg.ReasoningContent != "" && streamedReasoning == 0 {
			send(map[string]any{"type": "thinking", "content": msg.ReasoningContent})
		}
		if msg.Content != "" && streamedContent == 0 {
			send(map[string]any{"type": "token", "content": msg.Content})
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
	atomic.AddInt64(&serveStats.TokensIn, int64(contextTokens))
	atomic.AddInt64(&serveStats.TokensOut, int64(outputTokens))
	atomic.AddInt64(&serveStats.PromptsCompleted, 1)

	// Cumulative per-session usage (observability only — budgets are
	// enforced per-run by internal/budget).
	if sess != nil {
		sess.InputTokens += int64(contextTokens)
		sess.OutputTokens += int64(outputTokens)
	}

	// Save session — persist buffer and update the vector index.
	// The message history was already persisted per-turn by the persist
	// callback above (which filters dynamically-injected system messages —
	// skills, memory, episodes — so they are not stored in the session and
	// don't corrupt future origLen calculations on subsequent turns).
	if sess != nil {
		if mm := agent.Memory(); mm != nil {
			sess.Buffer = mm.GetBuffer()
		}
		store.Save(sess)
	}

	// done is sent only AFTER the final save: clients refresh their session
	// list the moment they see done, and a save-after-send race would hand
	// them stale state (usage, turns, updated_at).
	send(map[string]any{
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
	return fmt.Errorf("origin %q not allowed (only localhost is accepted)", origin)
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
func wsHandshakeWithLimits(cfg *golangws.Config, req *http.Request, token string, trustedProxies []string) error {
	if err := validateServeToken(cfg, req, token); err != nil {
		return err
	}
	if err := checkLocalOrigin(nil, req); err != nil {
		return err
	}
	if !wsUpgradeLimiter.allow(clientIP(req, trustedProxies)) {
		return fmt.Errorf("WebSocket upgrade rate limit exceeded")
	}
	select {
	case wsConnSem <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("too many concurrent WebSocket connections")
	}
}

// serveWSReal is the production library driver: it runs x/net/websocket's
// server with the given guarded callbacks. It is passed to serveWSUpgrades
// as the serve parameter (tests substitute a stub that mimics the library's
// contract, including the post-handshake failure path).
func serveWSReal(handshake func(*golangws.Config, *http.Request) error, handler func(*golangws.Conn), w http.ResponseWriter, req *http.Request) {
	(&golangws.Server{Handshake: handshake, Handler: handler}).ServeHTTP(w, req)
}

// serveWSUpgrades wraps the /ws endpoint with slot-leak protection for the
// handshake window. wsHandshakeWithLimits acquires wsConnSem inside the
// library's Handshake callback, but x/net/websocket can still fail the
// upgrade AFTER that callback returns (newServerConn → AcceptHandshake
// write error): serveWebSocket then returns without ever calling the
// Handler, and the slot — normally released by handleWS's first defer —
// would leak. maxWSConnections such failures permanently wedge /ws.
//
// The wrapper observes both sides on the single request goroutine and
// releases exactly when the handshake acquired a slot but the Handler
// never ran. When the Handler ran, handleWS owns the release.
func serveWSUpgrades(
	serve func(handshake func(*golangws.Config, *http.Request) error, handler func(*golangws.Conn), w http.ResponseWriter, req *http.Request),
	handshake func(*golangws.Config, *http.Request) error,
	handler func(*golangws.Conn),
) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var acquired, handled bool
		guardedHandshake := func(cfg *golangws.Config, req *http.Request) error {
			err := handshake(cfg, req)
			acquired = err == nil
			return err
		}
		guardedHandler := func(conn *golangws.Conn) {
			handled = true
			handler(conn)
		}
		defer func() {
			if acquired && !handled {
				select {
				case <-wsConnSem:
				default:
				}
			}
		}()
		serve(guardedHandshake, guardedHandler, w, req)
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

// requireLocalHost rejects requests whose Host header does not name a
// loopback interface. This closes DNS-rebinding attacks that point an external
// domain at 127.0.0.1 and then drive the local API from a malicious web page.
func requireLocalHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			http.Error(w, "Host not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validateServeTokenHTTP verifies the per-instance CSRF token on an HTTP
// request. It accepts the same cookie and header transports as the WebSocket
// handshake, but not the WebSocket subprotocol.
func validateServeTokenHTTP(req *http.Request, token string) error {
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

	return fmt.Errorf("missing or invalid server token")
}

// requireServeToken requires the per-instance CSRF token on every request.
// This blocks DNS-rebinding and cross-site driven reads of API endpoints that
// were previously unauthenticated on GET.
func requireServeToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := validateServeTokenHTTP(r, token); err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// presentsInstanceTokenHeader reports whether the request carries the
// per-instance CSRF token in the X-Odek-Ws-Token header. Header presentation
// is a KNOWLEDGE proof — unlike the ambient SameSite=Strict cookie, a
// cross-origin (e.g. DNS-rebinding) page can neither read the token value
// nor set the header (the custom header triggers a CORS preflight, which
// odek does not answer). It therefore distinguishes legitimate front-ends
// (the WebUI, bodek, curl with the token URL) from a rebinding page that
// merely holds the cookie.
func presentsInstanceTokenHeader(r *http.Request, wsToken string) bool {
	if wsToken == "" {
		return false
	}
	h := r.Header.Get(wsTokenHeaderName)
	return h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(wsToken)) == 1
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

// validateSessionTokenStrict is validateSessionToken for MUTATION paths
// (delete, rename/pin, cancel, job control): a legacy session with no
// stored token is minted, but the freshly minted token must actually be
// PRESENTED. The lenient variant's mint-and-pass let an instance-cookie-
// only holder act on pre-token-defense sessions (2026-09 posture review,
// wave C). Read paths keep the deliberate GET bootstrap, which RETURNS
// the minted token to the client.
func validateSessionTokenStrict(store *session.Store, sess *session.Session, token string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthToken == "" {
		sess.AuthToken = session.GenerateAuthToken()
		if err := store.Save(sess); err != nil {
			return false
		}
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.AuthToken)) == 1
}

// wsWriteTimeout bounds a single WebSocket frame write. A client that
// stops reading while its run streams (SIGSTOP'd process, or a token
// holder with --stream enabled) fills its TCP receive window and blocks
// Send forever; the write previously held the process-wide mutex while
// doing so, freezing every connection's writes — approval prompts and
// pongs included — until the wedged client drained (2026-08 audit). A
// var so tests can shrink it. Atomic access: tests retune it while live
// WS goroutines (including in-flight ones from earlier tests) read it
// concurrently — a plain read/write here is a data race (caught by CI).
var wsWriteTimeout atomic.Int64

func init() { wsWriteTimeout.Store(int64(30 * time.Second)) }

// wsConnWriters gives each connection its own write lock.
// golang.org/x/net/websocket is not safe for concurrent Sends, and frames
// are written from several goroutines (agent loop callbacks, subagent
// logs, approvals, socket reader); unsynchronized writes interleave into
// torn JSON. Serialization is per connection — a slow client must not
// stall writes to other clients, which the old process-wide mutex did.
var wsConnWriters sync.Map // *golangws.Conn → *connWriteState

type connWriteState struct {
	mu   sync.Mutex
	dead bool // write timed out: fast-fail all later sends on this conn
}

func connWriter(conn *golangws.Conn) *connWriteState {
	if v, ok := wsConnWriters.Load(conn); ok {
		return v.(*connWriteState)
	}
	w := &connWriteState{}
	actual, _ := wsConnWriters.LoadOrStore(conn, w)
	return actual.(*connWriteState)
}

// releaseConnWriter drops a closed connection's write state. Called from
// handleWS's teardown and after a write-timeout teardown.
func releaseConnWriter(conn *golangws.Conn) {
	wsConnWriters.Delete(conn)
}

func writeWSJSON(conn *golangws.Conn, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	w := connWriter(conn)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		golangws.Message.Send(conn, string(payload))
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(wsWriteTimeout.Load())):
		// The client stopped reading: its TCP receive window is full and
		// Send is wedged. Abandon the write (bounded caller, per-conn
		// lock released, later sends on this conn fast-fail) and tear the
		// connection down best-effort. The Close is asynchronous because
		// x/net/websocket's Close writes a close frame through the same
		// internal write lock the stuck sender holds — a synchronous call
		// would deadlock the watchdog. If the client never drains, the
		// parked sender and Close goroutines leak — bounded by
		// maxWSConnections and cleaned up when the socket eventually
		// errors out.
		w.dead = true
		go func() { _ = conn.Close() }()
		releaseConnWriter(conn)
	}
}

func writeWSError(conn *golangws.Conn, msg string) {
	writeWSJSON(conn, map[string]string{"type": "error", "message": msg})
}

// sendError emits an error event through a generic send sink (used by the
// headless run path, which has no socket).
// maxSubagentRelayDataBytes caps the data field of a subagent_log WS
// message after redaction. Child tool results are routinely larger (and
// model-controlled); the UI needs a status line, not the payload.
const maxSubagentRelayDataBytes = 8 << 10 // 8 KiB

// newSubagentLogRelay converts raw sub-agent NDJSON lines into redacted,
// size-capped, task-correlated WS messages (sub-agent telemetry M1 step 0:
// child stdout is model-controlled content — it must never reach a browser
// unredacted; loop.go tool_result data is raw tool output).
func newSubagentLogRelay(send func(v any) error) func(taskIdx int, taskID string, line string) {
	return func(taskIdx int, taskID string, line string) {
		var event struct {
			Type   string `json:"type"`
			Name   string `json:"name,omitempty"`
			Data   string `json:"data,omitempty"`
			TaskID string `json:"task_id,omitempty"`
			Status string `json:"status,omitempty"`
			Step   any    `json:"step,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return // malformed/partial line from a killed child — drop
		}
		data := redact.RedactSecrets(event.Data)
		if len(data) > maxSubagentRelayDataBytes {
			const truncSuffix = " …[truncated]"
			cut := maxSubagentRelayDataBytes - len(truncSuffix)
			if cut < 0 {
				cut = 0
			}
			data = data[:cut] + truncSuffix
		}
		if taskID == "" {
			taskID = event.TaskID
		}
		msg := map[string]any{
			"type":     "subagent_log",
			"task_idx": taskIdx,
			"task_id":  taskID,
			"event":    event.Type,
		}
		if event.Name != "" {
			msg["name"] = event.Name
		}
		if data != "" {
			msg["data"] = data
		}
		if event.Status != "" {
			msg["status"] = event.Status
		}
		if event.Step != nil {
			msg["step"] = event.Step
		}
		_ = send(msg)
	}
}

func sendError(send func(map[string]any), msg string) {
	send(map[string]any{"type": "error", "message": msg})
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

func handleSessionByID(store *session.Store, trustedProxies []string, wsToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		// /api/sessions/{id}/export — transcript download (md|json). Shares
		// the GET auth path below (rate limit + session token).
		// /export is a GET-only surface: the suffix is stripped for GET
		// requests only, mirroring the /plan guard below. Stripping it for
		// every method let DELETE /api/sessions/{id}/export fall through to
		// the base-session delete (destroying the session through a
		// read-only route) and POST .../export rename it.
		exportFormat := ""
		if r.Method == http.MethodGet && strings.HasSuffix(id, "/export") {
			id = strings.TrimSuffix(id, "/export")
			exportFormat = r.URL.Query().Get("format")
		}
		// /api/sessions/{id}/plan — read-only structured plan view. The
		// suffix is stripped for GET only: a non-GET request must not fall
		// through to the base-session handlers (POST would otherwise rename
		// the session through the plan URL). The plan surface is strictly
		// read-only.
		planView := false
		if r.Method == http.MethodGet && strings.HasSuffix(id, "/plan") {
			id = strings.TrimSuffix(id, "/plan")
			planView = true
		}
		if id == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Rate-limit session detail lookups per IP to slow brute-force
			// enumeration of the 128-bit ID space.
			if !sessionLookupLimiter.allow(clientIP(r, trustedProxies)) {
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
			if !ok && presentsInstanceTokenHeader(r, wsToken) {
				// Bootstrap the session token for callers that prove
				// knowledge of the per-instance CSRF token via the
				// X-Odek-Ws-Token header. Without this, sessions created by
				// one front-end (e.g. bodek, or a WebUI in another browser
				// profile) can never be loaded by another: the per-session
				// token only reaches the client that created the session,
				// and the legacy bootstrap only covers sessions that have
				// no token at all.
				effectiveToken, ok = sess.AuthToken, true
			}
			if !ok {
				http.Error(w, "invalid session token", http.StatusUnauthorized)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/export") {
				handleSessionExport(sess, exportFormat, w)
				return
			}
			if planView {
				handleSessionPlan(sess, w)
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
			if !validateSessionTokenStrict(store, sess, token) {
				http.Error(w, "invalid session token", http.StatusUnauthorized)
				return
			}
			if err := store.Delete(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPost:
			// Rename and/or pin a session. Pointer fields distinguish
			// "absent" from explicit false so each may be sent alone.
			var body struct {
				Name   *string `json:"name"`
				Pinned *bool   `json:"pinned"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if body.Name == nil && body.Pinned == nil {
				http.Error(w, "nothing to update (name or pinned required)", http.StatusBadRequest)
				return
			}
			sess, err := store.Load(id)
			if err != nil {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			token := sessionTokenFromRequest(r)
			if !validateSessionTokenStrict(store, sess, token) {
				http.Error(w, "invalid session token", http.StatusUnauthorized)
				return
			}
			if body.Name != nil {
				sess.Task = *body.Name
			}
			if body.Pinned != nil {
				sess.Pinned = *body.Pinned
			}
			store.Save(sess)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(sess)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// clientIP returns a best-effort client identifier for rate limiting.
// X-Forwarded-For / X-Real-Ip headers are only honored when the direct remote
// address is in trustedProxies. An empty trustedProxies list means headers are
// ignored even from loopback, closing the X-Forwarded-For spoofing bypass.
func clientIP(r *http.Request, trustedProxies []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isTrustedProxy(host, trustedProxies) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// Key on the LAST entry: with a trusted proxy in the path, the
			// right-most entry is the one the trusted proxy appended, while
			// the left-most is client-supplied and spoofable — rotating it
			// would rotate rate-limit buckets and grow the limiter map.
			if i := strings.LastIndex(fwd, ","); i >= 0 {
				return strings.TrimSpace(fwd[i+1:])
			}
			return strings.TrimSpace(fwd)
		}
		if real := r.Header.Get("X-Real-Ip"); real != "" {
			return real
		}
	}
	return host
}

// isTrustedProxy reports whether host is in the trusted proxy list. Entries may
// be exact IPs or CIDR ranges.
func isTrustedProxy(host string, trusted []string) bool {
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	for _, entry := range trusted {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == host {
			return true
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil && ip != nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
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

// handleLimits returns the execution-budget configuration resolved at server
// start plus the effective per-million token prices for the configured model
// (Limits.ResolvePrices). Clients rendering session costs use
// effective_prices directly; limits.model_prices lets them price other
// models. When no prices are configured, effective_prices is 0/0 — clients
// should treat that as "costs unavailable". The payload is built by
// buildLimitsView (introspect.go); the config_view tool renders the same
// map, pinned equal by TestRESTLimitsMatchesSharedBuilder.
func handleLimits(configuredModel string, limits budget.Limits) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeAPIJSON(w, http.StatusOK, buildLimitsView(configuredModel, limits))
	}
}

// handleCancel cancels the running prompt for the requested session.
// POST /api/cancel?session_id=<id> — cancels the agent execution scoped to
// that session. Requiring the session ID prevents one connection from
// cancelling another connection's prompt.
//
// Returns 200 with {"session_id", "idle"} mirroring the WS cancel contract:
// idle=false means a live prompt was found and cancelled, idle=true means
// nothing was running. The old unconditional 204 hid misses from API
// clients (asymmetric with WS/runs, which both report idle), which masked
// exactly the dropped-cancel bugs this endpoint exists to expose.
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

		// cancelPrompt fires the registered entry — context cancel plus the
		// approver interrupt (approval waits are ctx-blind, see handleWS).
		idle := !cancelPrompt(sessionID)
		writeAPIJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "idle": idle})
	}
}

// ── Static Handler ─────────────────────────────────────────────────────

// staticFiles maps URL paths to embedded file paths and their content types.
var staticFiles = map[string][2]string{
	"/":          {"ui/index.html", "text/html; charset=utf-8"},
	"/style.css": {"ui/style.css", "text/css; charset=utf-8"},
	"/app.js":    {"ui/app.js", "application/javascript; charset=utf-8"},
	// Self-hosted font (variable weight 100–700) so the UI works offline and
	// does not depend on the Google Fonts CDN.
	"/fonts/azeret-mono.woff2": {"ui/fonts/azeret-mono.woff2", "font/woff2"},
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
		if !ok && strings.HasPrefix(r.URL.Path, "/js/") {
			// ES modules under /js/ are served from the embedded ui/js
			// directory. Sanitize to a flat .js file name so path traversal
			// is impossible.
			name := strings.TrimPrefix(r.URL.Path, "/js/")
			if name != "" && !strings.Contains(name, "..") &&
				!strings.ContainsAny(name, "/\\") && strings.HasSuffix(name, ".js") {
				entry = [2]string{"ui/js/" + name, "application/javascript; charset=utf-8"}
				ok = true
			}
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := uiFS.ReadFile(entry[0])
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// The HTML entry point only receives the per-instance CSRF token when
		// the request presents the token in the `?token=` query parameter. This
		// prevents a network attacker from retrieving the token with a simple
		// `GET /`. The token is delivered both as a SameSite=Strict HttpOnly
		// cookie (sent automatically on same-site WebSocket upgrades) and as a
		// meta tag (read by app.js and sent as a WebSocket subprotocol).
		if r.URL.Path == "/" && wsToken != "" {
			// Constant-time, like every other comparison in this file: this
			// is the one endpoint that mints the authenticated cookie.
			if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(wsToken)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name:     wsTokenCookieName,
					Value:    wsToken,
					Path:     "/",
					SameSite: http.SameSiteStrictMode,
					HttpOnly: true,
					// Secure is set even though the server usually runs on
					// plain-http loopback: modern browsers treat localhost as a
					// potentially trustworthy origin and accept Secure cookies
					// there, and the UI always sends the token as a WebSocket
					// subprotocol (and /api header), so a browser that drops
					// the cookie loses nothing.
					Secure: true,
					// No explicit MaxAge/Expires so the cookie is a session cookie.
				})
				data = []byte(strings.Replace(string(data), "{{ODEK_WS_TOKEN}}", wsToken, 1))
				w.Header().Set("Cache-Control", "no-store")
			} else {
				// No valid token in the URL: serve the UI but leave the meta tag
				// empty so the browser cannot connect until the user uses the
				// token URL printed to the console.
				data = []byte(strings.Replace(string(data), "{{ODEK_WS_TOKEN}}", "", 1))
			}
		}

		w.Header().Set("Content-Type", entry[1])
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		// Assets are content-addressed by a strong ETag with
		// must-revalidate semantics: a browser tab left open across an
		// odek upgrade revalidates, gets a 304 when the file is unchanged,
		// and picks up the new UI the moment it differs — no heuristic
		// caching serving a stale frontend after `odek upgrade`.
		etag := `"` + fmt.Sprintf("%x", sha256.Sum256(data)) + `"`
		w.Header().Set("ETag", etag)
		// The token-bearing index.html keeps its stricter no-store policy
		// (set above) — only plain assets get the revalidate contract.
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// Strict CSP: no inline scripts (all handlers are addEventListener /
		// delegation), styles only from self + the few style="" attributes in
		// index.html. frame-ancestors replaces the old standalone CSP line.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
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

// isLoopbackAddr reports whether a TCP listener is bound to a loopback
// address. Unix sockets and non-TCP listeners are treated as non-loopback
// (fail safe) because we cannot reason about their exposure.
func isLoopbackAddr(a net.Addr) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	return ta.IP.IsLoopback()
}
