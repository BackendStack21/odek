package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/render"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/skills"
	"github.com/BackendStack21/odek/internal/telegram"
	toolpkg "github.com/BackendStack21/odek/internal/tool"
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

// clarifyMsgIDs stores the message ID of the active clarify prompt
// per chat, so the callback handler can edit/remove it after the user
// responds. Cleared when the clarify tool completes or times out.
var clarifyMsgIDs sync.Map // map[int64]int

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

// restartCooldown limits how often /restart can be triggered, preventing a
// compromised or over-eager account from restart-looping the bot.
const restartCooldown = 60 * time.Second

// lastRestartAt stores the Unix timestamp of the most recent /restart.
var lastRestartAt atomic.Int64

// restartCooldownRemaining returns the time left before another /restart is
// allowed. Zero means the cooldown has elapsed.
func restartCooldownRemaining() time.Duration {
	last := time.Unix(lastRestartAt.Load(), 0)
	if last.IsZero() {
		return 0
	}
	elapsed := time.Since(last)
	if elapsed >= restartCooldown {
		return 0
	}
	return restartCooldown - elapsed
}

// handleRestartCommand checks operator identity and cooldown for /restart and,
// if allowed, starts the asynchronous SIGHUP. It returns the reply text and a
// bool indicating whether a restart was actually triggered.
func handleRestartCommand(chatID, userID int64, adminChats, adminUsers []int64) (string, bool) {
	if !canManageSchedule(chatID, userID, adminChats, adminUsers) {
		return "🔒 /restart is restricted to configured operator chats/users.", false
	}
	if rem := restartCooldownRemaining(); rem > 0 {
		return fmt.Sprintf("⏳ /restart was used recently. Please wait %s before restarting again.", rem.Round(time.Second)), false
	}
	lastRestartAt.Store(time.Now().Unix())
	go func() {
		time.Sleep(500 * time.Millisecond)
		killFn(os.Getpid(), syscall.SIGHUP)
	}()
	return "🔄 *Restarting...*\n\nThe bot will restart momentarily. This may take a few seconds.", true
}

// instanceLockRef holds the current Telegram singleton lock release function,
// accessible from gracefulRestart so it can release the lock before os.Exit(0).
var instanceLockRef func()

