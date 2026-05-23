package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/loop"
	"github.com/BackendStack21/kode/internal/render"
	"github.com/BackendStack21/kode/internal/narrate"
	"github.com/BackendStack21/kode/internal/session"
	"github.com/BackendStack21/kode/internal/skills"
	"github.com/BackendStack21/kode/internal/telegram"
	toolpkg "github.com/BackendStack21/kode/internal/tool"
)

// chatMu serializes agent processing per chat to prevent same-chat message
// racing. Each chat gets its own mutex; messages from the same chat are
// processed sequentially, preserving session history integrity.
var chatMu sync.Map // map[int64]*sync.Mutex

// chatCancels stores per-chat cancel functions. When /stop is received, the
// cancel function is called to interrupt the running agent loop.
var chatCancels sync.Map // map[int64]context.CancelFunc

// chatRunInfos stores the latest IterationInfo for each chat, updated on
// every agent loop iteration. Used by /stop to report a summary of the
// interrupted task.
var chatRunInfos sync.Map // map[int64]loop.IterationInfo

// pendingSuggestions stores SkillSuggestion values keyed by skill name,
// awaiting user approval via inline keyboard callbacks.
var pendingSuggestions sync.Map // map[string]skills.SkillSuggestion

// activeTaskWG tracks in-flight handleChatMessage goroutines that are running
// the agent loop. Graceful restart waits on this WaitGroup before spawning the
// child process, ensuring in-progress tasks finish (or hit the drain timeout).
var activeTaskWG sync.WaitGroup

// restartInProgress is set atomically during graceful restart to prevent new
// agent runs from starting. handleChatMessage checks this flag after acquiring
// the per-chat mutex; if set, it sends a "try again" message and returns
// without running the agent.
var restartInProgress atomic.Bool

// resolvedAPIKey holds the API key after config loading, for use by
// spawnChild during graceful restart (the config loader clears env vars).
var resolvedAPIKey string

// killFn is used to signal processes (SIGHUP for restart, SIGTERM for
// shutdown). Override in tests to avoid killing the test process.
var killFn = syscall.Kill

