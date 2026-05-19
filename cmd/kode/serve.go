package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/resource"
	"github.com/BackendStack21/kode/internal/session"
	"github.com/BackendStack21/kode/internal/skills"
	"github.com/BackendStack21/kode/internal/ws"
)

//go:embed ui/index.html
var uiFS embed.FS

// ── Serve Command ───────────────────────────────────────────────────────

func serveCmd(args []string) error {
	addr := ":8080"
	openBrowser := false

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
			fmt.Println(`Usage: kode serve [flags]

Start the Kode web UI server.

Flags:
  --addr :8080    Listen address (default :8080)
  --open          Open browser automatically
  --help, -h      Show this help`)
			return nil
		default:
			return fmt.Errorf("unknown flag %q for serve", args[i])
		}
	}

	resolved := config.LoadConfig(config.CLIFlags{})
	systemMessage := resolved.System
	if systemMessage == "" {
		systemMessage = defaultSystem
	}

	store, err := session.NewStore()
	if err != nil {
		return fmt.Errorf("session store: %w", err)
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".kode", "sessions")),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic())
	mux.HandleFunc("/ws", handleWebSocket(store, resourceReg, resolved, systemMessage))
	mux.HandleFunc("/api/resources", handleResourceSearch(resourceReg))
	mux.HandleFunc("/api/sessions", handleSessionList(store))

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	fmt.Fprintf(os.Stderr, "kode serve ⚡  http://%s\n", listener.Addr())
	fmt.Fprintf(os.Stderr, "  WebSocket: ws://%s/ws\n", listener.Addr())
	fmt.Fprintf(os.Stderr, "  Type @ in the input to reference files and sessions.\n")

	if openBrowser {
		openInBrowser("http://" + listener.Addr().String())
	}

	return http.Serve(listener, mux)
}

// ── Agent Builder ──────────────────────────────────────────────────────

func newServeAgent(resolved config.ResolvedConfig, system string, sendFn func(v any) error) (*kode.Agent, func() error, *wsApprover, error) {
	var sm *skills.SkillManager
	if resolved.Skills.Learn {
		sm = skills.NewSkillManager(
			expandHome("~/.kode/skills"),
			"./.kode/skills",
		)
	}

	// Create WebSocket approver for dangerous operations approval
	approver := newWSApprover(sendFn)
	resolved.Dangerous.Approver = approver

	tools := builtinTools(resolved.Dangerous, sm, approver)
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
		cleanup, err := setupSandbox(tools, cfg)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("sandbox: %w", err)
		}
		sandboxCleanup = cleanup
	}

	agent, err := kode.New(kode.Config{
		Model:          resolved.Model,
		BaseURL:        resolved.BaseURL,
		APIKey:         resolved.APIKey,
		MaxIterations:  resolved.MaxIter,
		SystemMessage:  system,
		NoProjectFile:  resolved.NoAgents,
		Thinking:       resolved.Thinking,
		Tools:          tools,
		SandboxCleanup: sandboxCleanup,
		Renderer:       nil, // silent — we stream via WebSocket
		Skills:         &resolved.Skills,
		SkillManager:   sm,
		MemoryConfig:   resolved.Memory,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return agent, sandboxCleanup, approver, nil
}

// ── WebSocket Types ────────────────────────────────────────────────────

type wsClientMsg struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
}

// ── WebSocket Handler ──────────────────────────────────────────────────

func handleWebSocket(store *session.Store, resources *resource.Registry, resolved config.ResolvedConfig, system string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer conn.Close()

		// Create ONE agent per WebSocket connection — provides buffer
		// continuity across turns within the same session.
		agent, sandboxCleanup, approver, err := newServeAgent(resolved, system, func(v any) error {
			writeWSJSON(conn, v)
			return nil
		})
		if err != nil {
			writeWSError(conn, fmt.Sprintf("agent: %v", err))
			return
		}
		defer agent.Close()
		if sandboxCleanup != nil {
			defer sandboxCleanup()
		}

		ctx, cancel := signal.NotifyContext(r.Context(), os.Interrupt)
		defer cancel()

		// Track the current session across WebSocket messages
		var currentSession *session.Session

		for {
			opcode, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if opcode != ws.OpText {
				continue
			}

			// Peek at the message type without full unmarshal
			var msgType struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &msgType); err != nil {
				continue
			}

			// Handle approval responses separately (non-blocking, from the browser)
			if msgType.Type == "approval_response" {
				var resp approvalResponse
				if err := json.Unmarshal(data, &resp); err == nil {
					approver.HandleResponse(resp.ID, resp.Action)
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

			// Handle session switch mid-connection (new conversation)
			if msg.SessionID != "" && (currentSession == nil || currentSession.ID != msg.SessionID) {
				sess, err := store.Load(msg.SessionID)
				if err == nil {
					currentSession = sess
					// Restore buffer from the resumed session
					if mm := agent.Memory(); mm != nil && len(sess.Buffer) > 0 {
						mm.RestoreBuffer(sess.Buffer)
					}
				}
			}

			// Run prompt — passes the persistent agent for buffer continuity
			currentSession = handlePrompt(ctx, conn, store, resources, resolved, agent, currentSession, msg.Content, msg.SessionID)
		}

		// WebSocket disconnected — extract episode if enough turns
		if currentSession != nil {
			if mm := agent.Memory(); mm != nil {
				msgStrs := makeSessionMessageStrings(currentSession)
				mm.OnSessionEnd(currentSession.ID, currentSession.Turns, msgStrs)
			}
		}
	}
}

