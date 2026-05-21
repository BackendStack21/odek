package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/llm"
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
	cfg := telegram.ConfigFromEnv()
	if err := telegram.ValidateConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: %v\n", err)
		return err
	}

	// 4. Create bot client.
	bot := telegram.NewBot(cfg.Token)

	// 5. Create session store on disk (~/.odek/sessions/).
	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: session store: %v\n", err)
		return err
	}

	// 6. Create session manager (per-chat Telegram session cache).
	sessionManager := telegram.NewSessionManager(store)

	// 7. Create handler.
	handler := telegram.NewHandler(bot)

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
			resolved, systemMessage)
		return "", nil
	}

	handler.OnCommand = func(chatID int64, cmdName string, argsStr string) (string, error) {
		cmd := telegram.FindCommand(cmdName)
		if cmd == nil {
			return fmt.Sprintf("Unknown command: /%s", cmdName), nil
		}

		// Handle /new — clear session and reset trust in the approver.
		if cmdName == "new" {
			sessionManager.Delete(chatID)
			if a := handler.GetApprover(chatID); a != nil {
				a.ResetTrust()
			}
		}

		return cmd.Handler(argsStr)
	}

	handler.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		return "", nil // approval callbacks are routed by the approver
	}

	handler.OnVoiceMessage = func(chatID int64, fileID string) (string, error) {
		go handleChatMessage(chatID, "[voice message: "+fileID+"]",
			bot, handler, sessionManager, resolved, systemMessage)
		return "", nil
	}

	handler.OnPhotoMessage = func(chatID int64, fileIDs []string) (string, error) {
		go handleChatMessage(chatID, "[photo message: "+strings.Join(fileIDs, ",")+"]",
			bot, handler, sessionManager, resolved, systemMessage)
		return "", nil
	}

	handler.OnError = func(chatID int64, err error) {
		fmt.Fprintf(os.Stderr, "odek telegram: error for chat %d: %v\n", chatID, err)
	}

	// 11. Set command list via Telegram API.
	if err := bot.SetMyCommands(telegram.CommandDescriptors()); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: warning: set commands failed: %v\n", err)
	}

	// 12. Print startup banner.
	fmt.Fprintf(os.Stderr, "odek telegram bot started\n")

	// 13. Create poller.
	poller := telegram.NewPoller(bot)
	poller.Interval = time.Duration(cfg.PollInterval) * time.Second
	poller.Timeout = cfg.PollTimeout

	// 14. Create cancellable context for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 15. Handle SIGINT/SIGTERM for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nodek telegram: shutting down...\n")
		cancel()
	}()

	// 16. Start polling in a background goroutine.
	updates := make(chan telegram.Update, 100)
	go poller.Start(ctx, updates)

	// 17. Process updates until the channel is closed (ctx cancelled).
	for upd := range updates {
		handler.HandleUpdate(upd)
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

	agent, err := odek.New(odek.Config{
		Model:         resolved.Model,
		BaseURL:       resolved.BaseURL,
		APIKey:        resolved.APIKey,
		MaxIterations: resolved.MaxIter,
		SystemMessage: systemMessage,
		NoProjectFile: resolved.NoAgents,
		Thinking:      resolved.Thinking,
		Tools:         tools,
		Renderer:      rend,
	})
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

	// Save the updated session messages.
	cs.Messages = updatedMessages
	cs.TurnCount++
	if err := sessionManager.Save(chatID, cs.Messages); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: session save: %v\n", err)
	}

	// Send the response.
	if response != "" {
		handler.SendResponse(chatID, response)
	}
}

// reportError sends an error message to the given chat and logs to stderr.
func reportError(bot *telegram.Bot, chatID int64, msg string) {
	fmt.Fprintf(os.Stderr, "odek telegram: %s\n", msg)
	if _, err := bot.SendMessage(chatID, "❌ "+msg, nil); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: send error message: %v\n", err)
	}
}
