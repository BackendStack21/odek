package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/loop"
	"github.com/BackendStack21/kode/internal/render"
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

	// 9. Resolve system message.
	systemMessage := resolved.System
	if systemMessage == "" {
		systemMessage = defaultSystem
	}
	if resolved.GithubRepoDirectory != "" {
		systemMessage += fmt.Sprintf("\n\nRepository directory: %s\nThis is the local clone of the project repository.", resolved.GithubRepoDirectory)
	}
	if resolved.GithubRepoUrl != "" {
		systemMessage += fmt.Sprintf("\nRepository URL: %s\nThis is the upstream GitHub repository.", resolved.GithubRepoUrl)
	}

	// Quick Facts: must-remember odek metadata injected at the end of the
	// system prompt so the model sees them right before the user message.
	systemMessage += "\n\nQuick Facts (use these, do NOT search):\n"
	systemMessage += "- odek website: https://kode.21no.de\n"
	systemMessage += "- Built by: 21no.de (https://21no.de)\n"
	if resolved.GithubRepoUrl != "" {
		systemMessage += fmt.Sprintf("- Source code: %s\n", resolved.GithubRepoUrl)
	}
	systemMessage += "- Binary name: odek (repo is called kode on GitHub)\n"
	systemMessage += "- Language: Go, minimal dependencies, ~11 MB binary\n"
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

		// Handle /restart — send confirmation, then signal SIGHUP.
		// The signal handler spawns a child and exits.
		if cmdName == "restart" {
			if _, err := bot.SendMessage(chatID,
				"🔄 *Restarting...*\n\nThe bot will restart momentarily. This may take a few seconds.",
				nil); err != nil {
				handlerLog.Error("send restart message failed", "chat_id", chatID, "error", err)
			}
			syscall.Kill(os.Getpid(), syscall.SIGHUP)
			return "", nil
		}

		// Handle /new — clear session and reset trust in the approver.
		if cmdName == "new" {
			sessionManager.Delete(chatID)
			if a := handler.GetApprover(chatID); a != nil {
				a.ResetTrust()
			}
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
			return "✅ Got it, thanks!", nil
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
		go handleChatMessage(chatID, messageID, "[voice message: "+fileID+"]",
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnPhotoMessage = func(chatID int64, messageID int, fileIDs []string) (string, error) {
		go handleChatMessage(chatID, messageID, "[photo message: "+strings.Join(fileIDs, ",")+"]",
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

	// 15. Handle SIGINT/SIGTERM/SIGHUP.
	//     SIGHUP spawns a child process then exits (used by /restart and
	//     agent-triggered restarts). The child's acquireLock kills this
	//     process if it's still alive. SIGINT/SIGTERM do a graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-sigCh
		if sig == syscall.SIGHUP {
			fmt.Fprintf(os.Stderr, "\nodek telegram: restart requested — spawning child...\n")
			writeRestartMarker()
			if err := spawnChild(); err != nil {
				fmt.Fprintf(os.Stderr, "odek telegram: spawn failed: %v\n", err)
			}
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "\nodek telegram: shutting down...\n")
		cancel()
	}()

	// 15b. Check for restart marker from previous instance and notify.
	if chatID, ok := readRestartMarker(); ok && chatID != 0 {
		bot.SendMessage(chatID, "🔄 *Bot restarted*\n\nA new instance is now running. Use `/new` if you experience any context issues.", &telegram.SendOpts{ParseMode: "Markdown"})
	}

	// 16. Start polling in a background goroutine.
	updates := make(chan telegram.Update, 100)
	go poller.Start(ctx, updates)

	// 17. Process updates until the channel is closed (ctx cancelled).
	for upd := range updates {
		handler.HandleUpdate(upd)
	}

	// 18. Clean exit.
	return nil
}

// ── Restart (spawn + exit) ─────────────────────────────────────────────
//
// Instead of syscall.Exec (binary race, stale HTTP/2 connections, session
// loops), we spawn a child process and exit. The child's acquireLock kills
// the old process if it's still alive.

// restartMarkerPath returns the path to the restart marker file.
func restartMarkerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".odek", "restart.json"), nil
}

// writeRestartMarker writes a marker so the next instance knows a restart
// just happened. Currently writes an empty marker (global restart).
func writeRestartMarker() error {
	path, err := restartMarkerPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte("{}\n"), 0644)
}

// readRestartMarker reads and removes the restart marker. Returns true
// if a marker existed, along with the chat_id (0 if none specified).
func readRestartMarker() (int64, bool) {
	path, err := restartMarkerPath()
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	os.Remove(path)
	// Future: parse chat_id from JSON. For now, just signal restart happened.
	_ = data
	return 0, true
}