// getChatMutex returns the per-chat mutex for the given chat ID.
func getChatMutex(chatID int64) *sync.Mutex {
	v, _ := chatMu.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// telegramCmd is the entry point for "odek telegram".
func telegramCmd(args []string) error {
	// 0. Acquire singleton lock — kill any stale previous instance.
	lock, err := acquireLock()
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	defer lock.release()

	// 1. Load config from all sources (file → env).
	resolved := config.LoadConfig(config.CLIFlags{})

	// 2. Validate API key presence.
	if resolved.APIKey == "" {
		return fmt.Errorf("no API key configured — set ODEK_API_KEY, DEEPSEEK_API_KEY, or configure in odek.json")
	}
	// Store for spawnChild: config loader clears ODEK_API_KEY from env,
	// so the child process needs us to re-inject it.
	resolvedAPIKey = resolved.APIKey

	// 3. Load and validate Telegram config.
	cfg := resolved.Telegram
	if err := telegram.ValidateConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: %v\n", err)
		return err
	}

	// 4. Create bot client.
	bot := telegram.NewBot(cfg.Token)

	// 4b. Create logger.
	level := telegram.ParseLogLevel(cfg.LogLevel)
	rootLog := telegram.NewFileLogger(level, cfg.LogFile)
	botLog := rootLog.With("component", "bot")
	handlerLog := rootLog.With("component", "handler")
	pollerLog := rootLog.With("component", "poller")

	bot.SetLogger(botLog)

	// 4c. Configure fallback Telegram API endpoints if provided.
	if len(cfg.FallbackURLs) > 0 {
		bot.SetFallbackURLs(cfg.FallbackURLs)
	}

	// 4d. Configure daily token budget (0 = unlimited, the default).
	if cfg.DailyTokenBudget > 0 {
		bot.SetDailyTokenBudget(cfg.DailyTokenBudget)
		botLog.Info("daily token budget set", "budget", cfg.DailyTokenBudget)
	}

	// 5. Create session store on disk (~/.odek/sessions/).
	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: session store: %v\n", err)
		return err
	}

	// 6. Create session manager (per-chat Telegram session cache)
	//    with the configured session TTL (default 24h).
	sessionManager := telegram.NewSessionManager(store, time.Duration(cfg.SessionTTL)*time.Hour)

	// 7. Create handler.
	handler := telegram.NewHandler(bot)
	handler.SetLogger(handlerLog)

	// 8. Set handler config from cfg.
	handler.Config = telegram.HandlerConfig{
		AllowedChats: cfg.AllowedChats,
		BotUsername:  cfg.BotUsername,
		MaxMsgLength: cfg.MaxMsgLength,
		AllowedUsers: cfg.AllowedUsers,
	}

	// 9. Build system prompt: explicit override > IDENTITY.md > compiled default
	systemMessage := buildSystemPrompt(resolved)

	// Telegram-specific Quick Facts and recovery guidance
	systemMessage += "\n\nQuick Facts (use these, do NOT search):\n"
	systemMessage += "- odek website: https://odek.21no.de\n"
	systemMessage += "- Built by: 21no.de (https://21no.de)\n"
	if resolved.GithubRepoUrl != "" {
		systemMessage += fmt.Sprintf("- Source code: %s\n", resolved.GithubRepoUrl)
	}
	systemMessage += "- Binary name: odek\n"
	systemMessage += "- Language: Go, minimal dependencies, ~11 MB binary\n"
	systemMessage += "\n"
	systemMessage += "File attachment:\n"
	systemMessage += "- You CAN send files (zip, pdf, images, csv, etc.) in BOTH intermediate replies\n"
	systemMessage += "  (via send_message with the 'file' parameter) AND in your final answer.\n"
	systemMessage += "- For final answers: include MEDIA:document:/absolute/path/to/file at the START\n"
	systemMessage += "  of your response to attach a file. The user will receive it as a native document.\n"
	systemMessage += "- For images: use MEDIA:photo:/path, for voice: MEDIA:voice:/path.\n"
	systemMessage += "- The file MUST exist on disk at the path you provide.\n"
	systemMessage += "\n"
	systemMessage += "Tool failure recovery:\n"
	systemMessage += "- If a tool fails with 'no such file' or returns empty, check pwd first.\n"
	systemMessage += "- NEVER run 'find /' or recursive searches from root — they hang.\n"
	systemMessage += "- A single failure means the path or assumption was wrong — fix that,\n"
	systemMessage += "  don't escalate to a broader search. Narrow, don't widen."

	// Set working directory to the configured repo directory.
	// This ensures tools like search_files scan the project, not /root.
	if resolved.GithubRepoDirectory != "" {
		if err := os.Chdir(resolved.GithubRepoDirectory); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: warning: failed to chdir to %s: %v\n", resolved.GithubRepoDirectory, err)
		}
	}

	// Telegram-specific system prompt additions
	//
	// Important: OnTextMessage processes in a background goroutine so it doesn't
	// block the main update processing loop. The TelegramApprover blocks waiting
	// for inline keyboard callbacks, which arrive via the main loop — only async
	// dispatch prevents deadlock.
	handler.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		go handleChatMessage(chatID, messageID, text, bot, handler, sessionManager,
			resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnCommand = func(chatID int64, messageID int, cmdName string, argsStr string) (string, error) {
		cmd := telegram.FindCommand(cmdName)
		if cmd == nil {
			return fmt.Sprintf("Unknown command: /%s", cmdName), nil
		}

		// Handle /restart — return confirmation message, then signal SIGHUP.
		// The message is sent through the standard response pipeline (MarkdownV2 +
		// retry logic). SIGHUP fires asynchronously after a short delay so the
		// pipeline has time to dispatch before graceful restart begins.
		if cmdName == "restart" {
			go func() {
				time.Sleep(500 * time.Millisecond)
				killFn(os.Getpid(), syscall.SIGHUP)
			}()
			return "🔄 *Restarting...*\n\nThe bot will restart momentarily. This may take a few seconds.", nil
		}

		// Handle /new — clear session and reset trust in the approver.
		if cmdName == "new" {
			sessionManager.Delete(chatID)
			if a := handler.GetApprover(chatID); a != nil {
				a.ResetTrust()
			}
			var b strings.Builder
			b.WriteString("🔄 *New session started*\n\n")
			b.WriteString(fmt.Sprintf("• Model: `%s`\n", resolved.Model))
			if resolved.Sandbox {
				b.WriteString("• Sandbox: enabled\n")
			}
			if resolved.Skills.Verbose {
				b.WriteString("• Skills verbose: on\n")
			}
			if resolved.GithubRepoDirectory != "" {
				repo := filepath.Base(resolved.GithubRepoDirectory)
				b.WriteString(fmt.Sprintf("• Repo: `%s`\n", repo))
			}
			b.WriteString("\n_Send a message to begin._")
			return b.String(), nil
		}

		// Handle /stats — read from session store.
		if cmdName == "stats" {
			cs, err := sessionManager.Load(chatID)
			if err != nil || cs == nil {
				return "📊 *Session Stats*\n\nNo active session yet. Send a message to start one.", nil
			}
			return formatStats(cs), nil
		}

		// Handle /sessions — list recent sessions from the store.
		if cmdName == "sessions" {
			infos, err := sessionManager.ListSessions(10)
			if err != nil {
				return fmt.Sprintf("❌ Failed to list sessions: %v", err), nil
			}
			if len(infos) == 0 {
				return "📋 *Sessions*\n\nNo sessions found. Start a conversation first.", nil
			}
			var b strings.Builder
			b.WriteString("📋 *Sessions*\n\n")
			for _, s := range infos {
				ago := time.Since(s.UpdatedAt).Round(time.Minute)
				fmt.Fprintf(&b, "`%s` — %d turns, %s ago\n", s.ID, s.Turns, ago)
				if s.Task != "" {
					taskPreview := s.Task
					if len(taskPreview) > 50 {
						taskPreview = taskPreview[:50] + "…"
					}
					fmt.Fprintf(&b, "  _%s_\n", taskPreview)
				}
			}
			b.WriteString("\nUse `/resume <id>` to continue a session.")
			return b.String(), nil
		}

		// Handle /resume <id> — switch to a different session.
		if cmdName == "resume" {
			sessionID := strings.TrimSpace(argsStr)
			if sessionID == "" {
				return "❗ Usage: `/resume <session-id>`\n\nUse `/sessions` to see available sessions.", nil
			}
			cs, err := sessionManager.ResumeSession(chatID, sessionID)
			if err != nil {
				return fmt.Sprintf("❌ %v", err), nil
			}
			taskPreview := cs.Messages[0].Content
			if len(taskPreview) > 80 {
				taskPreview = taskPreview[:80] + "…"
			}
			return fmt.Sprintf(
				"✅ *Session resumed*: `%s`\n\n%d turns • %d messages\n_%s_\n\nSend a message to continue.",
				cs.SessionID, cs.TurnCount, len(cs.Messages), taskPreview,
			), nil
		}

		// Handle /prune [days] — clean up old sessions and plans.
		if cmdName == "prune" {
			days := 30
			if strings.TrimSpace(argsStr) != "" {
				if d, err := strconv.Atoi(strings.TrimSpace(argsStr)); err == nil && d > 0 {
					days = d
				} else {
					return "❗ Usage: `/prune [days]`\n\nExample: `/prune 7` to remove sessions and plans older than 7 days.", nil
				}
			}
			sessionsRemoved, err := sessionManager.PruneSessions(days)
			if err != nil {
				return fmt.Sprintf("❌ Failed to prune sessions: %v", err), nil
			}
			plansRemoved, err := sessionManager.PrunePlans(days)
			if err != nil {
				return fmt.Sprintf("❌ Failed to prune plans: %v", err), nil
			}
			total := sessionsRemoved + plansRemoved
			if total == 0 {
				return fmt.Sprintf("📋 *Prune* — Nothing older than %d days found.", days), nil
			}
			var b strings.Builder
			b.WriteString(fmt.Sprintf("🧹 *Pruned* — Removed items older than %d days:\n\n", days))
			if sessionsRemoved > 0 {
				b.WriteString(fmt.Sprintf("• %d session(s)\n", sessionsRemoved))
			}
			if plansRemoved > 0 {
				b.WriteString(fmt.Sprintf("• %d plan(s)\n", plansRemoved))
			}
			return b.String(), nil
		}

		// Handle /plan <description> — dispatch to agent for plan generation.
		if cmdName == "plan" {
			description := strings.TrimSpace(argsStr)
			if description == "" {
				return "❗ Usage: `/plan <description>`\n\nExample: `/plan Add user authentication with OAuth2`", nil
			}
			slug := telegram.Slugify(description)
			prompt := fmt.Sprintf(
				"Create a detailed implementation plan for: %s\n\n"+
					"Save the plan as a markdown file to `~/.odek/plans/%s.md`. "+
					"The plan should include:\n"+
					"- Overview and goals\n"+
					"- Architecture / design\n"+
					"- Implementation steps (bite-sized tasks)\n"+
					"- File paths and key code locations\n"+
					"- Testing strategy\n\n"+
					"Use your write_file tool to save the plan.",
				description, slug,
			)
			go handleChatMessage(chatID, messageID, prompt, bot, handler, sessionManager,
				resolved, systemMessage, handlerLog)
			return fmt.Sprintf("📝 *Planning* `%s`…\n\n_Generating plan for: %s_", slug, description), nil
		}

		// Handle /plan-resume — inject most recent plan into session context.
		if cmdName == "plan_resume" {
			slug, content, err := telegram.MostRecentPlan()
			if err != nil {
				return fmt.Sprintf("❌ %v", err), nil
			}
			// Inject the plan as a system-level context message.
			cs, err := sessionManager.GetOrCreate(chatID)
			if err != nil {
				return fmt.Sprintf("❌ Failed to get session: %v", err), nil
			}
			contextMsg := fmt.Sprintf(
				"[Plan loaded: %s]\n\n%s\n\n---\nContinue working on this plan. "+
					"Use your tools to implement the next step.",
				slug, content,
			)
			cs.Messages = append(cs.Messages, llm.Message{Role: "user", Content: contextMsg})
			cs.LastActive = time.Now()
			if err := sessionManager.Save(chatID, cs.Messages); err != nil {
				return fmt.Sprintf("❌ Failed to save session: %v", err), nil
			}
			return fmt.Sprintf("📋 *Plan loaded*: `%s`\n\n_Injected into session context. Send a message to continue._", slug), nil
		}

		// Handle /stop — cancel the running agent task and report a summary.
		if cmdName == "stop" {
			// Cancel the running agent context, if any.
			if cancelVal, ok := chatCancels.LoadAndDelete(chatID); ok {
				cancel := cancelVal.(context.CancelFunc)
				cancel()
			}
			// Retrieve the latest run info for a summary of what was interrupted.
			var summary string
			if infoVal, ok := chatRunInfos.LoadAndDelete(chatID); ok {
				info := infoVal.(loop.IterationInfo)
				summary = formatStopSummary(info)
			} else {
				summary = "⏹️ No active task to stop."
			}
			return summary, nil
		}

		return cmd.Handler(argsStr)
	}

	handler.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		// Route clarify callbacks — the user clicked Yes/No on a clarify question.
		if strings.HasPrefix(data, "clarify:") {
			answer := strings.TrimPrefix(data, "clarify:")
			if ch, ok := sessionManager.GetClarifyChannel(chatID); ok {
				select {
				case ch <- answer:
				default:
					// Channel full or closed — clarify already resolved.
				}
			}
			return "✅ You chose **" + answer + "**", nil
		}

		// Route skill suggestion callbacks — Save or Skip.
		if strings.HasPrefix(data, "skill_save:") {
			skillName := strings.TrimPrefix(data, "skill_save:")
			userDir := expandHome("~/.odek/skills")
			os.MkdirAll(userDir, 0755)
			// Find and save the suggestion from the pending suggestions map
			if s, ok := pendingSuggestions.Load(skillName); ok {
				if suggestion, ok := s.(skills.SkillSuggestion); ok {
					if err := skills.SaveSuggestion(userDir, suggestion); err != nil {
						return fmt.Sprintf("✗ Error saving skill: %v", err), nil
					}
					pendingSuggestions.Delete(skillName)
					return fmt.Sprintf("✓ Saved skill %q", skillName), nil
				}
			}
			return "⚠️ Suggestion no longer available.", nil
		}
		if strings.HasPrefix(data, "skill_skip:") {
			skillName := strings.TrimPrefix(data, "skill_skip:")
			// Persist the skip so it won't be suggested again
			userDir := expandHome("~/.odek/skills")
			sl := skills.LoadSkipList(userDir)
			heuristic := ""
			if s, ok := pendingSuggestions.Load(skillName); ok {
				if suggestion, ok := s.(skills.SkillSuggestion); ok {
					heuristic = suggestion.Heuristic
				}
			}
			sl.RecordSkip(userDir, skillName, heuristic)
			pendingSuggestions.Delete(skillName)
			return fmt.Sprintf("⏭ Skipped %q (won't suggest again)", skillName), nil
		}

		return "", nil // approval callbacks are routed by the approver
	}

	handler.OnVoiceMessage = func(chatID int64, messageID int, fileID string) (string, error) {
		// Download the voice file so the agent can transcribe it.
		localPath, err := telegram.DownloadVoice(bot, fileID)
		if err != nil {
			handlerLog.Warn("voice download failed", "chat_id", chatID, "error", err)
			go handleChatMessage(chatID, messageID,
				fmt.Sprintf("[voice message received — download failed: %v]", err),
				bot, handler, sessionManager, resolved, systemMessage, handlerLog)
			return "", nil
		}
		go handleChatMessage(chatID, messageID,
			fmt.Sprintf("🎤 Voice message received and saved to %q. Use shell tools (ffmpeg, whisper) to transcribe and respond.", localPath),
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnPhotoMessage = func(chatID int64, messageID int, fileIDs []string) (string, error) {
		localPath, err := telegram.DownloadPhoto(bot, fileIDs)
		if err != nil {
			handlerLog.Warn("photo download failed", "chat_id", chatID, "error", err)
			go handleChatMessage(chatID, messageID,
				fmt.Sprintf("[photo received — download failed: %v]", err),
				bot, handler, sessionManager, resolved, systemMessage, handlerLog)
			return "", nil
		}
		go handleChatMessage(chatID, messageID,
			fmt.Sprintf("🖼 Photo received and saved to %q. Use vision tools or shell commands to analyze and respond.", localPath),
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnDocumentMessage = func(chatID int64, messageID int, fileID string, fileName string) (string, error) {
		localPath, err := telegram.DownloadDocument(bot, fileID, fileName)
		if err != nil {
			handlerLog.Warn("document download failed", "chat_id", chatID, "file_name", fileName, "error", err)
			go handleChatMessage(chatID, messageID,
				fmt.Sprintf("[document received — download failed: %v]", err),
				bot, handler, sessionManager, resolved, systemMessage, handlerLog)
			return "", nil
		}
		go handleChatMessage(chatID, messageID,
			fmt.Sprintf("📄 Document received and saved to %q. Use shell tools to analyze and respond.", localPath),
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnError = func(chatID int64, err error) {
		handlerLog.Error("handler error", "chat_id", chatID, "error", err)
	}

	// 11. Set command list via Telegram API.
	if err := bot.SetMyCommands(telegram.CommandDescriptors()); err != nil {
		handlerLog.Warn("set commands failed", "error", err)
	}

	// 12. Print startup banner.
	handlerLog.Info("telegram bot started")

	// 13. Create poller.
	poller := telegram.NewPoller(bot)
	poller.SetLogger(pollerLog)
	poller.Interval = time.Duration(cfg.PollInterval) * time.Second
	poller.Timeout = cfg.PollTimeout

	// 14. Create cancellable context for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 14b. Start health server if configured.
	if cfg.HealthAddr != "" {
		healthSrv := telegram.NewHealthServer(cfg.HealthAddr)
		healthSrv.SetLogger(rootLog.With("component", "health"))
		go func() {
			if err := healthSrv.Start(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "odek telegram: health server: %v\n", err)
			}
		}()
		// Mark ready — the server goroutine is already listening.
		healthSrv.SetReady()
	}

	// 15. Handle SIGINT/SIGTERM/SIGHUP.
	//     SIGHUP triggers gracefulRestart (docs at "Restart (spawn + exit)"
	//     above). SIGINT/SIGTERM do a graceful shutdown (cancel context →
	//     poller eventually stops → main update loop exits).
	sigCh := make(chan os.Signal, 3)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGHUP {
				gracefulRestart(bot, cancel)
				continue
			}
			fmt.Fprintf(os.Stderr, "\nodek telegram: shutting down...\n")
			cancel()
			return
		}
	}()

	// 15b. Check for restart marker from previous instance and notify any
	// chats that had active runs when the old instance restarted.
	if chatIDs, ok := readRestartMarker(); ok {
		msg := "🔄 *Bot restarted*\n\nA new instance is now running. Use `/new` if you experience any context issues."
		for _, chatID := range chatIDs {
			if _, err := bot.SendMessage(chatID, msg, &telegram.SendOpts{ParseMode: "Markdown"}); err != nil {
				fmt.Fprintf(os.Stderr, "odek telegram: restart notify chat %d: %v\n", chatID, err)
			}
		}
	}

	// 16. Start polling in a background goroutine.
	updates := make(chan telegram.Update, 100)
	go func() {
		if err := poller.Start(ctx, updates); err != nil && err != context.Canceled {
			fmt.Fprintf(os.Stderr, "odek telegram: poller stopped: %v\n", err)
			cancel() // signal shutdown
		}
	}()

	// 17. Process updates until the channel is closed (ctx cancelled).
	for upd := range updates {
		handler.HandleUpdate(upd)
	}

	// 18. Clean exit.
	return nil
}

// ── Restart (spawn + exit) ─────────────────────────────────────────────
//
// RESTART ARCHITECTURE
//
// The /restart Telegram command triggers a three-phase graceful restart
// that minimizes disruption to active users while avoiding the race
// conditions of in-process exec():
//
//   [1] /restart command (OnCommand handler, telegram.go:191-201)
//       └─ returns "Restarting…" message to Telegram
//       └─ after 500ms delay, sends SIGHUP to self
//
//   [2] SIGHUP handler (step 15, telegram.go:524-526)
//       └─ calls gracefulRestart()
//
//   [3] gracefulRestart() (telegram.go:665-729)
//       ├─ Phase 1: Notify + Cancel
//       │  ├─ sets restartInProgress flag (blocks new agent runs)
//       │  ├─ notifies all active chats via Telegram message
//       │  └─ cancels all running agent contexts
//       ├─ Phase 2: Bounded Drain
//       │  ├─ waits for activeTaskWG (all handleChatMessage goroutines)
//       │  └─ 15s timeout — force proceeds even if tasks hang
//       └─ Phase 3: Marker + Spawn → Exit
//          ├─ writes restart.json marker (chat IDs for post-startup notify)
//          ├─ spawnChild() → new process via os.StartProcess()
//          └─ os.Exit(0) — child is independent, this process is done
//
//   [4] New (child) process startup
//       ├─ acquireLock() reads ~/.odek/telegram.pid
//       │  └─ finds stale PID (old process is already dead from os.Exit)
//       │  └─ writes own PID — lock acquired
//       ├─ reads restart.json marker (written in step 3)
//       ├─ notifies previously active chats: "Bot restarted"
//       └─ begins polling Telegram for updates
//
// WHY spawn+exit INSTEAD OF syscall.Exec
//
// syscall.Exec replaces the current process image — it reuses the same PID,
// same process group, same TCP connections. This causes:
//   * Binary race: the old binary file must stay intact while exec reads it
//     (impossible during `go build` → restart)
//   * Stale HTTP/2 connections: the HTTP client's connection pool is in an
//     indeterminate state after exec
//   * Session loops: open file descriptors leak into the new image
//
// Instead, os.StartProcess creates a fully independent child. The parent
// exits immediately (os.Exit(0)). The child gets a fresh PID, fresh process
// group, fresh network connections. The child's acquireLock handles the PID
// file race naturally (see acquireLock docs below).
//
// WHY os.Exit(0) INSTEAD OF cancel()
//
// The poller calls GetUpdates with a 30s long-poll timeout to Telegram's
// API (poller.go:71, bot.go:415-426). The underlying HTTP client uses
// http.Client.Post() which does NOT respect context.Context cancellation
// (bot.go:95). So when gracefulRestart calls cancel():
//
//   cancel() → polls' ctx.Done() fires
//            → but the poller goroutine is blocked on GetUpdates
//            → GetUpdates() ignores the cancelled context
//            → poller can't close the updates channel for up to 30s
//            → main goroutine stuck in for upd := range updates
//            → process stays alive, fully unresponsive
//            → only the child's acquireLock SIGTERM→5s→SIGKILL kills it
//
// Result: 5-35 seconds of unresponsiveness for what should be an instant
// restart. The root fix would be to make GetUpdates/Bot.doJSON accept a
// context.Context and pass it to http.NewRequestWithContext. Until then,
// os.Exit(0) is the correct escape: the child is already an independent
// process, so the parent has no reason to linger.
//
// EDGE CASES
//
// 1. spawnChild() fails (e.g. binary deleted mid-restart):
//    → restartInProgress is reset, bot continues running
//    → users see "Restarting…" but bot stays alive — no data loss
//
// 2. os.Exit(0) skips deferred cleanup (lock.release removes PID file):
//    → the stale PID file remains in ~/.odek/telegram.pid
//    → child's acquireLock reads it, finds old PID dead
//    → writes own PID — clean takeover with no race
//
// 3. Concurrent /restart calls:
//    → restartInProgress atomic bool guards against double-restart
//    → second call returns immediately (CAS false→true fails)
//
// 4. Agent run in progress during restart:
//    → Phase 1 cancels agent context
//    → Phase 2 waits for handleChatMessage to complete (15s max)
//    → session is saved to disk before the goroutine exits
//    → user can resume after restart via /resume

// restartMarkerPath returns the path to the restart marker file.
func restartMarkerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".odek", "restart.json"), nil
}

