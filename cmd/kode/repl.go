package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/render"
	"github.com/BackendStack21/kode/internal/session"
)

// ── REPL ──────────────────────────────────────────────────────────────

// replCmd handles `kode repl [--id <id>]`.
// It starts (or resumes) an interactive multi-turn session.
func replCmd(args []string) error {
	sessionID := ""
	for i := 0; i < len(args)-1 && args[i] == "--id"; {
		sessionID = args[i+1]
		i += 2
	}

	store, err := session.NewStore()
	if err != nil {
		return fmt.Errorf("session store: %w", err)
	}

	// Load or create session first — needed to know sandbox state
	var sess *session.Session
	if sessionID != "" {
		sess, err = store.Load(sessionID)
		if err != nil {
			return fmt.Errorf("load session %q: %w", sessionID, err)
		}
	}

	// Resolve config (before session creation so Session.Sandbox is set)
	resolved := config.LoadConfig(config.CLIFlags{})
	systemMessage := resolved.System
	if systemMessage == "" {
		systemMessage = defaultSystem
	}

	// Auto-apply sandbox if resuming a sandboxed session
	if sess != nil && sess.Sandbox && !resolved.Sandbox {
		resolved.Sandbox = true
		fmt.Fprintf(os.Stderr, "kode: session was sandboxed — enabling sandbox\n")
	}

	// Build tools
	tools := builtinTools(resolved.Dangerous)
	var sandboxCleanup func() error

	if resolved.Sandbox {
		sbCfg := sandboxConfig{
			Image:    resolved.SandboxImage,
			Network:  resolved.SandboxNetwork,
			Readonly: resolved.SandboxReadonly,
			Memory:   resolved.SandboxMemory,
			CPUs:     resolved.SandboxCPUs,
			User:     resolved.SandboxUser,
			Env:      resolved.SandboxEnv,
			Volumes:  resolved.SandboxVolumes,
		}
		cleanup, err := setupSandbox(tools, sbCfg)
		if err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		sandboxCleanup = cleanup
	}

	// Renderer
	modelLabel := kode.ProfileLabel(resolved.Model)
	if modelLabel == "" {
		modelLabel = "deepseek-chat"
	}
	color := !resolved.NoColor && render.ColorEnabled()
	rend := render.New(os.Stderr, color).WithModel(modelLabel)

	agent, err := kode.New(kode.Config{
		Model:          resolved.Model,
		BaseURL:        resolved.BaseURL,
		APIKey:         resolved.APIKey,
		MaxIterations:  resolved.MaxIter,
		SystemMessage:  systemMessage,
		NoProjectFile:  resolved.NoAgents,
		Thinking:       resolved.Thinking,
		Tools:          tools,
		SandboxCleanup: sandboxCleanup,
		Renderer:       rend,
	})
	if err != nil {
		return err
	}
	defer agent.Close()

	// Create session if not resuming one
	if sess == nil {
		sess, err = store.Create(
			[]llm.Message{{Role: "system", Content: systemMessage}},
			resolved.Model,
			"interactive session",
		)
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		sess.Sandbox = resolved.Sandbox
		store.Save(sess)
	}

	fmt.Fprintf(os.Stderr, "\nkode ⚡ %s · session %s\n\n", modelLabel, sess.ID)
	fmt.Fprintf(os.Stderr, "  Type /help for commands, /exit to quit.\n\n")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	scanner := bufio.NewScanner(os.Stdin)
	turn := 0
	for {
		fmt.Fprintf(os.Stderr, "─── Turn %d ───\n", turn+1)
		fmt.Fprint(os.Stderr, "> ")

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())

		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			if handleREPLCommand(input, sess) {
				break
			}
			continue
		}

		// Build message history: session messages + new user input
		messages := sess.GetMessages()
		origLen := len(messages)
		messages = append(messages, llm.Message{Role: "user", Content: input})

		// Run agent with full history
		rend.Start(input)
		_, allMessages, err := agent.RunWithMessages(ctx, messages)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kode: agent error: %v\n", err)
			continue
		}

		// Save new messages to session
		newMsgs := allMessages[origLen:]
		if err := store.Append(sess.ID, newMsgs); err != nil {
			fmt.Fprintf(os.Stderr, "kode: save error: %v\n", err)
		}

		// Reload session to get updated turn count
		sess, _ = store.Load(sess.ID)
		turn++

		fmt.Fprintln(os.Stderr)
	}
	return nil
}

// handleREPLCommand processes a REPL slash command.
// Returns true if the session should exit.
func handleREPLCommand(input string, sess *session.Session) bool {
	switch strings.ToLower(input) {
	case "/exit", "/quit":
		fmt.Fprintf(os.Stderr, "Session %s saved. Continue later with: kode repl --id %s\n", sess.ID, sess.ID)
		return true
	case "/help":
		fmt.Fprint(os.Stderr, `Commands:
  /exit, /quit    Exit REPL (session is saved)
  /help           Show this help
  /info           Show session info

`)
	case "/info":
		fmt.Fprintf(os.Stderr, "Session: %s\n", sess.ID)
		fmt.Fprintf(os.Stderr, "Model:   %s\n", sess.Model)
		fmt.Fprintf(os.Stderr, "Turns:   %d\n", sess.Turns)
		if sess.Sandbox {
			fmt.Fprintf(os.Stderr, "Sandbox: yes\n")
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s  (/help for commands)\n", input)
	}
	return false
}