// spawnChild starts a new odek telegram process detached from the parent.
func spawnChild() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("executable: %w", err)
	}
	// Copy args (same as current process).
	argv := make([]string, len(os.Args))
	copy(argv, os.Args)
	argv[0] = exe

	attr := &os.ProcAttr{
		Env: os.Environ(),
		// Detach: nil files so child gets its own stdin/stdout/stderr.
		// The child process will be reparented to init when we exit.
		Files: []*os.File{nil, nil, nil},
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
	resolved	config.ResolvedConfig,
	systemMessage string,
	log telegram.Logger,
) {
	// Serialize per chat: only one agent loop runs per chat at a time.
	// Prevents same-chat message racing that would corrupt session history.
	mu := getChatMutex(chatID)
	mu.Lock()
	defer mu.Unlock()

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

	// ── Tool Tracing ───────────────────────────────────────────────
	// Single editable message showing live tool execution progress.
	// The message is created lazily — only when the first tool call
	// fires, not before. This avoids the premature "🤔 Thinking…" spam.
	var traceMsgID int
	var traceMu sync.Mutex
	traceLines := make([]string, 0, 8)

	// truncate shortens a string for display, appending "…" if trimmed.
	truncate := func(s string, max int) string {
		if len(s) > max {
			return s[:max] + "…"
		}
		return s
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

	// Resolve skills config (same logic as main.go run command).
	var skillsCfg *skills.SkillsConfig
	if resolved.Skills.Learn {
		skillsCfg = &resolved.Skills
	}

	agentCfg := odek.Config{
		Model:         resolved.Model,
		BaseURL:       resolved.BaseURL,
		APIKey:        resolved.APIKey,
		MaxIterations: resolved.MaxIter,
		SystemMessage: systemMessage,
		NoProjectFile: resolved.NoAgents,
		Skills:        skillsCfg,
		Thinking:      resolved.Thinking,
		Tools:         agentTools,
		Renderer:      rend,
		ToolEventHandler: func(event string, name string, data string) {
			traceMu.Lock()
			defer traceMu.Unlock()

			// Lazy-init: create the trace message on the first tool call.
			if traceMsgID == 0 && event == "tool_call" {
				if msg, err := bot.SendMessage(chatID, "🔧 …", nil); err == nil {
					traceMsgID = msg.ID
				} else {
					return
				}
			}
			if traceMsgID == 0 {
				return
			}

			switch event {
			case "tool_call":
				args := truncate(data, 150)
				line := fmt.Sprintf("%s %s(%s)  ⏳", render.ToolEmoji(name), name, args)
				traceLines = append(traceLines, line)
				bot.EditMessageText(chatID, traceMsgID, strings.Join(traceLines, "\n"), nil)

			case "tool_result":
				sizeLabel := fmt.Sprintf("%dB", len(data))
				if len(data) > 1024 {
					sizeLabel = fmt.Sprintf("%dKB", len(data)/1024)
				}
				if len(traceLines) > 0 {
					last := traceLines[len(traceLines)-1]
					traceLines[len(traceLines)-1] = strings.Replace(last, " ⏳", " ✅ ("+sizeLabel+")", 1)
					bot.EditMessageText(chatID, traceMsgID, strings.Join(traceLines, "\n"), nil)
				}
			}
		},
		IterationCallback: func(info loop.IterationInfo) {
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
			names := strings.Join(event.Skills, ", ")
			sendAsync(bot, chatID, "📚 Loaded skill: "+names, &telegram.SendOpts{ReplyToMessageID: messageID})
		case "autoloaded":
			names := strings.Join(event.Skills, ", ")
			sendAsync(bot, chatID, "📚 Auto-loaded skills: "+names, &telegram.SendOpts{ReplyToMessageID: messageID})
		case "saved":
			sendAsync(bot, chatID, fmt.Sprintf("✓ Saved skill %q", event.SkillName), &telegram.SendOpts{ReplyToMessageID: messageID})
		case "deleted":
			sendAsync(bot, chatID, fmt.Sprintf("✗ Deleted skill %q", event.SkillName), &telegram.SendOpts{ReplyToMessageID: messageID})
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
			if msg != "" {
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
// prevent silent delivery failures.
func sendAsync(bot *telegram.Bot, chatID int64, text string, opts *telegram.SendOpts) {
	go func() {
		if _, err := bot.SendMessage(chatID, text, opts); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: async send failed: %v\n", err)
		}
	}()
}

// ── Singleton Lock ─────────────────────────────────────────────────────
//
// Prevents two bot instances from polling Telegram simultaneously (which
// causes 409 Conflict errors). Uses a PID file at ~/.odek/telegram.pid.
// On startup, if a stale PID file exists, the old process is killed before
// the new one starts.

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