// restartMarker holds structured data written before a restart and read by
// the new instance. Fields tells the new instance which chats were active.
type restartMarker struct {
	ChatIDs []int64 `json:"chat_ids,omitempty"`
}

// writeRestartMarker writes a marker so the next instance knows a restart
// just happened. It records the chat IDs of any active agent runs so the
// new instance can notify those users.
func writeRestartMarker() error {
	marker := restartMarker{}
	chatCancels.Range(func(key, value any) bool {
		marker.ChatIDs = append(marker.ChatIDs, key.(int64))
		return true
	})
	// Sort for deterministic output.
	sort.Slice(marker.ChatIDs, func(i, j int) bool {
		return marker.ChatIDs[i] < marker.ChatIDs[j]
	})

	// Marshal — fallback to empty marker on error (non-fatal).
	data, err := json.Marshal(marker)
	if err != nil {
		data = []byte("{}\n")
	}

	path, err := restartMarkerPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// readRestartMarker reads and removes the restart marker. Returns the list
// of chat IDs that had active agent runs at restart time, and true if a
// marker existed.
func readRestartMarker() ([]int64, bool) {
	path, err := restartMarkerPath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	os.Remove(path)

	var marker restartMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		// Legacy empty marker or corrupt — signal restart happened.
		return nil, true
	}
	return marker.ChatIDs, true
}

