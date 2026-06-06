package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/render"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/skills"
)

// ── REPL ──────────────────────────────────────────────────────────────

// replCmd handles `odek repl [flags]`.
// It starts (or resumes) an interactive multi-turn session.
// Accepts --model, --thinking, --sandbox, and --sandbox-* flags
// just like `odek run`, plus --id to resume a specific session.
func replCmd(args []string) error {
	f, err := parseReplFlags(args)
	if err != nil {
		return err
	}
	sessionID := f.ID

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
	resolved := config.LoadConfig(config.CLIFlags{
		Model:           f.Model,
		Thinking:        f.Thinking,
		Sandbox:         f.Sandbox,
		PromptCaching:   f.PromptCaching,
		InteractionMode: f.InteractionMode,

		SandboxImage:    f.SandboxImage,
		SandboxNetwork:  f.SandboxNetwork,
		SandboxReadonly: f.SandboxReadonly,
		SandboxMemory:   f.SandboxMemory,
		SandboxCPUs:     f.SandboxCPUs,
		SandboxUser:     f.SandboxUser,
	})
	systemMessage := buildSystemPrompt(resolved)

	// session resume
	if sess != nil && sess.Sandbox && !resolved.Sandbox {
		resolved.Sandbox = true
		fmt.Fprintf(os.Stderr, "odek: session was sandboxed — enabling sandbox\n")
	}

	// Build tools
	var sm *skills.SkillManager
	if resolved.Skills.Learn {
		sm = skills.NewSkillManager(
			expandHome("~/.odek/skills"),
			"./.odek/skills",
		)
	}
	tools := builtinTools(resolved.Dangerous, sm, nil, resolved.MaxConcurrency, resolved.APIKey, config.TranscriptionConfig{}, nil)
	var sandboxCleanup func() error

	// MCP server tools
	var mcpCleanup func()
	if len(resolved.MCPServers) > 0 {
		cl, err := loadMCPTools(resolved.MCPServers, &tools)
		if err != nil {
			return fmt.Errorf("mcp: %w", err)
		}
		mcpCleanup = cl
		defer mcpCleanup()
	}

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
		var replContainerName string
		replContainerName, cleanup, err := setupSandbox(tools, sbCfg)
		if err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		_ = replContainerName // not used in REPL mode
		sandboxCleanup = cleanup
	} else {
		warnSandboxDisabled()
	}

	// Renderer
	modelLabel := odek.ProfileLabel(resolved.Model)
	if modelLabel == "" {
		modelLabel = "deepseek-v4-flash"
	}
	color := !resolved.NoColor && render.ColorEnabled()
	rend := render.New(os.Stderr, color).WithModel(modelLabel)

	// Resolve skills config pointer (only when learn mode is enabled)
	var skillsCfg *skills.SkillsConfig
	if resolved.Skills.Learn {
		skillsCfg = &resolved.Skills
	}

	agent, err := odek.New(odek.Config{
		Model:          resolved.Model,
		BaseURL:        resolved.BaseURL,
		APIKey:         resolved.APIKey,
		MaxIterations:  resolved.MaxIter,
		SystemMessage:  systemMessage,
		NoProjectFile:  resolved.NoAgents,
		Thinking:       resolved.Thinking,
		ThinkingBudget: f.ThinkingBudget,
		Tools:          tools,
		SandboxCleanup: sandboxCleanup,
		Renderer:       rend,
		Skills:         skillsCfg,
		SkillManager:   sm,
		MemoryConfig:   resolved.Memory,
		PromptCaching:  resolved.PromptCaching,
	})
	if err != nil {
		return err
	}
	defer agent.Close()

	// Restore buffer from session if resuming
	if sess != nil && len(sess.Buffer) > 0 {
		if mm := agent.Memory(); mm != nil {
			mm.RestoreBuffer(sess.Buffer)
		}
	}

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

	fmt.Fprintf(os.Stderr, "\nodek ⚡ %s · session %s\n\n", modelLabel, sess.ID)
	fmt.Fprintf(os.Stderr, "  Type /help for commands, /exit to quit.\n\n")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	turn := 0

	// Line editor with history and tab completion for slash commands
	editor := newReplEditor(
		fmt.Sprintf("odek %d> ", turn+1),
		[]string{
			"/exit", "/quit", "/help", "/info",
			"/sandbox", "/model", "/session",
		},
	)
	editor.history.Load(filepath.Join(odekDir(), historyFilename))
	for {
		fmt.Fprintf(os.Stderr, "─── Turn %d ───\n", turn+1)

		input, err := editor.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) || err.Error() == "interrupt" {
				fmt.Fprintf(os.Stderr, "\n")
				break
			}
			break
		}
		input = strings.TrimSpace(input)

		if input == "" {
			turn++
			continue
		}

		if strings.HasPrefix(input, "/") {
			if handleREPLCommand(input, sess) {
				break
			}
			turn++
			continue
		}

		// Resolve @references in REPL input
		cwd, _ := os.Getwd()
		if enriched, err := enrichTask(input, nil, cwd); err == nil {
			input = enriched
		}

		// Build message history: session messages + new user input
		messages := sess.GetMessages()
		origLen := len(messages)
		messages = append(messages, llm.Message{Role: "user", Content: input})

		// Append user input to buffer (AppendBuffer summarizes raw text).
		if mm := agent.Memory(); mm != nil {
			mm.AppendBuffer("user", input)
		}

		// Run agent with full history
		rend.Start(input)
		_, allMessages, err := agent.RunWithMessages(ctx, messages)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odek: agent error: %v\n", err)
			continue
		}

		// Append agent response to buffer (AppendBuffer summarizes raw text).
		if mm := agent.Memory(); mm != nil && len(allMessages) > 0 {
			if last := allMessages[len(allMessages)-1]; last.Role == "assistant" {
				mm.AppendBuffer("agent", last.Content)
			}
		}

		// Save new messages to session
		newMsgs := allMessages[origLen:]
		if err := store.Append(sess.ID, newMsgs); err != nil {
			fmt.Fprintf(os.Stderr, "odek: save error: %v\n", err)
		}

		// Reload session to get updated turn count + persist buffer
		sess, _ = store.Load(sess.ID)
		if sess != nil {
			if mm := agent.Memory(); mm != nil {
				sess.Buffer = mm.GetBuffer()
				store.Save(sess)
			}
		}
		turn++

		fmt.Fprintln(os.Stderr)
	}

	// Session end — extract episode if enough turns.
	// Run asynchronously so episode extraction does not delay process exit.
	if mm := agent.Memory(); mm != nil {
		go func() {
			messages := sess.GetMessages()
			msgStrs := make([]string, 0, len(messages))
			for _, m := range messages {
				msgStrs = append(msgStrs, m.Role+": "+m.Content)
			}
			prov := memory.DeriveProvenance(messages)
			mm.OnSessionEndWithProvenance(sess.ID, sess.Turns, msgStrs, prov)
		}()
	}

	return nil
}

// handleREPLCommand processes a REPL slash command.
// Returns true if the session should exit.
func handleREPLCommand(input string, sess *session.Session) bool {
	switch strings.ToLower(input) {
	case "/exit", "/quit":
		fmt.Fprintf(os.Stderr, "Session %s saved. Continue later with: odek repl --id %s\n", sess.ID, sess.ID)
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
