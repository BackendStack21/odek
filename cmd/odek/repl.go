package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/guard"
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
		Stream:          f.Stream,
		Compaction:      f.Compaction,
		Planning:        f.Planning,
		InteractionMode: f.InteractionMode,

		SandboxImage:    f.SandboxImage,
		SandboxNetwork:  f.SandboxNetwork,
		SandboxReadonly: f.SandboxReadonly,
		SandboxMemory:   f.SandboxMemory,
		SandboxCPUs:     f.SandboxCPUs,
		SandboxUser:     f.SandboxUser,
	})
	if err := approveProjectSandbox(resolved, os.Stdin, os.Stdout); err != nil {
		return err
	}
	systemMessage := buildSystemPrompt(resolved)

	// session resume
	if sess != nil && sess.Sandbox && !resolved.Sandbox {
		resolved.Sandbox = true
		fmt.Fprintf(os.Stderr, "odek: session was sandboxed — enabling sandbox\n")
	}

	// Create session if not resuming one — before tools are built so the
	// background runtime can bind to the session id (bg_* jobs are
	// session-scoped: they outlive turns and die at session end).
	if sess == nil {
		sess, err = store.Create(
			[]session.Message{{Role: "system", Content: systemMessage}},
			resolved.Model,
			"interactive session",
		)
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		sess.Sandbox = resolved.Sandbox
		sess.Provider = resolved.Provider
		store.Save(sess)
	}

	// Build tools
	sm := skills.NewSkillManagerWithEmbedding(
		expandHome("~/.odek/skills"),
		"./.odek/skills",
		resolved.Skills.Embedding,
	)
	tools := builtinTools(resolved.Dangerous, sm, nil, resolved.MaxConcurrency, resolved.APIKey, toolConfigFromResolved(resolved), nil)

	// MCP server tools
	var mcpCleanup func()
	if len(resolved.MCPServers) > 0 {
		cl, err := loadMCPTools(resolved, &tools)
		if err != nil {
			return fmt.Errorf("mcp: %w", err)
		}
		mcpCleanup = cl
		defer mcpCleanup()
	}

	var sandboxCleanup func() error

	// Sandbox (H-8): defaults ON with a loud unsandboxed fallback; explicit
	// --sandbox keeps the hard-fail behavior. Runs before tool filtering so
	// the background runtime can route spawns through the same container.
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
	bgName, bgCleanup, bgSandboxed, bgErr := ensureSandbox(resolved, tools, sbCfg)
	if bgErr != nil {
		return fmt.Errorf("sandbox: %w", bgErr)
	}
	if bgSandboxed {
		sandboxCleanup = bgCleanup
	}

	// Background commands: session-scoped runtime — jobs outlive turns but
	// die at session end / process exit (deferred Shutdown). Nil when the
	// section is disabled; the bg_* tools then simply stay absent.
	bgRT := newBackgroundRuntime(backgroundSettingsFromResolved(resolved), sess.ID, bgName, nil)
	defer bgRT.Shutdown()
	tools = appendBackgroundTools(tools, bgRT)

	// Apply tool filtering based on configuration (after MCP tools are loaded
	// so disabled/enabled lists can reference MCP tool names too).
	tools = filterBuiltinTools(tools, resolved.Tools, nil)

	// Renderer
	modelLabel := odek.ProfileLabel(resolved.Model)
	if modelLabel == "" {
		modelLabel = "deepseek-v4-flash"
	}
	color := !resolved.NoColor && render.ColorEnabled()
	rend := render.New(os.Stderr, color).WithModel(modelLabel)

	// Resolve skills config pointer (only when learn mode is enabled)
	skillsCfg := &resolved.Skills

	injectionGuard, err := guard.New(&resolved.Guard)
	if err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	if injectionGuard != nil {
		defer injectionGuard.Close()
		SetToolOutputGuard(injectionGuard, resolved.Guard)
	}

	replCfg := odek.Config{
		Model:            resolved.Model,
		BaseURL:          resolved.BaseURL,
		APIKey:           resolved.APIKey,
		MaxIterations:    resolved.MaxIter,
		SystemMessage:    systemMessage,
		UntrustedWrapper: func(source, content string) string { return wrapUntrusted(context.Background(), source, content) },
		NoProjectFile:    resolved.NoAgents,
		Thinking:         resolved.Thinking,
		ThinkingBudget:   f.ThinkingBudget,
		Tools:            tools,
		ToolFilter:       odek.ToolFilterConfig{Enabled: resolved.Tools.Enabled, Disabled: resolved.Tools.Disabled},
		SandboxCleanup:   sandboxCleanup,
		Renderer:         rend,
		Skills:           skillsCfg,
		SkillManager:     sm,
		MemoryConfig:     resolved.Memory,
		MemoryDir:        expandHome("~/.odek/memory"),
		DangerousConfig:  &resolved.Dangerous,
		PromptCaching:    resolved.PromptCaching,
		Stream:           resolved.Stream,
		DeltaHandler:     streamDeltaPrinter(resolved.Stream, rend),
		Compaction:       resolved.Compaction,
		Guard:            injectionGuard,
		GuardConfig:      resolved.Guard,
	}
	applyResolvedProvider(&replCfg, resolved)
	agent, err := odek.New(replCfg)
	if err != nil {
		return err
	}
	defer agent.Close()
	// File delegate_tasks artifacts under the interactive session so the
	// store's OnDelete cascade owns their lifecycle.
	if sess != nil {
		agent.SetToolSessionID(sess.ID)
	}

	// Background completion notices: drained at the top of every iteration
	// and injected as an observe-phase message (background.notify="observe";
	// "off" keeps the agent on explicit bg_status/bg_output polling).
	if bgRT != nil && resolved.Background.Notify == "observe" {
		agent.SetBackgroundNoticeProvider(bgRT.provider)
	}

	// Restore buffer from session if resuming
	if sess != nil && len(sess.Buffer) > 0 {
		if mm := agent.Memory(); mm != nil {
			mm.RestoreBuffer(sess.Buffer)
		}
	}

	// Persist per-turn progress so an interrupted turn (Ctrl-C) survives up
	// to the last completed step instead of losing the whole turn.
	agent.SetMessagesPersistCallback(func(snapshot []session.Message) {
		if sess == nil || len(snapshot) < len(sess.Messages) {
			// The loop trimmed history in place — keep the richer state
			// already persisted instead of overwriting it.
			return
		}
		sess.Messages = snapshot
		if err := store.SaveNoIndex(sess); err != nil {
			fmt.Fprintf(os.Stderr, "odek: save error: %v\n", err)
		}
	})
	cwd, _ := os.Getwd()
	if mm := agent.Memory(); mm != nil {
		mm.SetSessionContext(sess.ID, cwd)
	}

	fmt.Fprintf(os.Stderr, "\nodek ⚡ %s · session %s\n\n", modelLabel, sess.ID)
	fmt.Fprintf(os.Stderr, "  Type /help for commands, /exit to quit.\n\n")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	auditStore := session.NewAuditStore(store.Dir())

	turn := 0
	// resumedSession gates the one-shot return-after-break injection on the
	// first turn after resuming with `odek repl --id <session>`.
	resumedSession := sessionID != ""

	// Line editor with history and tab completion for slash commands.
	// Keep this list in sync with handleREPLCommand — completing a command
	// that isn't implemented yields "Unknown command".
	editor := newReplEditor(
		fmt.Sprintf("odek %d> ", turn+1),
		replCommands,
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
			if handleREPLCommand(input, sess, bgRT) {
				break
			}
			turn++
			continue
		}
		originalInput := input
		auditTurn := sess.Turns + 1
		runCtx := withAuditRecorder(ctx, auditStore, sess.ID, auditTurn)
		runCtx = withReadLedger(runCtx, sess.ID)

		// Resolve @references in REPL input
		cwd, _ := os.Getwd()
		if enriched, err := enrichTask(runCtx, input, nil, cwd); err == nil {
			input = enriched
		}

		// Build message history: session messages + new user input
		messages := sess.GetMessages()
		if resumedSession {
			// Return-after-break: on session resume, inject a concise
			// summary of where the user left off (first turn only).
			messages = injectReturnAfterBreak(ctx, agent.Memory(), messages)
			resumedSession = false
		}
		histLen := len(messages)
		messages = append(messages, session.Message{Role: "user", Content: input})

		// Append user input to buffer (AppendBuffer summarizes raw text).
		if mm := agent.Memory(); mm != nil {
			mm.AppendBuffer("user", input)
		}

		// Run agent with full history
		rend.Start(input)
		_, allMessages, err := agent.RunWithMessages(runCtx, messages)
		if err != nil {
			recordTurnAudit(auditStore, sess.ID, auditTurn, originalInput, auditTurnDelta(allMessages, histLen))
			// Persist the partial history so the interrupted turn survives
			// up to the last completed step (mirrors the Telegram cancel path).
			persistPartialMessages(store, sess, allMessages)
			fmt.Fprintf(os.Stderr, "odek: agent error: %v\n", err)
			continue
		}
		recordTurnAudit(auditStore, sess.ID, auditTurn, originalInput, auditTurnDelta(allMessages, histLen))

		// Append agent response to buffer (AppendBuffer summarizes raw text).
		if mm := agent.Memory(); mm != nil && len(allMessages) > 0 {
			if last := allMessages[len(allMessages)-1]; last.Role == "assistant" {
				mm.AppendBuffer("agent", last.Content)
			}
		}

		// The per-turn persist callback already saved the full history;
		// reload and Save once more to persist the buffer and update the
		// vector index for the completed turn.
		sess, _ = store.Load(sess.ID)
		if sess != nil {
			if mm := agent.Memory(); mm != nil {
				sess.Buffer = mm.GetBuffer()
			}
			if err := store.Save(sess); err != nil {
				fmt.Fprintf(os.Stderr, "odek: save error: %v\n", err)
			}
		}

		// Follow-up suggestions after the turn (presentation-only, printed
		// on stderr like the rest of the REPL's turn output; not persisted).
		if mm := agent.Memory(); mm != nil {
			printFollowUpSuggestions(os.Stderr, mm, resolved.InteractionMode)
		}
		turn++

		fmt.Fprintln(os.Stderr)
	}

	// Session end — extract episode if enough turns.
	// Run in the background (tracked by the memory manager's WaitGroup) so
	// episode extraction does not delay REPL exit; the deferred Agent.Close
	// drains it via WaitForBackground so it is not silently lost.
	if mm := agent.Memory(); mm != nil {
		mm.RunBackground(func() {
			messages := sess.GetMessages()
			msgStrs := make([]string, 0, len(messages))
			for _, m := range messages {
				msgStrs = append(msgStrs, m.Role+": "+m.Content)
			}
			prov := memory.DeriveProvenance(messages)
			mm.OnSessionEndWithProvenance(sess.ID, sess.Turns, msgStrs, prov)
		})
	}

	return nil
}