// countSyncMap returns the number of entries in a sync.Map.
func countSyncMap(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// gracefulRestart performs a three-phase graceful restart:
//
// Phase 1: Notify + Cancel
//
//	Sets restart flag to block new tasks, notifies all active chats
//	about the imminent restart, cancels their agent contexts.
//
// Phase 2: Bounded Drain
//
//	Waits for all handleChatMessage goroutines to finish (bounded
//	to 15 seconds). Context cancellation makes RunWithMessages return,
//	which triggers deferred cleanup (session save, lock release) in
//	each goroutine.
//
// Phase 3: Marker + Spawn → Exit
//
//	Writes the restart marker (with active chat IDs for post-startup
//	notification) and spawns a child process via os.StartProcess.
//	On success, the parent calls os.Exit(0) immediately instead of
//	waiting for graceful shutdown to propagate through the poller
//	(which is blocked on a 30s long-poll GetUpdates call that does
//	not respect context cancellation — see the "WHY os.Exit(0)"
//	section in the file header).
//	On spawn failure, resets restartInProgress, logs the error, and
//	returns so the current instance continues running. The bot stays
//	alive and users can retry /restart.
//
// Must be called from the SIGHUP handler goroutine only.
func gracefulRestart(bot *telegram.Bot, cancel context.CancelFunc) {
	// Guard: only one restart at a time.
	if !restartInProgress.CompareAndSwap(false, true) {
		return
	}

	activeCount := countSyncMap(&chatCancels)
	fmt.Fprintf(os.Stderr, "odek telegram: graceful restart — phase 1: notifying %d active chat(s)...\n", activeCount)

	// ── Phase 1: Notify + Cancel ──────────────────────────────────────
	chatCancels.Range(func(key, value any) bool {
		chatID := key.(int64)
		cancel := value.(context.CancelFunc)

		// Best-effort notification — the API call might fail during shutdown.
		if _, err := bot.SendMessage(chatID,
			"🔄 *Bot restarting* — your current task was interrupted.\n\n"+
				"Your conversation has been saved. The new instance will be ready "+
				"in a few seconds. Use `/new` to start fresh.",
			&telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2}); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: restart notify chat %d: %v\n", chatID, err)
		}

		cancel()
		return true
	})

	// ── Phase 2: Bounded Drain ─────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "odek telegram: graceful restart — phase 2: draining tasks...\n")
	waitCh := make(chan struct{})
	go func() {
		activeTaskWG.Wait()
		close(waitCh)
	}()

	select {
	case <-waitCh:
		fmt.Fprintf(os.Stderr, "odek telegram: all tasks finished cleanly\n")
	case <-time.After(15 * time.Second):
		fmt.Fprintf(os.Stderr, "odek telegram: drain timeout (15s), force-restarting\n")
	}

	// ── Phase 3: Marker + Spawn → Exit or Recover ──────────────────────
	fmt.Fprintf(os.Stderr, "odek telegram: graceful restart — phase 3: spawning child...\n")
	writeRestartMarker()
	if err := spawnChild(); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: spawn failed: %v\n", err)
		restartInProgress.Store(false) // allow new tasks to start
		return
	}

	// Child spawned successfully — exit immediately.
	//
	// cancel()+graceful shutdown would wait for the poller's long-poll
	// GetUpdates HTTP call to return (up to 30s), during which the bot is
	// unresponsive. The child's acquireLock does send SIGTERM→5s→SIGKILL,
	// but that's a 5-35s window of unresponsiveness — a poor UX for what
	// should be an instant restart.
	//
	// Since the child is an independent process already running via
	// os.StartProcess, the cleanest path is to exit right here. The child's
	// acquireLock reads the stale PID file, finds this PID is dead,
	// and writes its own without issue.
	os.Exit(0)
}

