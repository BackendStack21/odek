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
	"github.com/BackendStack21/kode/internal/render"
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

func newServeAgent(resolved config.ResolvedConfig, system string) (*kode.Agent, func() error, error) {
	var sm *skills.SkillManager
	if resolved.Skills.Learn {
		sm = skills.NewSkillManager(
			expandHome("~/.kode/skills"),
			"./.kode/skills",
		)
	}

	tools := builtinTools(resolved.Dangerous, sm)
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
			return nil, nil, fmt.Errorf("sandbox: %w", err)
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
	})
	if err != nil {
		return nil, nil, err
	}

	return agent, sandboxCleanup, nil
}

// ── WebSocket Handler ───────────────────────────────────────────────────

type wsClientMsg struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
}

func handleWebSocket(store *session.Store, resources *resource.Registry, resolved config.ResolvedConfig, system string) http.HandlerFunc {
	// Resolve config once at startup — reconnects use the same config
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrade(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer conn.Close()

		ctx, cancel := signal.NotifyContext(r.Context(), os.Interrupt)
		defer cancel()

		for {
			opcode, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if opcode != ws.OpText {
				continue
			}

			var msg wsClientMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				writeWSError(conn, "invalid JSON")
				continue
			}

			if msg.Type != "prompt" || msg.Content == "" {
				continue
			}

			// Run prompt (blocking per WS loop — one prompt at a time)
			handlePrompt(ctx, conn, store, resources, resolved, system, msg.Content, msg.SessionID)
		}
	}
}

func handlePrompt(ctx context.Context, conn *ws.Conn, store *session.Store, resources *resource.Registry, resolved config.ResolvedConfig, system string, prompt string, sessionID string) {
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

	// Build agent for this prompt
	agent, sandboxCleanup, err := newServeAgent(resolved, system)
	if err != nil {
		writeWSError(conn, fmt.Sprintf("agent: %v", err))
		return
	}
	defer agent.Close()
	if sandboxCleanup != nil {
		defer sandboxCleanup()
	}

	// Build message history
	var messages []llm.Message
	if sess != nil {
		messages = sess.GetMessages()
		messages = append(messages, llm.Message{Role: "user", Content: enrichedPrompt})
	} else {
		modelLabel := kode.ProfileLabel(resolved.Model)
		if modelLabel == "" {
			modelLabel = "kode"
		}

		messages = []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: enrichedPrompt},
		}

		// Persist new session
		newSess, err := store.Create(
			[]llm.Message{{Role: "system", Content: system}},
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

	// Run agent with WebSocket streaming writer
	rend := render.New(&wsStreamWriter{conn: conn}, false)
	_ = rend // agent doesn't expose renderer injection post-creation

	// The agent loop runs silently (no renderer). We stream the final
	// answer via the wsStreamWriter as we get it.
	origLen := len(messages) - 1 // exclude the user message we just appended

	// Since the agent uses RunWithMessages (not streaming per-token),
	// we send the final result as a single "token" event.
	// TODO: add streaming callback support to the agent loop
	start := time.Now()
	_, allMessages, err := agent.RunWithMessages(ctx, messages)
	latency := time.Since(start)

	if err != nil {
		writeWSError(conn, err.Error())
		return
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

	writeWSJSON(conn, map[string]any{
		"type":    "done",
		"latency": latency.Seconds(),
	})

	// Save session — append new messages
	if sess != nil {
		store.Append(sess.ID, newMsgs)
	}
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