// ── Prompt Handler ─────────────────────────────────────────────────────

// handlePrompt processes a single user prompt within a WebSocket connection.
// Uses the persistent agent (for buffer continuity) and manages session state.
// Returns the updated session (may be a new session for first prompts).
func handlePrompt(
	ctx context.Context,
	conn *ws.Conn,
	store *session.Store,
	resources *resource.Registry,
	resolved config.ResolvedConfig,
	agent *kode.Agent,
	currSess *session.Session,
	prompt string,
	sessionID string,
) *session.Session {
	// Resolve @ references
	refs := resource.ParseRefs(prompt)
	resolvedRefs := make(map[string]string)
	for _, ref := range refs {
		content, err := resources.Load(ctx, ref.Raw)
		if err != nil {
			continue
		}
		resolvedRefs[ref.Raw] = content
	}
	enrichedPrompt := resource.ReplaceRefs(prompt, resolvedRefs)

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
	if sess != nil {
		sid = sess.ID
	}
	writeWSJSON(conn, map[string]any{"type": "session", "session_id": sid, "model": resolved.Model})

	// Append user input to buffer
	if mm := agent.Memory(); mm != nil {
		userSummary := shorten(prompt, 100)
		mm.AppendBuffer("user", userSummary)
	}

	// Run agent
	origLen := len(messages) - 1 // exclude the user message we just appended
	start := time.Now()
	_, allMessages, err := agent.RunWithMessages(ctx, messages)
	latency := time.Since(start)

	if err != nil {
		writeWSError(conn, err.Error())
		return currSess // return unchanged session on error
	}

	// Get new messages from this turn
	newMsgs := allMessages[origLen:]
	if sess != nil {
		origLen = 0
	} else {
		origLen = 1 // skip system for new sessions
	}
	if origLen < len(newMsgs) {
		newMsgs = newMsgs[origLen:]
	}

	// Stream the assistant's response
	for _, msg := range newMsgs {
		if msg.Role == "assistant" {
			if msg.Content != "" {
				writeWSJSON(conn, map[string]any{"type": "token", "content": msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				writeWSJSON(conn, map[string]any{
					"type":    "tool_call",
					"name":    tc.Function.Name,
					"command": tc.Function.Arguments,
				})
			}
		}
		if msg.Role == "tool" {
			writeWSJSON(conn, map[string]any{
				"type":   "tool_result",
				"name":   msg.Name,
				"output": shorten(msg.Content, 500),
			})
		}
	}

	// Find the assistant response for buffer
	if mm := agent.Memory(); mm != nil {
		for _, msg := range newMsgs {
			if msg.Role == "assistant" && msg.Content != "" {
				agentSummary := shorten(msg.Content, 100)
				mm.AppendBuffer("agent", agentSummary)
				break
			}
		}
	}

	writeWSJSON(conn, map[string]any{
		"type":    "done",
		"latency": latency.Seconds(),
	})

	// Save session — persist messages AND buffer
	if sess != nil {
		// Persist buffer to session before saving
		if mm := agent.Memory(); mm != nil {
			sess.Buffer = mm.GetBuffer()
		}
		store.Append(sess.ID, newMsgs)
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
	conn *ws.Conn
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

func writeWSJSON(conn *ws.Conn, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	conn.WriteMessage(ws.OpText, payload)
}

func writeWSError(conn *ws.Conn, msg string) {
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessions == nil {
			sessions = []session.Session{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}

// ── Static Handler ─────────────────────────────────────────────────────

func handleStatic() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := uiFS.ReadFile("ui/index.html")
		if err != nil {
			http.Error(w, "UI not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