// spawnChild starts a new odek telegram process detached from the parent.
// Stderr is redirected to /tmp/odek-telegram.log so startup errors are visible.
func spawnChild() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("executable: %w", err)
	}
	// Copy args (same as current process).
	argv := make([]string, len(os.Args))
	copy(argv, os.Args)
	argv[0] = exe

	// Open stderr log file for the child so startup errors aren't lost.
	stderr, err := os.OpenFile("/tmp/odek-telegram.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		stderr = nil // fallback: child gets /dev/null
	}

	// Build child environment: parent env + re-injected API key.
	// config.LoadConfig clears ODEK_API_KEY from the environment, so it's
	// missing from os.Environ(). Re-inject it so the child can start.
	childEnv := os.Environ()
	if resolvedAPIKey != "" {
		childEnv = append(childEnv, "ODEK_API_KEY="+resolvedAPIKey)
	}

	attr := &os.ProcAttr{
		Env: childEnv,
		// Detach: nil stdin/stdout so child is reparented to init.
		// Stderr is captured to the log file for debugging.
		Files: []*os.File{nil, nil, stderr},
	}
	_, err = os.StartProcess(exe, argv, attr)
	return err
}

// handleChatMessage processes a user message from Telegram in a background
// goroutine. It creates or loads the chat session, creates a TelegramApprover
// for approval prompts, runs the agent loop with RunWithMessages, and sends
// back the response. Each chat gets its own TelegramApprover instance.
func handleChatMessage(
	chatID int64,
	messageID int,
	text string,
	bot *telegram.Bot,
	handler *telegram.Handler,
	sessionManager *telegram.SessionManager,
	resolved config.ResolvedConfig,
	systemMessage string,
	log telegram.Logger,
) {
	// Serialize per chat: only one agent loop runs per chat at a time.
	// Prevents same-chat message racing that would corrupt session history.
	mu := getChatMutex(chatID)
	if !mu.TryLock() {
		// Another agent run is in progress — tell the user we're queued.
		bot.SendMessage(chatID, "⏳ Working on your previous request — queued.", nil)
		mu.Lock()
	}
	defer mu.Unlock()

	// Recover from panics so a single bad agent run doesn't deadlock the chat.
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in handleChatMessage", "chat_id", chatID, "panic", r)
			reportError(bot, chatID, messageID, fmt.Sprintf(
				"Internal error: %v\n\nThe bot is still running. Use /new to start a fresh session.", r,
			))
		}
	}()

	// ── Restart guard ─────────────────────────────────────────────────
	// If a graceful restart is in progress, don't start new agent runs.
	// The user can resend their message after restart completes.
	if restartInProgress.Load() {
		bot.SendMessage(chatID,
			"⏳ Bot is restarting — please try again in a few seconds.",
			&telegram.SendOpts{ReplyToMessageID: messageID})
		return
	}

	// Register as an active task so graceful shutdown waits for us.
	// NOTE: Must happen AFTER the restartInProgress check so we never
	// Add() when bailing out. When restartInProgress is true, the
	// goroutine returns without ever calling RunWithMessages, so there's
	// no task to wait for.
	activeTaskWG.Add(1)
	defer activeTaskWG.Done()

	// Create a per-chat TelegramApprover for inline keyboard approval.
	approver := telegram.NewTelegramApprover(bot, chatID)
	handler.SetApprover(chatID, approver)
	defer handler.DeleteApprover(chatID)

	// Get or create the session for this chat.
	cs, err := sessionManager.GetOrCreate(chatID)
	if err != nil {
		reportError(bot, chatID, messageID, "Failed to create session: "+err.Error())
		return
	}

	// Append user message to session.
	cs.Messages = append(cs.Messages, llm.Message{Role: "user", Content: text})
	cs.LastActive = time.Now()

	// Build the agent with Telegram approver.
	tools := builtinTools(resolved.Dangerous, nil, approver, resolved.MaxConcurrency)

	modelLabel := odek.ProfileLabel(resolved.Model)
	if modelLabel == "" {
		modelLabel = "deepseek-chat"
	}

	rend := render.New(os.Stderr, false).WithModel(modelLabel)

	// ── Pre-flight budget check ────────────────────────────────────
	// Before running the agent, check if the daily budget is already
	// exhausted — avoids burning an API call just to be rejected.
	// Use a 5% buffer: if >95% of budget is consumed, refuse new runs
	// to leave headroom for the response delivery (which uses tokens too).
	if resolved.Telegram.DailyTokenBudget > 0 {
		if err := bot.CheckDailyBudget(1); err != nil {
			reportError(bot, chatID, messageID, fmt.Sprintf(
				"Daily token budget exhausted: %v. "+
					"The budget resets at midnight UTC. "+
					"Set daily_token_budget to 0 in config for unlimited usage.",
				err,
			))
			return
		}
		// Warn if budget is running low (>80% consumed).
		used, limit := bot.DailyTokenUsage()
		if limit > 0 && used > 0 {
			pct := used * 100 / limit
			if pct >= 80 {
				sendAsync(bot, chatID, fmt.Sprintf(
					"⚠️ *Budget: %d%% used* (%d/%d tokens)\\. "+
						"Consider `/new` to trim conversation history or set a higher `daily_token_budget`\\.",
					pct, used, limit,
				), &telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2, ReplyToMessageID: messageID})
			}
		}
	}

	// ── Typing Indicator ────────────────────────────────────────────
	// Send "typing" action every 4s while the agent runs (Telegram shows
	// it for ~5s). Circuit breaker: stop after 3 consecutive failures
	// to prevent log spam when the API is unreachable or rate-limited.
	typingDone := make(chan struct{})
	defer close(typingDone)
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		consecutiveFails := 0
		for {
			select {
			case <-ticker.C:
				if consecutiveFails >= 3 {
					return // circuit breaker tripped
				}
				if err := bot.SendChatAction(chatID, "typing"); err != nil {
					consecutiveFails++
					fmt.Fprintf(os.Stderr, "odek telegram: sendChatAction failed (%d/3): %v\n", consecutiveFails, err)
				} else {
					consecutiveFails = 0
				}
			case <-typingDone:
				return
			}
		}
	}()
	
	// ── Engaging Mode: Immediate Progress ──────────────────────────
	// Send an instant "working on it" message so the user sees feedback
	// within milliseconds, not seconds. This message gets updated with
	// narrator-style progress on each tool call, then deleted when the
	// final answer arrives.
	var narrator *narrate.Narrator
	isEngaging := resolved.InteractionMode != "verbose"
	if isEngaging {
		narrator = narrate.New(true)
	}
	var progressMsgID int
	if isEngaging {
		msg, err := bot.SendMessage(chatID, "🤔 Looking into that...",
			&telegram.SendOpts{ReplyToMessageID: messageID})
		if err == nil {
			progressMsgID = msg.ID
		}
	}
	defer func() {
		// Clean up the progress message when the task finishes.
		// The final answer replaces it entirely — this avoids cluttering
		// the chat with a stale "thinking" message.
		if progressMsgID != 0 {
			bot.DeleteMessage(chatID, progressMsgID)
		}
	}()

	// ── Tool Tracing ───────────────────────────────────────────────
	// Individual messages per tool call so the user can follow the
	// agent's journey step by step. Reasoning is shown before tool
	// calls. All trace messages are deleted after the agent responds.
	// toolMsgIDs tracks per-turn tool message IDs for editing on result.
	var toolMsgIDs sync.Map // map[string]int

	// truncate shortens a string for display, appending "…" if trimmed.
	truncate := func(s string, max int) string {
		if len(s) > max {
			return s[:max] + "…"
		}
		return s
	}

	// truncateWords limits text to maxWords, appending "…" if trimmed.
	truncateWords := func(s string, maxWords int) string {
		words := strings.Fields(s)
		if len(words) <= maxWords {
			return s
		}
		return strings.Join(words[:maxWords], " ") + "…"
	}

	// Collect agent run stats via the iteration callback.
	var runInfo loop.IterationInfo
	var allToolsMu sync.Mutex
	allTools := make(map[string]int)

	// ── Clarify Tool ───────────────────────────────────────────────
	// Wire the clarify tool with a Telegram-native answer function.
	// When the agent calls clarify(question), the bot sends an inline
	// keyboard message and blocks until the user responds.
	agentTools := append([]odek.Tool{}, tools...)
	agentTools = append(agentTools, toolpkg.NewClarifyTool(func(question string) (string, error) {
		ch := make(chan string, 1)
		sessionManager.SetClarifyChannel(chatID, ch)
		defer sessionManager.DeleteClarifyChannel(chatID)

		// Send the question with Yes/No buttons.
		replyMarkup := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					{Text: "Yes", CallbackData: "clarify:yes"},
					{Text: "No", CallbackData: "clarify:no"},
				},
			},
		}
		if _, err := bot.SendMessage(chatID, "❓ "+question,
			&telegram.SendOpts{ReplyMarkup: replyMarkup, ParseMode: "Markdown", ReplyToMessageID: messageID}); err != nil {
			return "", fmt.Errorf("clarify: send message: %w", err)
		}

		// Wait for the user to click a button (or timeout).
		select {
		case answer := <-ch:
			return answer, nil
		case <-time.After(10 * time.Minute):
			return "", fmt.Errorf("clarify: timed out waiting for response")
		case <-typingDone:
			return "", fmt.Errorf("clarify: task cancelled by /stop")
		}
	}))

	// ── Send Message Tool ──────────────────────────────────────────
	// Wire the send_message tool so the agent can send intermediate
	// messages, files, and interactive keyboards mid-task — not just
	// at the final answer.
	agentTools = append(agentTools, toolpkg.NewSendMessageTool(
		func(text string, file string, buttons [][]map[string]string) error {
			if file != "" {
				// Detect media type from extension.
				mediaType := mediaTypeFromExt(file)
				return sendTelegramMedia(bot, chatID, mediaType, file, text, buttons)
			}
			if len(buttons) > 0 {
				markup := buttonsToMarkup(buttons)
				_, err := bot.SendMessage(chatID, text, &telegram.SendOpts{
					ParseMode:   telegram.ParseModeMarkdownV2,
					ReplyMarkup: markup,
				})
				return err
			}
			_, err := bot.SendMessage(chatID, text, &telegram.SendOpts{
				ParseMode: telegram.ParseModeMarkdownV2,
			})
			return err
		}))

	// Resolve skills config (same logic as main.go run command).
	var skillsCfg *skills.SkillsConfig
	if resolved.Skills.Learn {
		skillsCfg = &resolved.Skills
	}

	agentCfg := odek.Config{
		Model:           resolved.Model,
		BaseURL:         resolved.BaseURL,
		APIKey:          resolved.APIKey,
		MaxIterations:   resolved.MaxIter,
		MaxToolParallel: resolved.MaxToolParallel,
		SystemMessage:   systemMessage,
		RuntimeContext:  odek.BuildRuntimeContext("telegram"),
		InteractionMode: resolved.InteractionMode,
		NoProjectFile:   resolved.NoAgents,
		Skills:          skillsCfg,
		Thinking:        resolved.Thinking,
		Tools:           agentTools,
		Renderer:        rend,
		ToolEventHandler: func(event string, name string, data string) {
			// Engaging mode: update the progress message with narrated tool
			// descriptions instead of raw traces.
			if resolved.InteractionMode != "verbose" {
				if progressMsgID != 0 && event == "tool_call" && narrator != nil {
					if msg := narrator.ToolCallMessage(name, data); msg != "" {
						bot.EditMessageText(chatID, progressMsgID, msg, nil)
					}
				}
				return
			}

			// Verbose mode: individual messages per tool call so the user
			// can follow the agent's journey step by step.
			switch event {
			case "tool_call":
				args := truncate(data, 150)
				line := fmt.Sprintf("%s `%s` %s", render.ToolEmoji(name), name, args)
				if msg, err := bot.SendMessage(chatID, line, nil); err == nil {
					toolMsgIDs.Store(name, msg.ID)
				}

			case "tool_result":
				// Send a new message for the result so the full timeline
				// is preserved in chat history.
				sizeLabel := fmt.Sprintf("%dB", len(data))
				if len(data) > 1024 {
					sizeLabel = fmt.Sprintf("%dKB", len(data)/1024)
				}
				bot.SendMessage(chatID,
					fmt.Sprintf("%s `%s` ✅ (%s)", render.ToolEmoji(name), name, sizeLabel), nil)
				// Clean up the initial tool_call message — it's stale now.
				if msgIDVal, ok := toolMsgIDs.Load(name); ok {
					bot.DeleteMessage(chatID, msgIDVal.(int))
				}
				toolMsgIDs.Delete(name)
			}
		},
		IterationCallback: func(info loop.IterationInfo) {
			// Pre-tool callback: show LLM reasoning before tools run.
			if info.IsPreTool {
				if info.ReasoningContent != "" {
					reasoning := truncateWords(info.ReasoningContent, 50)
					if reasoning != "" {
						bot.SendMessage(chatID, "💭 "+reasoning,
							&telegram.SendOpts{ReplyToMessageID: messageID})
					}
				}
				return
			}

			// Post-tool / final-answer callback: collect tool stats.
			allToolsMu.Lock()
			for _, name := range info.ToolNames {
				if _, ok := allTools[name]; !ok {
					allTools[name] = 0
				}
				allTools[name]++
			}
			allToolsMu.Unlock()

			// Always capture the latest iteration info — used by /stop
			// to report a summary of the interrupted task.
			runInfo = info
			chatRunInfos.Store(chatID, info)
		},
		SkillEventHandler: func(event skills.SkillEvent) {
			switch event.Type {
			case "loaded":
				if skillsCfg != nil && skillsCfg.Verbose {
					names := strings.Join(event.Skills, ", ")
					sendAsync(bot, chatID, "📚 Loaded skill: "+names, &telegram.SendOpts{ReplyToMessageID: messageID})
				}
			case "autoloaded":
				if skillsCfg != nil && skillsCfg.Verbose {
					names := strings.Join(event.Skills, ", ")
					sendAsync(bot, chatID, "📚 Auto-loaded skills: "+names, &telegram.SendOpts{ReplyToMessageID: messageID})
				}
			case "saved":
				if skillsCfg != nil && skillsCfg.Verbose {
					sendAsync(bot, chatID, fmt.Sprintf("✓ Saved skill %q", event.SkillName), &telegram.SendOpts{ReplyToMessageID: messageID})
				}
			case "deleted":
				if skillsCfg != nil && skillsCfg.Verbose {
					sendAsync(bot, chatID, fmt.Sprintf("✗ Deleted skill %q", event.SkillName), &telegram.SendOpts{ReplyToMessageID: messageID})
				}
			case "suggested":
				replyMarkup := &telegram.InlineKeyboardMarkup{
					InlineKeyboard: [][]telegram.InlineKeyboardButton{
						{
							{Text: "💾 Save", CallbackData: "skill_save:" + event.SkillName},
							{Text: "⏭ Skip", CallbackData: "skill_skip:" + event.SkillName},
						},
					},
				}
				// Build message with preview from the event body
				msg := fmt.Sprintf("🔍 *Skill suggestion:* %s\n_%s_",
					event.SkillName, event.Heuristic)
				if len(event.Body) > 0 {
					preview := event.Body
					if len(preview) > 400 {
						if lastNL := strings.LastIndexByte(preview[:400], '\n'); lastNL > 200 {
							preview = preview[:lastNL]
						} else {
							preview = preview[:400]
						}
						preview += "\n_... (truncated)_"
					}
					msg += fmt.Sprintf("\n\n```\n%s\n```", preview)
				}
			sendAsync(bot, chatID, msg,
				&telegram.SendOpts{ReplyMarkup: replyMarkup, ParseMode: "Markdown", ReplyToMessageID: messageID})
			}
		},
		Approver: approver,
	}

	agent, err := odek.New(agentCfg)
	if err != nil {
		reportError(bot, chatID, messageID, "Failed to create agent: "+err.Error())
		return
	}
	defer agent.Close()

	// Create a cancellable context so /stop can interrupt the agent loop.
	agentCtx, agentCancel := context.WithCancel(context.Background())
	chatCancels.Store(chatID, agentCancel)
	defer func() {
		agentCancel()
		chatCancels.LoadAndDelete(chatID)
		chatRunInfos.LoadAndDelete(chatID)
	}()

	// Run the agent with the full message history (multi-turn).
	response, updatedMessages, err := agent.RunWithMessages(agentCtx, cs.Messages)
	if err != nil {
		// Clean up any tool trace messages on error.
		deleteToolTraceMessages(bot, chatID, &toolMsgIDs)

		// If the context was cancelled (by /stop or restart), save partial
		// state and notify the user with a cancellation summary.
		if errors.Is(err, context.Canceled) {
			// Save partial session state so the conversation isn't lost.
			cs.LastActive = time.Now()
			if saveErr := sessionManager.Save(chatID, cs.Messages); saveErr != nil {
				log.Error("save session after cancel", "chat_id", chatID, "error", saveErr)
			}
			// Send a cancellation summary so the user knows what happened.
			cancelMsg := "⏹️ *Task cancelled.*"
			if infoVal, ok := chatRunInfos.LoadAndDelete(chatID); ok {
				info := infoVal.(loop.IterationInfo)
				cancelMsg += "\n\n" + formatStopSummary(info)
			}
			cancelMsg += "\n\n_Session state saved. Send a new message to continue._"
			sendAsync(bot, chatID, cancelMsg,
				&telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2, ReplyToMessageID: messageID})
			return
		}

		reportError(bot, chatID, messageID, "Agent error: "+err.Error())
		return
	}

	// Bill actual token usage against daily budget (if configured).
	tokensUsed := int64(runInfo.InputTokens + runInfo.OutputTokens)
	if tokensUsed > 0 {
		if err := bot.CheckDailyBudget(tokensUsed); err != nil {
			// Budget exceeded — report it but still deliver the response.
			log.Warn("daily token budget exceeded",
				"chat_id", chatID, "tokens", tokensUsed, "error", err)
			bot.SendMessage(chatID, fmt.Sprintf(
				"⚠️ *Token budget warning*\\n\\n%v\\n\\n"+
					"Further agent runs may be blocked until the daily budget resets. "+
					"Use `/stats` to check current usage.",
				err,
			), &telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2, ReplyToMessageID: messageID})
		}
	}

	// Save the updated session messages.
	cs.Messages = updatedMessages
	cs.TurnCount++
	if err := sessionManager.Save(chatID, cs.Messages); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: session save: %v\n", err)
	}

	// Send the response, then append compact stats as a separate message.
	if response != "" {
		handler.SendResponse(chatID, response, messageID)

		// Clean up all tool trace messages — they're stale after the response.
		deleteToolTraceMessages(bot, chatID, &toolMsgIDs)

		// Send run stats as a separate message directly via Bot.SendMessage
		// (bypassing SendResponse/FormatResponse) so MarkdownV2 backtick code
		// formatting is handled natively by Telegram's parser.
		if runInfo.Turn > 0 {
			allToolsMu.Lock()
			toolList := sortedToolKeys(allTools)
			allToolsMu.Unlock()

			statsLine := formatTelegramStats(runInfo, toolList)
			if _, err := bot.SendMessage(chatID, statsLine, &telegram.SendOpts{
				ParseMode:        telegram.ParseModeMarkdownV2,
				ReplyToMessageID: messageID,
			}); err != nil {
				// Fallback: send as plain text so the info isn't lost
				if _, err2 := bot.SendMessage(chatID, statsLine, &telegram.SendOpts{
					ReplyToMessageID: messageID,
				}); err2 != nil {
					fmt.Fprintf(os.Stderr, "odek telegram: stats send fallback failed: %v (orig: %v)\n", err2, err)
				}
			}
		} else {
			fmt.Fprintf(os.Stderr, "odek telegram: stats skipped (runInfo.Turn=%d)\n", runInfo.Turn)
		}
	}

	// ── Learn loop: run self-improvement heuristics ──
	if skillsCfg != nil && skillsCfg.Learn && agent.SkillManager() != nil {
		sm := agent.SkillManager()
		suggestions := learnAndSuggest(cs.Messages, sm, nil, false, skillsCfg.AutoSave.Enabled)
		userDir := expandHome("~/.odek/skills")
		os.MkdirAll(userDir, 0755)

		// Filter skipped suggestions
		filtered, skipped := skills.FilterSkipped(suggestions, userDir,
			skillsCfg.Curation.SkipThreshold, skillsCfg.Curation.SkipResetDays)

		// Auto-save if enabled
		if skillsCfg.AutoSave.Enabled {
			result := skills.AutoSaveSuggestions(filtered, userDir, *skillsCfg)
			for _, name := range result.Saved {
				sm.Notifier.Notify(skills.SkillEvent{
					Type: "saved", SkillName: name, Timestamp: time.Now().UTC(),
				})
			}
			if len(result.Saved) > 0 {
				sm.MarkDirty()
				sm.Reload()
				// Run micro-curation
				allSkills := sm.AllSkills()
				var newSkills []skills.Skill
				for _, s := range allSkills {
					if s.Quality == skills.QualityDraft {
						newSkills = append(newSkills, s)
					}
				}
				msg := skills.RunAutoCurate(userDir, newSkills, allSkills, *skillsCfg, nil)
			if msg != "" && skillsCfg.Verbose {
				sendAsync(bot, chatID, msg, nil)
			}
			}
		} else {
			// Store suggestions for inline keyboard callback handling
			for _, s := range filtered {
				pendingSuggestions.Store(s.Name, s)
			}
		}

		if skipped > 0 {
			log.Debug("skill suggestions suppressed by skip list", "count", skipped)
		}
	}

	// ── Media cleanup ────────────────────────────────────────────────
	// Remove downloaded media files older than 1 hour in the background.
	// These are only needed during the agent turn for analysis/transcription.
	go func() {
		if removed, err := telegram.CleanupMedia(1 * time.Hour); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: media cleanup: %v\n", err)
		} else if removed > 0 {
			log.Debug("cleaned up old media files", "count", removed)
		}
	}()
}