// replCommands lists the slash commands the REPL implements (tab
// completion + docs source of truth). Only commands handleREPLCommand
// actually handles may appear here.
var replCommands = []string{
	"/exit", "/quit", "/help", "/info", "/jobs", "/jobs stop",
}

// handleREPLCommand processes a REPL slash command.
// Returns true if the session should exit.
func handleREPLCommand(input string, sess *session.Session, rt *bgRuntime) bool {
	switch strings.ToLower(input) {
	case "/exit", "/quit":
		fmt.Fprintf(os.Stderr, "Session %s saved. Continue later with: odek repl --id %s\n", sess.ID, sess.ID)
		return true
	case "/help":
		fmt.Fprint(os.Stderr, `Commands:
  /exit, /quit    Exit REPL (session is saved)
  /help           Show this help
  /info           Show session info
  /jobs           List background jobs (id, status, runtime, exit, command)
  /jobs stop <id> Stop one background job

`)
	case "/jobs":
		printBackgroundJobs(os.Stderr, rt)
	case "/jobs stop":
		fmt.Fprintf(os.Stderr, "usage: /jobs stop <id>  (see /jobs for ids)\n")
	case "/info":
		fmt.Fprintf(os.Stderr, "Session: %s\n", sess.ID)
		fmt.Fprintf(os.Stderr, "Model:   %s\n", sess.Model)
		fmt.Fprintf(os.Stderr, "Turns:   %d\n", sess.Turns)
		if sess.Sandbox {
			fmt.Fprintf(os.Stderr, "Sandbox: yes\n")
		}
	default:
		if strings.HasPrefix(strings.ToLower(input), "/jobs stop ") {
			stopBackgroundJob(rt, strings.TrimSpace(input[len("/jobs stop "):]))
			return false
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s  (/help for commands)\n", input)
	}
	return false
}

// printBackgroundJobs renders the session's background jobs as a table:
// id, status, runtime, exit code, command head.
func printBackgroundJobs(w io.Writer, rt *bgRuntime) {
	if rt == nil {
		fmt.Fprintln(w, "Background commands are disabled (background.enabled=false).")
		return
	}
	jobs := rt.mgr.List(rt.session)
	if len(jobs) == 0 {
		fmt.Fprintln(w, "No background jobs in this session.")
		return
	}
	fmt.Fprintf(w, "%-8s %-9s %10s  %-4s  %s\n", "ID", "STATUS", "RUNTIME", "EXIT", "COMMAND")
	for _, j := range jobs {
		exit := "-"
		if j.Status == bgproc.StatusExited || j.Status == bgproc.StatusFailed {
			exit = strconv.Itoa(j.ExitCode)
		}
		d := j.EndedAt.Sub(j.StartedAt)
		if j.EndedAt.IsZero() {
			d = time.Since(j.StartedAt)
		}
		fmt.Fprintf(w, "%-8s %-9s %9ss  %-4s  %s\n", j.ID, j.Status, d.Round(time.Second), exit, headString(j.Command, bgCommandHead))
	}
}

// stopBackgroundJob stops one background job by id ("/jobs stop <id>").
func stopBackgroundJob(rt *bgRuntime, id string) {
	if rt == nil {
		fmt.Fprintln(os.Stderr, "Background commands are disabled.")
		return
	}
	job, ok := rt.mgr.Stop(rt.session, id)
	if !ok {
		fmt.Fprintf(os.Stderr, "No background job %q in this session.\n", id)
		return
	}
	fmt.Fprintf(os.Stderr, "Job %s: %s\n", job.ID, job.Status)
}
