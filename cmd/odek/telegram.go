package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
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
	"github.com/BackendStack21/kode/internal/session"
	"github.com/BackendStack21/kode/internal/telegram"
)

// chatMu serializes agent processing per chat to prevent same-chat message
// racing. Each chat gets its own mutex; messages from the same chat are
// processed sequentially, preserving session history integrity.
var chatMu sync.Map // map[int64]*sync.Mutex

// getChatMutex returns the per-chat mutex for the given chat ID.
func getChatMutex(chatID int64) *sync.Mutex {
	v, _ := chatMu.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// telegramCmd is the entry point for "odek telegram".
func telegramCmd(args []string) error {
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
	bot.SetDailyTokenBudget(cfg.DailyTokenBudget)

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

	// 10. Wire handler callbacks.
	//
	// Important: OnTextMessage processes in a background goroutine so it doesn't
	// block the main update processing loop. The TelegramApprover blocks waiting
	// for inline keyboard callbacks, which arrive via the main loop — only async
	// dispatch prevents deadlock.
	handler.OnTextMessage = func(chatID int64, text string) (string, error) {
		go handleChatMessage(chatID, text, bot, handler, sessionManager,
			resolved, systemMessage, handlerLog)
		return "", nil
	}

	// restartRequested is set atomically when a /restart command is received.
	// Checked after the update loop exits to decide between restart and exit.
	var restartRequested atomic.Bool

	handler.OnCommand = func(chatID int64, cmdName string, argsStr string) (string, error) {
		cmd := telegram.FindCommand(cmdName)
		if cmd == nil {
			return fmt.Sprintf("Unknown command: /%s", cmdName), nil
		}

		// Handle /restart — send confirmation directly, then trigger SIGHUP.
		if cmdName == "restart" {
			// Send the restart message directly via the bot to ensure it's
			// delivered before the process re-execs.
			if _, err := bot.SendMessage(chatID,
				"🔄 *Restarting...*\n\nThe bot will restart momentarily. This may take a few seconds.",
				nil); err != nil {
				handlerLog.Error("send restart message failed", "chat_id", chatID, "error", err)
			}
			// Signal SIGHUP to self — the signal handler will cancel the
			// context, stopping the poller, and the main loop will re-exec.
			restartRequested.Store(true)
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

		return cmd.Handler(argsStr)
	}

	handler.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		return "", nil // approval callbacks are routed by the approver
	}

	handler.OnVoiceMessage = func(chatID int64, fileID string) (string, error) {
		go handleChatMessage(chatID, "[voice message: "+fileID+"]",
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnPhotoMessage = func(chatID int64, fileIDs []string) (string, error) {
		go handleChatMessage(chatID, "[photo message: "+strings.Join(fileIDs, ",")+"]",
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

	// 15. Handle SIGINT/SIGTERM/SIGHUP for graceful shutdown and restart.
	//     SIGHUP triggers a full process restart (used by /restart command).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-sigCh
		if sig == syscall.SIGHUP {
			fmt.Fprintf(os.Stderr, "\nodek telegram: restart requested...\n")
		} else {
			fmt.Fprintf(os.Stderr, "\nodek telegram: shutting down...\n")
		}
		cancel()
	}()

	// 16. Start polling in a background goroutine.
	updates := make(chan telegram.Update, 100)
	go poller.Start(ctx, updates)

	// 17. Process updates until the channel is closed (ctx cancelled).
	for upd := range updates {
		handler.HandleUpdate(upd)
	}

	// 18. If restart was requested (via /restart command), re-exec the binary.
	//     This preserves the exact same arguments so the bot comes back with
	//     the same configuration. If syscall.Exec fails, fall through to exit.
	if restartRequested.Load() {
		fmt.Fprintf(os.Stderr, "odek telegram: re-executing %s %v...\n", os.Args[0], os.Args[1:])
		if err := syscall.Exec(os.Args[0], os.Args, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: restart failed: %v\n", err)
			return err
		}
	}

	return nil
}

// handleChatMessage processes a user message from Telegram in a background
// goroutine. It creates or loads the chat session, creates a TelegramApprover
// for approval prompts, runs the agent loop with RunWithMessages, and sends
// back the response. Each chat gets its own TelegramApprover instance.
func handleChatMessage(
	chatID int64,
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
		reportError(bot, chatID, "Failed to create session: "+err.Error())
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

	// ── Tool Tracing ───────────────────────────────────────────────
	// Single editable message showing live tool execution progress.
	var traceMsgID int
	var traceMu sync.Mutex
	traceLines := make([]string, 0, 8)

	// Send initial thinking message.
	if initMsg, err := bot.SendMessage(chatID, "🤔 Thinking...", nil); err == nil {
		traceMsgID = initMsg.ID
	}

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

	agentCfg := odek.Config{
		Model:         resolved.Model,
		BaseURL:       resolved.BaseURL,
		APIKey:        resolved.APIKey,
		MaxIterations: resolved.MaxIter,
		SystemMessage: systemMessage,
		NoProjectFile: resolved.NoAgents,
		Thinking:      resolved.Thinking,
		Tools:         tools,
		Renderer:      rend,
		ToolEventHandler: func(event string, name string, data string) {
			traceMu.Lock()
			defer traceMu.Unlock()
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
				// Replace the last line's ⏳ with a completion marker
				// and the result size instead of the actual content.
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

			if info.HasFinalAnswer {
				runInfo = info
			}
		},
	}

	agent, err := odek.New(agentCfg)
	if err != nil {
		reportError(bot, chatID, "Failed to create agent: "+err.Error())
		return
	}
	defer agent.Close()

	// Run the agent with the full message history (multi-turn).
	response, updatedMessages, err := agent.RunWithMessages(context.Background(), cs.Messages)
	if err != nil {
		reportError(bot, chatID, "Agent error: "+err.Error())
		return
	}

	// Check daily token budget.
	totalTokens := int64(agent.TotalInputTokens() + agent.TotalOutputTokens())
	if err := bot.CheckDailyBudget(totalTokens); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: %v\n", err)
		reportError(bot, chatID, "Daily token budget exceeded. Usage for today has been tracked and will be enforced going forward.")
		// Still save the session so the conversation isn't lost.
		cs.Messages = updatedMessages
		cs.TurnCount++
		if err := sessionManager.Save(chatID, cs.Messages); err != nil {
			fmt.Fprintf(os.Stderr, "odek telegram: session save: %v\n", err)
		}
		return
	}

	// Save the updated session messages.
	cs.Messages = updatedMessages
	cs.TurnCount++
	if err := sessionManager.Save(chatID, cs.Messages); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: session save: %v\n", err)
	}

	// Send the response, then append compact stats as a separate message.
	if response != "" {
		handler.SendResponse(chatID, response)

		// Send run stats as a separate message directly via Bot.SendMessage
		// (bypassing SendResponse/FormatResponse) so MarkdownV2 backtick code
		// formatting is handled natively by Telegram's parser.
		if runInfo.Turn > 0 {
			allToolsMu.Lock()
			toolList := sortedToolKeys(allTools)
			allToolsMu.Unlock()

			statsLine := formatTelegramStats(runInfo, toolList)
			if _, err := bot.SendMessage(chatID, statsLine, &telegram.SendOpts{
				ParseMode: telegram.ParseModeMarkdownV2,
			}); err != nil {
				// Fallback: send as plain text so the info isn't lost
				if _, err2 := bot.SendMessage(chatID, statsLine, nil); err2 != nil {
					fmt.Fprintf(os.Stderr, "odek telegram: stats send fallback failed: %v (orig: %v)\n", err2, err)
				}
			}
		} else {
			fmt.Fprintf(os.Stderr, "odek telegram: stats skipped (runInfo.Turn=%d)\n", runInfo.Turn)
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

// reportError sends an error message to the given chat and logs to stderr.
func reportError(bot *telegram.Bot, chatID int64, msg string) {
	fmt.Fprintf(os.Stderr, "odek telegram: %s\n", msg)
	if _, err := bot.SendMessage(chatID, "❌ "+msg, nil); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: send error message: %v\n", err)
	}
}