// formatStats formats session statistics for the Telegram stats command.
func formatStats(cs *telegram.ChatSession) string {
	duration := time.Since(cs.CreatedAt).Truncate(time.Second)

	return fmt.Sprintf(
		"📊 *Session Stats*\n\n"+
			"Messages: %d\n"+
			"Turns: %d\n"+
			"Started: %s\n"+
			"Duration: %s\n"+
			"Last active: %s",
		len(cs.Messages),
		cs.TurnCount,
		cs.CreatedAt.Format("Jan 02, 2006 15:04 UTC"),
		duration.String(),
		cs.LastActive.Format("15:04 UTC"),
	)
}

// ── Progress Callback Helpers ──────────────────────────────────────────

// sortedToolKeys returns the keys of a map sorted alphabetically.
func sortedToolKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formatTelegramStats formats the final agent run statistics for Telegram.
// Returns a compact Markdown code-formatted line.
func formatTelegramStats(info loop.IterationInfo, toolList []string) string {
	toolStr := "none"
	if len(toolList) > 0 {
		toolStr = strings.Join(toolList, ", ")
	}

	latency := info.TotalLatency.Truncate(time.Second)
	iters := fmt.Sprintf("%d turn", info.Turn)
	if info.Turn != 1 {
		iters += "s"
	}

	// Always include cache stats so the user can see them even when zero.
	cacheStr := fmt.Sprintf(" · cache: %dcr+%drd+%dct",
		info.CacheCreationTokens, info.CacheReadTokens, info.CachedTokens)

	return fmt.Sprintf(
		"```\n✅ Done · %s · %d in / %d out%s · %s — tools: %s\n```",
		iters, info.InputTokens, info.OutputTokens, cacheStr, latency.String(), toolStr,
	)
}