// getChatMutex returns the per-chat mutex for the given chat ID.
func getChatMutex(chatID int64) *sync.Mutex {
	v, _ := chatMu.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// resetChatForNew implements the /new command's session reset: it archives the
// current session and clears the approver's trust state for a fresh start.
//
// It deliberately does NOT remove the per-chat mutex from chatMu. Deleting the
// mutex while an in-flight handleChatMessage goroutine still holds it lets the
// next message LoadOrStore a *fresh* mutex and TryLock it successfully, so two
// agent runs execute concurrently for the same chat — corrupting interleaved
// sessionManager.Save writes and clobbering each other's approver. The per-chat
// mutex is naturally bounded (one entry per chat ID ever seen), so retaining it
// is not a meaningful leak.
func resetChatForNew(chatID int64, sessionManager *telegram.SessionManager, handler *telegram.Handler, log telegram.Logger) {
	if err := sessionManager.ArchiveAndDelete(chatID); err != nil {
		log.Warn("archive session", "chat_id", chatID, "error", err)
	}
	if a := handler.GetApprover(chatID); a != nil {
		a.ResetTrust()
	}
}

// telegramCmd is the entry point for "odek telegram".
func telegramCmd(args []string) error {
	// 0. Acquire singleton lock — wait for any previous instance to exit.
	lockRelease, err := acquireLock()
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	instanceLockRef = lockRelease
	defer func() {
		instanceLockRef = nil
		lockRelease()
	}()

	// 1. Load config from all sources (file → env).
	resolved := config.LoadConfig(config.CLIFlags{})

	// 1b. If the parent handed us an API key via an inherited file descriptor
	// (FD-based handoff used on Telegram restart), use it. This keeps the key
	// out of the child's /proc/<pid>/environ.
	if key := readKeyFromInheritedFD(); key != "" {
		resolved.APIKey = key
	}

	// 2. Validate API key presence.
	if resolved.APIKey == "" {
		return fmt.Errorf("no API key configured — set ODEK_API_KEY, DEEPSEEK_API_KEY, or configure in odek.json")
	}
	// Store for spawnChild: config loader clears ODEK_API_KEY from env,
	// so the child process needs us to hand it off securely.
	resolvedAPIKey = resolved.APIKey

	// 3. Load and validate Telegram config.
	cfg := resolved.Telegram
	if err := telegram.ValidateConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: %v\n", err)
		return err
	}

	// 4. Create bot client.
	bot := telegram.NewBot(cfg.Token)
	bot.MaxDownloadSize = cfg.MaxDownloadSize
	bot.MediaQuotaPerChat = cfg.MediaQuotaPerChat

	// 4b. Create logger.
	level := telegram.ParseLogLevel(cfg.LogLevel)
	rootLog := telegram.NewFileLogger(level, cfg.LogFile)
	botLog := rootLog.With("component", "bot")
	handlerLog := rootLog.With("component", "handler")
	pollerLog := rootLog.With("component", "poller")

	bot.SetLogger(botLog)

	// 4c. Configure fallback Telegram API endpoints if provided.
	if len(cfg.FallbackURLs) > 0 {
		if err := bot.SetFallbackURLs(cfg.FallbackURLs); err != nil {
			return fmt.Errorf("telegram: invalid fallback URL: %w", err)
		}
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

	// Initialize semantic search index using the shared embedding backend (or a
	// sessions.embedding override) so a single endpoint config powers it.
	if err := store.InitVectorIndex(resolved.SessionEmbedding); err != nil {
		fmt.Fprintf(os.Stderr, "odek telegram: vector index: %v\n", err)
		// Non-fatal — search falls back to metadata-only.
	}

	// 6. Create session manager (per-chat Telegram session cache)
	//    with the configured session TTL (default 24h).
	sessionManager := telegram.NewSessionManager(store, time.Duration(cfg.SessionTTL)*time.Hour)

	// 7. Create handler.
	handler := telegram.NewHandler(bot)
	handler.SetLogger(handlerLog)

	// 8. Set handler config from cfg.
	handler.Config = telegram.HandlerConfig{
		AllowedChats:  cfg.AllowedChats,
		BotUsername:   cfg.BotUsername,
		MaxMsgLength:  cfg.MaxMsgLength,
		AllowedUsers:  cfg.AllowedUsers,
		AllowAllUsers: cfg.AllowAllUsers,
	}

	// Loud warning when running without an allowlist (only reachable when the
	// operator explicitly set ODEK_TELEGRAM_ALLOW_ALL; ValidateConfig blocks
	// the accidental case).
	if cfg.AllowAllUsers && !cfg.HasAllowlist() {
		handlerLog.Warn("telegram bot is running with NO allowlist — ANY user can drive the agent (ODEK_TELEGRAM_ALLOW_ALL=true)")
	}

	// 9. Build system prompt: explicit override > IDENTITY.md > compiled default
	systemMessage := buildSystemPrompt(resolved)

	// Telegram-specific Quick Facts and recovery guidance
	systemMessage += "\n\nQuick Facts (use these, do NOT search):\n"
	systemMessage += "- odek website: https://odek.21no.de\n"
	systemMessage += "- Built by: 21no.de (https://21no.de)\n"
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
	systemMessage += "  don't escalate to a broader search. Narrow, don't widen.\n"
	systemMessage += "\n"
	systemMessage += "search_files performance:\n"
	systemMessage += "- ALWAYS use file_glob (e.g. '*.go', '*.md') to restrict the file types scanned.\n"
	systemMessage += "- ALWAYS use a narrow path — never '/' or '/root' without file_glob.\n"
	systemMessage += "- Without file_glob, every readable file in the subtree is opened and scanned.\n"
	systemMessage += "- For multi-pattern searches, use multi_grep (parallel walk, less overhead).\n"
	systemMessage += "\n"

	// Telegram-specific system prompt additions
	//
	// Important: OnTextMessage processes in a background goroutine so it doesn't
	// block the main update processing loop. The TelegramApprover blocks waiting
	// for inline keyboard callbacks, which arrive via the main loop — only async
	// dispatch prevents deadlock.
	handler.OnTextMessage = func(chatID int64, messageID int, text string, forwarded bool, userID int64) (string, error) {
		go handleChatMessage(chatID, messageID, userID, telegramTextMessage(chatID, text, forwarded),
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	// Shared schedule store for the in-chat /schedule commands. Created once and
	// also handed to the embedded scheduler below so both sides agree. A nil
	// store (rare init failure) degrades to a friendly error from the commands.
	scheduleStore, err := schedule.NewStore()
	if err != nil {
		handlerLog.Warn("schedule: store unavailable; /schedule commands degraded", "error", err)
		scheduleStore = nil
	}

	handler.OnCommand = func(chatID int64, messageID int, cmdName string, argsStr string, userID int64) (string, error) {
		cmd := telegram.FindCommand(cmdName)
		if cmd == nil {
			return fmt.Sprintf("Unknown command: /%s", cmdName), nil
		}

		// Handle /schedules and /schedule — manage scheduled tasks. scheduleReloadRef
		// is set by the embedded scheduler (nil if none runs here); the parsing and
		// formatting live in schedule_telegram.go. A /schedule run dispatches the
		// job's task through the normal chat pipeline.
		if cmdName == "schedules" || cmdName == "schedule" {
			sub := argsStr
			if cmdName == "schedules" {
				sub = "list"
			}
			reply, runTask := telegramScheduleReply(chatID, userID, sub, scheduleStore,
				scheduleReloadRef, resolved.Schedules.AllowTelegramManagement,
				resolved.Schedules.TelegramAdminChats, resolved.Schedules.TelegramAdminUsers)
			if runTask != "" {
				go handleChatMessage(chatID, messageID, userID, runTask, bot, handler, sessionManager,
					resolved, systemMessage, handlerLog)
			}
			return reply, nil
		}

		// Handle /restart — restricted to operator chats/users and rate-limited.
		// The confirmation message is sent through the standard response pipeline
		// (MarkdownV2 + retry logic). SIGHUP fires asynchronously after a short
		// delay so the pipeline has time to dispatch before graceful restart begins.
		if cmdName == "restart" {
			reply, _ := handleRestartCommand(chatID, userID,
				resolved.Schedules.TelegramAdminChats, resolved.Schedules.TelegramAdminUsers)
			return reply, nil
		}

		// Handle /new — archive the current session and start fresh.
		// The current session is preserved as a timestamped archive file
		// so it can be revisited via `odek session list`.
		if cmdName == "new" {
			resetChatForNew(chatID, sessionManager, handler, handlerLog)
			var b strings.Builder
			b.WriteString("🔄 *Session archived, starting fresh*\n\n")
			fmt.Fprintf(&b, "• Model: `%s`\n", resolved.Model)
			if resolved.Sandbox {
				b.WriteString("• Sandbox: enabled\n")
			}
			if resolved.Skills.Verbose {
				b.WriteString("• Skills verbose: on\n")
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
			fmt.Fprintf(&b, "🧹 *Pruned* — Removed items older than %d days:\n\n", days)
			if sessionsRemoved > 0 {
				fmt.Fprintf(&b, "• %d session(s)\n", sessionsRemoved)
			}
			if plansRemoved > 0 {
				fmt.Fprintf(&b, "• %d plan(s)\n", plansRemoved)
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
			go handleChatMessage(chatID, messageID, userID, prompt, bot, handler, sessionManager,
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
			cancelled := false
			if cancelVal, ok := chatCancels.LoadAndDelete(chatID); ok {
				cancel := cancelVal.(context.CancelFunc)
				cancel()
				cancelled = true
			}
			// Retrieve the latest run info for a summary of what was interrupted.
			// Use Load, not LoadAndDelete — handleChatMessage's cancellation handler
			// and defer own the cleanup. Racing with LoadAndDelete here would strip
			// the info before the cancellation handler can format its own message.
			var summary string
			if infoVal, ok := chatRunInfos.Load(chatID); ok {
				info := infoVal.(loop.IterationInfo)
				summary = formatStopSummary(info)
			} else if cancelled {
				// Cancel was sent but no run info yet (first iteration callback
				// hadn't fired). Report cancellation without detailed summary.
				summary = "⏹️ *Task cancelled.*"
			} else {
				summary = "⏹️ No active task to stop."
			}
			return summary, nil
		}

		return cmd.Handler(argsStr)
	}

	handler.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		// Route clarify callbacks — the user clicked Yes/No on a clarify question.
		if answer, ok := strings.CutPrefix(data, "clarify:"); ok {
			if ch, ok := sessionManager.GetClarifyChannel(chatID); ok {
				select {
				case ch <- answer:
				default:
					// Channel full or closed — clarify already resolved.
				}
			}
			// Remove the inline keyboard and update text to show the answer.
			if msgID, ok := clarifyMsgIDs.Load(chatID); ok {
				bot.EditMessageText(chatID, msgID.(int),
					"✅ *User answered:* "+answer,
					&telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2, ReplyMarkup: &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{}}})
				clarifyMsgIDs.Delete(chatID)
			}
			return "", nil
		}

		// Route skill suggestion callbacks — Save or Skip.
		if skillName, ok := strings.CutPrefix(data, "skill_save:"); ok {
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
		if skillName, ok := strings.CutPrefix(data, "skill_skip:"); ok {
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

	handler.OnVoiceMessage = func(chatID int64, messageID int, fileID string, userID int64) (string, error) {
		// Download the voice file.
		localPath, err := telegram.DownloadVoice(bot, chatID, fileID)
		if err != nil {
			handlerLog.Warn("voice download failed", "chat_id", chatID, "error", err)
			go handleChatMessage(chatID, messageID, userID,
				fmt.Sprintf("[voice message received — download failed: %v]", err),
				bot, handler, sessionManager, resolved, systemMessage, handlerLog)
			return "", nil
		}

		// Auto-transcribe if configured and whisper is available.
		if resolved.Transcription.AutoTranscribe {
			tool := newTranscribeTool(resolved.Dangerous, resolved.Transcription)
			result, err := tool.Call(fmt.Sprintf(`{"path":"%s"}`, localPath))
			if err == nil {
				var r struct {
					Text  string `json:"text"`
					Error string `json:"error"`
				}
				if json.Unmarshal([]byte(result), &r) == nil && r.Error == "" && r.Text != "" {
					// Transcribed text crosses an external trust boundary; wrap it before
					// injecting it into the user message stream (telegramVoiceMessage).
					go handleChatMessage(chatID, messageID, userID,
						telegramVoiceMessage(chatID, r.Text),
						bot, handler, sessionManager, resolved, systemMessage, handlerLog)
					return "", nil
				}
			}
			// Transcription failed — fall through to file path message
			handlerLog.Warn("auto-transcribe failed, falling back to path", "chat_id", chatID, "error", err)
		}

		// Fallback: pass the file path to the agent
		go handleChatMessage(chatID, messageID, userID,
			wrapUntrusted(context.Background(), fmt.Sprintf("telegram:chat:%d:voice", chatID),
				fmt.Sprintf("🎤 Voice message saved to %q. Use transcribe() tool to get the text.", localPath)),
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnPhotoMessage = func(chatID int64, messageID int, fileIDs []string, caption string, userID int64) (string, error) {
		localPath, err := telegram.DownloadPhoto(bot, chatID, fileIDs)
		if err != nil {
			handlerLog.Warn("photo download failed", "chat_id", chatID, "error", err)
			go handleChatMessage(chatID, messageID, userID,
				fmt.Sprintf("[photo received — download failed: %v]", err),
				bot, handler, sessionManager, resolved, systemMessage, handlerLog)
			return "", nil
		}

		caption = strings.TrimSpace(caption)

		// Auto-describe if configured and the vision model is available: run the
		// photo through the local vision model FIRST to extract a description,
		// then hand that description (plus the user's caption, if any) to the
		// agent so it can answer the request. Mirrors voice auto-transcription.
		if resolved.Vision.AutoDescribe {
			tool := newVisionTool(resolved.Dangerous, resolved.Vision)
			argsJSON, _ := json.Marshal(map[string]string{
				"path":   localPath,
				"prompt": photoVisionPrompt(caption),
			})

			result, err := tool.Call(string(argsJSON))
			if err == nil {
				var r struct {
					Description string `json:"description"`
					Error       string `json:"error"`
				}
				if json.Unmarshal([]byte(result), &r) == nil && r.Error == "" && r.Description != "" {
					// r.Description is already wrapped in <untrusted_content>
					// boundaries by the vision tool (image text is untrusted).
					go handleChatMessage(chatID, messageID, userID,
						photoVisionMessage(caption, r.Description),
						bot, handler, sessionManager, resolved, systemMessage, handlerLog)
					return "", nil
				}
			}
			// Vision failed — fall through to the path-based message below.
			handlerLog.Warn("auto-describe failed, falling back to path", "chat_id", chatID, "error", err)
		}

		// Fallback: hand the agent the file path (and caption) so it can analyze
		// the image itself via the vision/shell tools.
		go handleChatMessage(chatID, messageID, userID,
			photoFallbackMessage(localPath, caption),
			bot, handler, sessionManager, resolved, systemMessage, handlerLog)
		return "", nil
	}

	handler.OnDocumentMessage = func(chatID int64, messageID int, fileID string, fileName string, userID int64) (string, error) {
		localPath, err := telegram.DownloadDocument(bot, chatID, fileID, fileName)
		if err != nil {
			handlerLog.Warn("document download failed", "chat_id", chatID, "file_name", fileName, "error", err)
			go handleChatMessage(chatID, messageID, userID,
				fmt.Sprintf("[document received — download failed: %v]", err),
				bot, handler, sessionManager, resolved, systemMessage, handlerLog)
			return "", nil
		}
		go handleChatMessage(chatID, messageID, userID,
			telegramDocumentMessage(localPath),
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
				gracefulRestart(bot)
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
		msg := "🔄 *Bot restarted*\n\nA new instance is now running.\nUse `/resume` to continue your last session, or `/new` to start fresh."
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

	// 16b. Start the embedded scheduler. It fires scheduled jobs (see
	// `odek schedule`) and delivers results to Telegram, sharing this
	// process's resolved config — so no environment-inheritance problem and no
	// separate cron daemon. If an external `odek schedule daemon` already holds
	// the lock, this defers to it instead of double-firing.
	stopScheduler := startSchedulerForBot(ctx, bot, resolved, systemMessage, handlerLog, scheduleStore, sessionManager)
	defer stopScheduler()

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
// just happened. chatIDs must be captured in Phase 1 (before the drain),
// since goroutines remove themselves from chatCancels in their exit defers.
func writeRestartMarker(chatIDs []int64) error {
	marker := restartMarker{ChatIDs: chatIDs}
	// Sort for deterministic output.
	slices.Sort(marker.ChatIDs)

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
func gracefulRestart(bot *telegram.Bot) {
	// Guard: only one restart at a time.
	if !restartInProgress.CompareAndSwap(false, true) {
		return
	}

	activeCount := countSyncMap(&chatCancels)
	fmt.Fprintf(os.Stderr, "odek telegram: graceful restart — phase 1: notifying %d active chat(s)...\n", activeCount)

	// ── Phase 1: Notify + Cancel ──────────────────────────────────────
	// Capture active chat IDs NOW — goroutine defers remove them from
	// chatCancels when they exit, so by the time Phase 2 drains, the map
	// is empty. writeRestartMarker needs these IDs to notify users on startup.
	var activeChatIDs []int64
	chatCancels.Range(func(key, value any) bool {
		chatID := key.(int64)
		cancel := value.(context.CancelFunc)

		activeChatIDs = append(activeChatIDs, chatID)

		// Best-effort notification — the API call might fail during shutdown.
		if _, err := bot.SendMessage(chatID,
			"🔄 *Bot restarting* — your current task was interrupted.\n\n"+
				"Your conversation has been saved. The new instance will be ready "+
				"in a few seconds. Use `/resume` to continue or `/new` to start fresh.",
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
	writeRestartMarker(activeChatIDs)
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
	// os.StartProcess, the cleanest path is to exit right here.
	// Release the singleton lock before exit so the child gets a clean slate.
	if instanceLockRef != nil {
		instanceLockRef()
	}
	// Close the embedded scheduler's MCP connections before exiting — os.Exit
	// skips deferred cleanup, so without this the MCP child processes (e.g.
	// Playwright/Chromium) would leak across every restart.
	if mcpCleanupRef != nil {
		mcpCleanupRef()
	}
	// Release the schedule lock too, so the restarted child's embedded
	// scheduler can re-acquire it instead of finding a (briefly) live owner.
	if scheduleUnlockRef != nil {
		scheduleUnlockRef()
	}
	os.Exit(0)
}

// spawnChild starts a new odek telegram process detached from the parent.
// Stderr is redirected to /tmp/odek-telegram.log so startup errors are visible.
func spawnChild() error {
	return spawnChildWithStarter(os.StartProcess)
}

type processStarter func(name string, argv []string, attr *os.ProcAttr) (*os.Process, error)

// spawnChildWithStarter is the testable core of spawnChild. The starter
// argument lets tests intercept the ProcAttr without actually launching a
// process.
func spawnChildWithStarter(starter processStarter) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("executable: %w", err)
	}
	// Copy args (same as current process). The restarted `odek telegram`
	// process starts its own embedded scheduler goroutine, so there is nothing
	// extra to re-exec through — the scheduler's lifecycle follows the bot's.
	// (gracefulRestart releases the schedule lock before os.Exit so the child
	// re-acquires it cleanly.)
	argv := make([]string, len(os.Args))
	copy(argv, os.Args)
	argv[0] = exe

	// Open stderr log file for the child so startup errors aren't lost.
	stderr, err := os.OpenFile("/tmp/odek-telegram.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		stderr = nil // fallback: child gets /dev/null
	}

	// Build child environment: parent env only. The API key is passed via an
	// inherited file descriptor instead of the environment so it does not
	// appear in /proc/<pid>/environ or crash dumps. This mirrors the FD-based
	// handoff used for sub-agents.
	childEnv := os.Environ()
	var files []*os.File
	var cleanup func()
	if resolvedAPIKey != "" {
		keyFile, keyCleanup, err := writeKeyToUnlinkedFile(resolvedAPIKey)
		if err != nil {
			return fmt.Errorf("write key to FD: %w", err)
		}
		cleanup = keyCleanup
		childEnv = append(childEnv, keyFDEnvVar+"=3")
		files = []*os.File{nil, nil, stderr, keyFile}
	} else {
		files = []*os.File{nil, nil, stderr}
	}

	attr := &os.ProcAttr{
		Env: childEnv,
		// Detach: nil stdin/stdout so child is reparented to init.
		// Stderr is captured to the log file for debugging.
		// FD 3 carries the API key when needed.
		Files: files,
	}
	_, err = starter(exe, argv, attr)
	if cleanup != nil {
		cleanup()
	}
	return err
}

// seedSystemMessage guarantees the system prompt is messages[0].
//
// RunWithMessages — unlike Run — does NOT inject the engine's system message,
// so every caller that resumes a message history must seed it (the run/continue
// commands do the same). Without this, a fresh Telegram chat reaches the model
// with no system prompt at all and the agent answers as the provider's base
// identity (e.g. "I am Claude") instead of from IDENTITY.md / the default.
//
// New or system-less histories get the prompt prepended; resumed histories get
// messages[0] refreshed so IDENTITY.md / prompt changes take effect next turn.
func seedSystemMessage(messages []llm.Message, system string) []llm.Message {
	if len(messages) == 0 || messages[0].Role != "system" {
		return append([]llm.Message{{Role: "system", Content: system}}, messages...)
	}
	messages[0].Content = system
	return messages
}

// handleChatMessage processes a user message from Telegram in a background
// goroutine. It creates or loads the chat session, creates a TelegramApprover
// for approval prompts, runs the agent loop with RunWithMessages, and sends
// back the response. Each chat gets its own TelegramApprover instance.
func handleChatMessage(
	chatID int64,
	messageID int,
	userID int64,
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
	// Bind approvals to the originating user so group members cannot hijack
	// each other's approval prompts.
	approver := telegram.NewTelegramApprover(bot, chatID, userID)
	handler.SetApprover(chatID, approver)
	defer handler.DeleteApprover(chatID)

	// Get or create the session for this chat.
	cs, err := sessionManager.GetOrCreate(chatID)
	if err != nil {
		reportError(bot, chatID, messageID, "Failed to create session: "+err.Error())
		return
	}

	// Ensure the system prompt is messages[0] before the agent runs.
	cs.Messages = seedSystemMessage(cs.Messages, systemMessage)

	// Append user message to session.
	cs.Messages = append(cs.Messages, llm.Message{Role: "user", Content: text})
	cs.LastActive = time.Now()

	// Persist the user message immediately so session_search can find it
	// inside the agent loop. Without this, the current turn is only in
	// memory and invisible to both vector search and deepSearch.
	// Build a session.Snapshot directly to avoid incrementing TurnCount
	// (that happens once at the end in the normal Save path).
	sess := &session.Session{
		ID:        cs.SessionID,
		CreatedAt: cs.CreatedAt,
		UpdatedAt: time.Now(),
		Model:     "",
		Turns:     cs.TurnCount,
		Task:      fmt.Sprintf("tg-%d", chatID),
		Messages:  cs.Messages,
	}
	if err := sessionManager.Store.Save(sess); err != nil {
		log.Error("save session before agent run", "chat_id", chatID, "error", err)
	}

	// Build the agent with Telegram approver.
	tools := builtinTools(resolved.Dangerous, nil, approver, resolved.MaxConcurrency, resolved.APIKey, toolConfig{Transcription: resolved.Transcription, Vision: resolved.Vision, WebSearch: resolved.WebSearch}, sessionManager.Store)

	modelLabel := odek.ProfileLabel(resolved.Model)
	if modelLabel == "" {
		modelLabel = "deepseek-v4-flash"
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

	// ── Progress Mode Setup ───────────────────────────────────────
	// The tool_progress config controls Telegram progress bubbles:
	//   "all"     — single editable bubble with smart previews, throttled edits,
	//               dedup counter, and flood-control fallback (recommended default)
	//   "new"     — like "all" but only updates when the tool name changes
	//   "verbose" — raw tool args in per-tool messages with result replacement
	//   "off"     — no per-tool progress (just thinking + final answer)
	//
	// When interaction_mode is "enhance", the old per-tool narrated-message
	// behavior is used regardless of tool_progress (preserves existing UX).
	toolProgress := resolved.ToolProgress
	isEnhance := resolved.InteractionMode == "enhance"
	isOff := resolved.InteractionMode == "off"
	if isOff {
		toolProgress = "off"
	}
	if isEnhance {
		toolProgress = "enhance"
	}

	var progressMsgID int
	var progressLines []string
	var lastProgressMsg string
	var repeatCount int
	var canEdit = true
	var lastEditTime time.Time
	const editThrottle = 1500 * time.Millisecond

	// Send an initial "working on it" message for all modes except "off".
	if toolProgress != "off" {
		msg, err := bot.SendMessage(chatID, "🤔 Looking into that...",
			&telegram.SendOpts{ReplyToMessageID: messageID})
		if err == nil {
			progressMsgID = msg.ID
		}
	}
	// Cleanup progress messages when the task finishes.
	defer func() {
		if progressMsgID != 0 && resolved.ToolProgressCleanup {
			bot.DeleteMessage(chatID, progressMsgID)
		}
	}()

	// ── Tool Tracing (legacy verbose mode fallback) ──────────────
	// Used when interaction_mode is "verbose" AND tool_progress is NOT set
	// (or set to something other than "verbose"). These per-tool trace
	// messages are preserved for the old verbose interaction mode.
	var toolMsgIDs sync.Map // map[string]int

	// ── Tool Latency Tracking ──────────────────────────────────────
	// tool_call and tool_result fire in the same order within each
	// iteration batch. We track start times as a FIFO slice so on
	// tool_result we can report the actual execution duration.
	var toolStartTimes []time.Time
	recordToolStart := func() {
		toolStartTimes = append(toolStartTimes, time.Now())
	}
	popToolLatency := func() string {
		if len(toolStartTimes) == 0 {
			return ""
		}
		start := toolStartTimes[0]
		toolStartTimes = toolStartTimes[1:]
		d := time.Since(start)
		if d < time.Second {
			return fmt.Sprintf("%dms", d.Milliseconds())
		}
		return fmt.Sprintf("%.1fs", d.Seconds())
	}

	// Helper: build a progress line for a tool call.
	buildProgressLine := func(name, data string) string {
		emoji := render.ToolEmoji(name)
		preview := render.ToolPreview(name, data)
		if preview != "" {
			return fmt.Sprintf("%s %s: \"%s\"", emoji, name, preview)
		}
		return fmt.Sprintf("%s %s...", emoji, name)
	}

	// Helper: send or edit the progress bubble.
	sendProgress := func(line string) {
		if toolProgress == "off" || progressMsgID == 0 {
			return
		}

		// "new" mode: only show a new message when the tool changes.
		if toolProgress == "new" && len(progressLines) > 0 && line == lastProgressMsg {
			return
		}

		// Dedup: collapse consecutive identical messages.
		if line == lastProgressMsg {
			repeatCount++
			if len(progressLines) > 0 {
				progressLines[len(progressLines)-1] = fmt.Sprintf("%s (×%d)", line, repeatCount+1)
			}
		} else {
			lastProgressMsg = line
			repeatCount = 0
			progressLines = append(progressLines, line)
		}

		fullText := strings.Join(progressLines, "\n")

		// Throttle edits to avoid Telegram flood control.
		if time.Since(lastEditTime) < editThrottle {
			return // will be flushed on next non-throttled tick
		}

		if canEdit {
			err := bot.EditMessageText(chatID, progressMsgID, fullText, nil)
			if err != nil {
				errStr := err.Error()
				if strings.Contains(errStr, "flood") || strings.Contains(errStr, "retry after") {
					canEdit = false
					// Fallback: send as new message
					msg, err2 := bot.SendMessage(chatID, line,
						&telegram.SendOpts{ReplyToMessageID: messageID})
					if err2 == nil {
						progressMsgID = msg.ID
					}
				}
			}
			lastEditTime = time.Now()
		} else {
			// Editing failed previously — send new messages
			msg, err := bot.SendMessage(chatID, line,
				&telegram.SendOpts{ReplyToMessageID: messageID})
			if err == nil {
				progressMsgID = msg.ID
			}
		}
	}

	// reasoningProgressLine captures the first sentence of LLM reasoning
	// for use as the progress bubble content in "all"/"new" modes.
	// Reset at the start of each IsPreTool callback. When empty (no reasoning),
	// the ToolEventHandler falls back to the old ToolPreview approach.
	var reasoningProgressLine string
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
		if msg, err := bot.SendMessage(chatID, "❓ "+question,
			&telegram.SendOpts{ReplyMarkup: replyMarkup, ParseMode: "Markdown", ReplyToMessageID: messageID}); err != nil {
			return "", fmt.Errorf("clarify: send message: %w", err)
		} else {
			clarifyMsgIDs.Store(chatID, msg.ID)
			defer clarifyMsgIDs.Delete(chatID)
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
			// Defense-in-depth: never send buttons that use reserved internal
			// callback prefixes, even if the tool validation was bypassed.
			if err := validateSendMessageButtons(buttons); err != nil {
				return err
			}
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
		Model:            resolved.Model,
		BaseURL:          resolved.BaseURL,
		APIKey:           resolved.APIKey,
		MaxIterations:    resolved.MaxIter,
		MaxToolParallel:  resolved.MaxToolParallel,
		SystemMessage:    systemMessage,
		UntrustedWrapper: func(source, content string) string { return wrapUntrusted(context.Background(), source, content) },
		RuntimeContext:   odek.BuildRuntimeContext("telegram"),
		InteractionMode:  resolved.InteractionMode,
		NoProjectFile:    resolved.NoAgents,
		Skills:           skillsCfg,
		Thinking:         resolved.Thinking,
		Tools:            agentTools,
		Renderer:         rend,
		ToolEventHandler: func(event string, name string, data string) {
			// Enhance mode: send new messages with narrated descriptions.
			if isEnhance {
				// Content reset: interim messages reset the progress bubble.
				if event == "tool_call" && name == "send_message" {
					progressMsgID = 0
					progressLines = nil
					lastProgressMsg = ""
					repeatCount = 0
					return
				}
				switch event {
				case "tool_call":
					line := buildProgressLine(name, data)
					bot.SendMessage(chatID, telegram.EscapeMarkdown(line),
						&telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2})
				}
				return
			}

			// Progress modes: "all", "new", "verbose", "off".
			switch event {
			case "tool_call":
				// Content reset: if send_message fires mid-run, reset the
				// progress bubble so it appears below the sent message.
				if name == "send_message" {
					progressMsgID = 0
					progressLines = nil
					lastProgressMsg = ""
					repeatCount = 0
					return
				}

				if toolProgress == "verbose" {
					recordToolStart()
					line := fmt.Sprintf("%s `%s` %s", render.ToolEmoji(name), name, truncateToolArgs(data, 2000))
					line = telegram.EscapeMarkdown(line)
					bot.SendMessage(chatID, line,
						&telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2})
				} else if toolProgress == "all" || toolProgress == "new" {
					// Insert reasoning header (first sentence) at top of bubble
					if reasoningProgressLine != "" {
						if len(progressLines) == 0 || progressLines[0] != reasoningProgressLine {
							// Insert reasoning header as first line
							progressLines = append([]string{reasoningProgressLine}, progressLines...)
							lastProgressMsg = reasoningProgressLine
						}
						reasoningProgressLine = "" // consumed, reset for next call
					}
					// Always add tool-specific progress line
					line := buildProgressLine(name, data)
					sendProgress(line)
				}

			case "tool_result":
				if toolProgress == "verbose" {
					latency := popToolLatency()
					sizeLabel := fmt.Sprintf("%dB", len(data))
					if len(data) > 1024 {
						sizeLabel = fmt.Sprintf("%dKB", len(data)/1024)
					}
					line := fmt.Sprintf("%s `%s` ✅ (%s, %s)", render.ToolEmoji(name), name, latency, sizeLabel)
					line = telegram.EscapeMarkdown(line)
					bot.SendMessage(chatID, line,
						&telegram.SendOpts{ParseMode: telegram.ParseModeMarkdownV2})
				}
				// "all"/"new" modes: tool_result is silent (progress
				// bubble already shows the narrated tool call).
			}
		},
		// reasoningProgressLine is set in IsPreTool callback and consumed
		// by the ToolEventHandler to insert the reasoning header into the
		// progress bubble, followed by individual tool lines.
		IterationCallback: func(info loop.IterationInfo) {
			// Show the model's reasoning as a compact 💭 message for both
			// pre-tool iterations AND the final answer turn.
			// Previously only IsPreTool was handled, so final-answer reasoning
			// was silently dropped.
			if info.IsPreTool || info.HasFinalAnswer {
				reasoningProgressLine = ""
				if info.ReasoningContent != "" {
					firstSentence := render.FirstSentence(info.ReasoningContent)
					if firstSentence != "" {
						reasoningProgressLine = firstSentence
						bot.SendMessage(chatID, "💭 "+firstSentence,
							&telegram.SendOpts{ReplyToMessageID: messageID})
					}
				}
				if info.IsPreTool {
					return // pre-tool: stats not yet available, nothing more to do
				}
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
		MemoryEventHandler: func(event memory.MemoryEvent) {
			// Only surface memory activity in verbose mode, and only the
			// operationally meaningful events (skip high-frequency/internal ones
			// like episode_stored and episode_deduped to avoid chat noise).
			if skillsCfg == nil || !skillsCfg.Verbose {
				return
			}
			var msg string
			switch event.Type {
			case "fact_added":
				msg = "🧠 Memory fact added (" + event.Target + ")"
			case "fact_merged":
				msg = "🧠 Memory fact merged (" + event.Target + ")"
			case "fact_replaced":
				msg = "🧠 Memory fact updated (" + event.Target + ")"
			case "fact_removed":
				msg = "🧠 Memory fact removed (" + event.Target + ")"
			case "fact_consolidated":
				msg = fmt.Sprintf("🧠 Memory consolidated (%s: %d → %d)", event.Target, event.Count, event.NewCount)
			case "episode_evicted":
				msg = fmt.Sprintf("💾 %d episode(s) evicted", event.Count)
			case "episode_pending_review":
				msg = "🔒 Episode pending review (untrusted): " + event.SessionID
			case "episode_promoted":
				msg = "💾 Episode promoted: " + event.SessionID
			default:
				return
			}
			sendAsync(bot, chatID, msg, &telegram.SendOpts{ReplyToMessageID: messageID})
		},
		AgentSignalHandler: func(event loop.SignalEvent) {
			if skillsCfg == nil || !skillsCfg.Verbose {
				return
			}
			var msg string
			switch event.Type {
			case "context_trimmed":
				msg = fmt.Sprintf("✂️ Context trimmed (%s): %d group(s) dropped", event.Detail, event.Count)
			case "tool_recovery":
				msg = "🔁 Tool recovery: " + event.Tool
			default:
				return
			}
			sendAsync(bot, chatID, msg, &telegram.SendOpts{ReplyToMessageID: messageID})
		},
		Approver:        approver,
		DangerousConfig: &resolved.Dangerous,
	}

	agent, err := odek.New(agentCfg)
	if err != nil {
		reportError(bot, chatID, messageID, "Failed to create agent: "+err.Error())
		return
	}
	defer agent.Close()

	// Create a cancellable context so /stop can interrupt the agent loop.
	// Also apply a max run timeout to prevent runaway agents.
	var (
		agentCtx    context.Context
		agentCancel context.CancelFunc
	)
	if resolved.Telegram.AgentTimeout > 0 {
		agentCtx, agentCancel = context.WithTimeout(context.Background(),
			time.Duration(resolved.Telegram.AgentTimeout)*time.Second)
	} else {
		agentCtx, agentCancel = context.WithCancel(context.Background())
	}
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
			// Use updatedMessages (the agent's current state) instead of
			// cs.Messages (the input state) to preserve any tool results
			// or context updates made before cancellation.
			cs.LastActive = time.Now()
			if len(updatedMessages) > 0 {
				cs.Messages = updatedMessages
			}
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
			result := skills.AutoSaveSuggestions(filtered, userDir, *skillsCfg, false)
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

// truncateToolArgs truncates tool call arguments to at most maxLen characters
// for Telegram progress display. This prevents the "message is too long" error
// (Telegram's 4096 character limit) when sending verbose progress messages for
// calls like write_file that include large content payloads.
func truncateToolArgs(data string, maxLen int) string {
	if len(data) <= maxLen {
		return data
	}
	return data[:maxLen] + fmt.Sprintf("… [%d more bytes]", len(data)-maxLen)
}

// ── Singleton Lock ─────────────────────────────────────────────────────
//
// Prevents two bot instances from polling Telegram simultaneously (which
// causes 409 Conflict errors). Uses an advisory file lock on
// ~/.odek/telegram.lock via the internal/flock module.
//
// Why not a PID file? A PID file is probed with signals and, on macOS and
// other non-Linux POSIX systems, can easily be made to kill an unrelated
// process whose PID was planted by an attacker. flock is advisory, portable,
// and the OS automatically releases the lock when the holding process exits.
//
// LIFECYCLE
//
// Normal startup (no previous instance):
//
//	acquireLock() → flock succeeds → OK
//
// Competing startup (existing instance still alive):
//
//	acquireLock() → blocks on flock until the old process exits → OK
//
// Restart (child starts after parent's os.Exit(0)):
//
//	acquireLock() → old process released the lock via os.Exit → OK

// acquireLock acquires an exclusive advisory lock on ~/.odek/telegram.lock
// using the internal/flock module. The returned release function must be
// called on shutdown. Any legacy telegram.pid file is removed on success.
func acquireLock() (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	lockFile := filepath.Join(home, ".odek", "telegram.lock")

	// Ensure parent dir exists.
	if err := os.MkdirAll(filepath.Dir(lockFile), 0755); err != nil {
		return nil, fmt.Errorf("mkdir lock: %w", err)
	}

	release, err := flock.Lock(lockFile)
	if err != nil {
		return nil, fmt.Errorf("acquire singleton lock: %w", err)
	}

	// Clean up the legacy PID file if present; it is no longer used.
	pidFile := filepath.Join(home, ".odek", "telegram.pid")
	os.Remove(pidFile)

	return release, nil
}

// ── send_message helpers ──────────────────────────────────────────────

// photoVisionPrompt builds the extraction prompt handed to the vision model
// for a received photo. A non-empty caption focuses the (small) model on the
// part of the image the user is asking about; otherwise a thorough default
// describe prompt is used.
//
// The caption crosses the Telegram trust boundary, so it is wrapped as
// untrusted content before being embedded in the prompt. This prevents a
// prompt-injected caption from steering the local vision model as if it were
// a system instruction.
func photoVisionPrompt(caption string) string {
	if caption != "" {
		return fmt.Sprintf(
			"Describe this image in detail. Pay special attention to anything relevant to the user-provided caption below. Include any visible text, objects, people, and notable details.\n\n%s",
			wrapUntrusted(context.Background(), "telegram:photo:caption", caption))
	}
	return "Describe this image in detail. Include any visible text, objects, people, and notable details."
}

// photoVisionMessage builds the user-role message injected into the agent after
// the vision model extracts a description. description is expected to already be
// wrapped in <untrusted_content> boundaries by the vision tool. When a caption
// is present it is surfaced as the user's request so the agent answers it.
func photoVisionMessage(caption, description string) string {
	if caption != "" {
		return fmt.Sprintf(
			"The user sent an image with this message:\n%s\n\n"+
				"A local vision model extracted this description of the image:\n%s\n\n"+
				"Use the description to respond to the user's message.",
			wrapUntrusted(context.Background(), "telegram:photo:caption", caption), description)
	}
	return fmt.Sprintf(
		"The user sent an image (no caption). A local vision model extracted this description:\n%s\n\n"+
			"Respond appropriately — e.g. summarize what's in the image.",
		description)
}

// telegramTextMessage builds the user-role content for an incoming Telegram
// text message. Direct messages are kept as-is so the operator's typed intent
// is treated normally; forwarded messages are wrapped as untrusted because
// they cross an external trust boundary.
func telegramTextMessage(chatID int64, text string, forwarded bool) string {
	if forwarded {
		return wrapUntrusted(context.Background(), fmt.Sprintf("telegram:chat:%d:forwarded", chatID), text)
	}
	return text
}

// telegramVoiceMessage builds the user-role content for an auto-transcribed
// voice message. The transcript is produced by a speech-to-text model from
// attacker-influenceable audio, so it crosses an external trust boundary and
// must be wrapped as untrusted before it enters the message stream — a
// malicious recording must not become the user's "trusted" request.
func telegramVoiceMessage(chatID int64, transcript string) string {
	return wrapUntrusted(context.Background(), fmt.Sprintf("telegram:chat:%d:voice", chatID), transcript)
}

// telegramDocumentMessage builds the user-role message for an incoming
// document. The whole message is wrapped as untrusted because the document
// path comes from an external channel.
func telegramDocumentMessage(localPath string) string {
	return wrapUntrusted(context.Background(), "telegram:document", fmt.Sprintf("📄 Document received and saved to %q. Use shell tools to analyze and respond.", localPath))
}

// photoFallbackMessage builds the message injected when auto-describe is off or
// the vision model fails: it hands the agent the saved file path (and caption,
// if any) so the agent can analyze the image itself via the vision/shell tools.
func photoFallbackMessage(localPath, caption string) string {
	if caption != "" {
		return fmt.Sprintf("🖼 Photo saved to %q with this message from the user:\n%s\n\nUse the vision tool to analyze the image, then respond.", localPath, wrapUntrusted(context.Background(), "telegram:photo:caption", caption))
	}
	return fmt.Sprintf("🖼 Photo received and saved to %q. Use the vision tool or shell commands to analyze and respond.", localPath)
}

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
	// Defense-in-depth: validate the path against the media allowlist.
	resolved, err := telegram.ResolveMediaPath(path)
	if err != nil {
		return fmt.Errorf("telegram media: %w", err)
	}

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
		_, err := bot.SendPhoto(chatID, resolved, caption, opts)
		return err
	case "voice":
		_, err := bot.SendVoice(chatID, resolved, caption, opts)
		return err
	default:
		_, err := bot.SendDocument(chatID, resolved, caption, opts)
		return err
	}
}

// validateSendMessageButtons ensures no button uses a reserved internal
// callback-data prefix. This is defense-in-depth alongside the validation in
// internal/tool/send_message.go.
func validateSendMessageButtons(buttons [][]map[string]string) error {
	for i, row := range buttons {
		for j, btn := range row {
			cd := btn["callback_data"]
			if toolpkg.IsReservedCallbackPrefix(cd) {
				return fmt.Errorf("button[%d][%d] uses reserved callback_data prefix %q", i, j, cd)
			}
		}
	}
	return nil
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