// formatStopSummary formats a summary of an interrupted task for the /stop
// command response. It includes turns completed, tokens consumed, tools used,
// and total wall-clock time before cancellation.
func formatStopSummary(info loop.IterationInfo) string {
	toolStr := "none"
	if len(info.ToolNames) > 0 {
		// Deduplicate and sort tool names for a clean display.
		seen := make(map[string]struct{}, len(info.ToolNames))
		unique := make([]string, 0, len(info.ToolNames))
		for _, name := range info.ToolNames {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				unique = append(unique, name)
			}
		}
		sort.Strings(unique)
		toolStr = strings.Join(unique, ", ")
	}

	latency := info.TotalLatency.Truncate(time.Second)
	iters := fmt.Sprintf("%d turn", info.Turn)
	if info.Turn != 1 {
		iters += "s"
	}

	return fmt.Sprintf(
		"⏹️ *Task Interrupted*\n\n"+
			"%s · %d in / %d out · %s — tools: %s",
		iters, info.InputTokens, info.OutputTokens, latency.String(), toolStr,
	)
}

// reportError sends an error message to the given chat and logs to stderr.
func reportError(bot *telegram.Bot, chatID int64, messageID int, msg string) {
	fmt.Fprintf(os.Stderr, "odek telegram: %s\n", msg)
	if _, err := bot.SendMessage(chatID, "❌ "+msg, &telegram.SendOpts{ReplyToMessageID: messageID}); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: send error message: %v\n", err)
	}
}

// sendAsync sends a Telegram message in a background goroutine and logs
// any errors to stderr. Use this instead of raw go bot.SendMessage() to
// prevent silent delivery failures. Recovers from panics so a single bad
// notification doesn't leak a goroutine.
func sendAsync(bot *telegram.Bot, chatID int64, text string, opts *telegram.SendOpts) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "odek telegram: async send panic: %v\n", r)
			}
		}()
		if _, err := bot.SendMessage(chatID, text, opts); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: async send failed: %v\n", err)
		}
	}()
}

// ── Singleton Lock ─────────────────────────────────────────────────────
//
// Prevents two bot instances from polling Telegram simultaneously (which
// causes 409 Conflict errors). Uses a PID file at ~/.odek/telegram.pid.
//
// LIFECYCLE
//
// Normal startup (no previous instance):
//
//	acquireLock() → PID file doesn't exist → writes own PID → OK
//
// Competing startup (existing instance still alive):
//
//	acquireLock() → reads PID file → kills old process (SIGTERM→5s→SIGKILL)
//	               → old process dies → writes own PID → OK
//
// Restart (child starts after parent's os.Exit(0)):
//
//	acquireLock() → reads PID file → finds old (dead) PID
//	               → syscall.Kill(pid, 0) fails (process gone)
//	               → writes own PID → OK
//
// Note: During restart, the parent's deferred lock.release() never runs
// (os.Exit(0) skips defers). The stale PID file is harmless — the child's
// acquireLock simply finds a dead PID and overwrites it.

type instanceLock struct {
	pidFile string
}

// acquireLock reads any existing PID file, kills the old process if still
// alive, then writes the current PID. Returns the lock for deferred release.
func acquireLock() (*instanceLock, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	pidFile := filepath.Join(home, ".odek", "telegram.pid")

	// Ensure parent dir exists.
	if err := os.MkdirAll(filepath.Dir(pidFile), 0755); err != nil {
		return nil, fmt.Errorf("mkdir pid: %w", err)
	}

	// Read stale PID and kill it.
	if data, err := os.ReadFile(pidFile); err == nil {
		oldPID := strings.TrimSpace(string(data))
		if oldPID != "" {
			// Check if it's an odek telegram process.
			procPath := filepath.Join("/proc", oldPID, "cmdline")
			if cmdline, err := os.ReadFile(procPath); err == nil {
				if strings.Contains(string(cmdline), "odek") &&
					strings.Contains(string(cmdline), "telegram") {
					pid, _ := strconv.Atoi(oldPID)
					if pid > 1 {
						fmt.Fprintf(os.Stderr, "odek telegram: killing stale instance (PID %d)\n", pid)
						syscall.Kill(pid, syscall.SIGTERM)
						// Wait up to 5s for graceful shutdown.
						for i := 0; i < 50; i++ {
							time.Sleep(100 * time.Millisecond)
							if err := syscall.Kill(pid, 0); err != nil {
								break // process gone
							}
						}
						// Force kill if still alive.
						syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}
		}
	}

	// Write our PID.
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0644); err != nil {
		return nil, fmt.Errorf("write pid: %w", err)
	}

	return &instanceLock{pidFile: pidFile}, nil
}

// release removes the PID file on clean shutdown.
func (l *instanceLock) release() {
	os.Remove(l.pidFile)
}

// ── send_message helpers ──────────────────────────────────────────────

// mediaTypeFromExt returns the Telegram media type for a file extension.
func mediaTypeFromExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return "photo"
	case ".ogg", ".mp3", ".wav", ".opus":
		return "voice"
	default:
		return "document"
	}
}

// sendTelegramMedia sends a file as a Telegram media message with caption
// and optional inline keyboard. Detects the media type from file extension.
func sendTelegramMedia(bot *telegram.Bot, chatID int64, mediaType, path, caption string, buttons [][]map[string]string) error {
	var replyMarkup *telegram.InlineKeyboardMarkup
	if len(buttons) > 0 {
		replyMarkup = buttonsToMarkup(buttons)
	}
	opts := &telegram.SendOpts{
		ParseMode:   telegram.ParseModeMarkdownV2,
		ReplyMarkup: replyMarkup,
	}
	switch mediaType {
	case "photo":
		_, err := bot.SendPhoto(chatID, path, caption, opts)
		return err
	case "voice":
		_, err := bot.SendVoice(chatID, path, caption, opts)
		return err
	default:
		_, err := bot.SendDocument(chatID, path, caption, opts)
		return err
	}
}

// buttonsToMarkup converts the tool's button format to Telegram's
// InlineKeyboardMarkup type.
func buttonsToMarkup(buttons [][]map[string]string) *telegram.InlineKeyboardMarkup {
	markup := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: make([][]telegram.InlineKeyboardButton, len(buttons)),
	}
	for i, row := range buttons {
		markup.InlineKeyboard[i] = make([]telegram.InlineKeyboardButton, len(row))
		for j, btn := range row {
			markup.InlineKeyboard[i][j] = telegram.InlineKeyboardButton{
				Text:         btn["text"],
				CallbackData: btn["callback_data"],
			}
		}
	}
	return markup
}

// deleteToolTraceMessages deletes all individual tool trace messages for a chat.
// Used to clean up after the agent finishes (success or error).
func deleteToolTraceMessages(bot *telegram.Bot, chatID int64, msgIDs *sync.Map) {
	msgIDs.Range(func(key, value any) bool {
		if msgID, ok := value.(int); ok {
			if err := bot.DeleteMessage(chatID, msgID); err != nil {
				fmt.Fprintf(os.Stderr, "odek telegram: delete tool message failed: %v\n", err)
			}
		}
		msgIDs.Delete(key)
		return true
	})
}
