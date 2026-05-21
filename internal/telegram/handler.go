package telegram

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// ─── Config ────────────────────────────────────────────────────────────────

// HandlerConfig controls which messages the Handler processes.
type HandlerConfig struct {
	AllowedChats []int64 // empty = allow all
	BotUsername  string  // for @mention detection in groups (without @)
	MaxMsgLength int     // default: 4096
	AllowedUsers []int64 // empty = allow all users
}

// ─── Handler ──────────────────────────────────────────────────────────────

// Handler routes incoming Telegram updates to the appropriate callback based
// on message type. It is the bridge between the raw Telegram API and the agent.
type Handler struct {
	Bot    *Bot
	Config HandlerConfig
	log    Logger

	// approvers maps chatID → *TelegramApprover for inline keyboard approval
	// requests. Protected by sync.Map for concurrent read/write from the
	// update loop (reads) and agent goroutines (writes).
	approvers sync.Map

	// OnTextMessage is called when a plain text message is received.
	// Returns the response text (may be empty).
	// Should run asynchronously if it starts the agent loop — callers
	// should dispatch to a goroutine to avoid blocking the update loop.
	OnTextMessage func(chatID int64, text string) (string, error)

	// OnCallbackQuery is called when a callback query is received and
	// it was NOT handled by the TelegramApprover. Returns the response
	// text (may be empty).
	OnCallbackQuery func(chatID int64, callbackData string) (string, error)

	// OnCommand is called when a bot command (e.g. /start) is received.
	// Returns the response text (may be empty).
	OnCommand func(chatID int64, command string, args string) (string, error)

	// OnVoiceMessage is called when a voice message is received.
	// Returns the response text (may be empty).
	// fileID is the Telegram file ID of the voice message in OGG format.
	// Callers should use DownloadVoice to save the file locally.
	OnVoiceMessage func(chatID int64, fileID string) (string, error)

	// OnPhotoMessage is called when a photo message is received.
	// Returns the response text (may be empty).
	// fileIDs contains all available sizes (last = largest).
	// Callers should use DownloadPhoto with the last element.
	OnPhotoMessage func(chatID int64, fileIDs []string) (string, error)

	// OnError is called when a processing error occurs.
	OnError func(chatID int64, err error)
}

// SetApprover stores a TelegramApprover for the given chat ID.
// Thread-safe: safe to call from any goroutine.
func (h *Handler) SetApprover(chatID int64, a *TelegramApprover) {
	h.approvers.Store(chatID, a)
}

// GetApprover retrieves the TelegramApprover for the given chat ID.
// Returns nil if no approver is registered. Thread-safe.
func (h *Handler) GetApprover(chatID int64) *TelegramApprover {
	v, ok := h.approvers.Load(chatID)
	if !ok {
		return nil
	}
	a, _ := v.(*TelegramApprover)
	return a
}

// DeleteApprover removes the TelegramApprover for the given chat ID.
// Thread-safe. Used when a session is reset or ends.
func (h *Handler) DeleteApprover(chatID int64) {
	h.approvers.Delete(chatID)
}

// NewHandler creates a Handler with the given bot and default settings.
func NewHandler(bot *Bot) *Handler {
	return &Handler{
		Bot:    bot,
		Config: HandlerConfig{
			MaxMsgLength: 4096,
		},
		log: NewNopLogger(),
		OnTextMessage:   defaultTextHandler(),
		OnCallbackQuery: defaultCallbackHandler(),
		OnCommand:       defaultCommandHandler(),
		OnVoiceMessage:  defaultVoiceHandler(bot),
		OnPhotoMessage:  defaultPhotoHandler(bot),
	}
}

// SetLogger sets the logger for this handler. If nil, a NopLogger is used.
func (h *Handler) SetLogger(l Logger) {
	if l == nil {
		h.log = NewNopLogger()
		return
	}
	h.log = l
}

// defaultTextHandler returns a default OnTextMessage callback.
func defaultTextHandler() func(int64, string) (string, error) {
	return func(_ int64, _ string) (string, error) {
		return "Not implemented yet: text", nil
	}
}

// defaultCallbackHandler returns a default OnCallbackQuery callback.
func defaultCallbackHandler() func(int64, string) (string, error) {
	return func(_ int64, _ string) (string, error) {
		return "Not implemented yet: callback query", nil
	}
}

// defaultCommandHandler returns a default OnCommand callback.
func defaultCommandHandler() func(int64, string, string) (string, error) {
	return func(_ int64, _, _ string) (string, error) {
		return "Not implemented yet: command", nil
	}
}

// defaultVoiceHandler returns a default OnVoiceMessage callback that downloads
// the voice file and returns a MEDIA: response.
func defaultVoiceHandler(bot *Bot) func(int64, string) (string, error) {
	return func(chatID int64, fileID string) (string, error) {
		path, err := DownloadVoice(bot, fileID)
		if err != nil {
			return "", fmt.Errorf("telegram handler: download voice: %w", err)
		}
		return fmt.Sprintf("MEDIA:voice:%s", path), nil
	}
}

// defaultPhotoHandler returns a default OnPhotoMessage callback that downloads
// the largest photo size and returns a MEDIA: response.
func defaultPhotoHandler(bot *Bot) func(int64, []string) (string, error) {
	return func(chatID int64, fileIDs []string) (string, error) {
		path, err := DownloadPhoto(bot, fileIDs)
		if err != nil {
			return "", fmt.Errorf("telegram handler: download photo: %w", err)
		}
		return fmt.Sprintf("MEDIA:photo:%s", path), nil
	}
}

// ─── Update Routing ───────────────────────────────────────────────────────

// HandleUpdate routes an incoming Telegram update to the appropriate handler.
func (h *Handler) HandleUpdate(upd Update) {
	switch {
	case upd.Message != nil:
		h.handleMessage(upd.Message)
	case upd.CallbackQuery != nil:
		h.handleCallback(upd.CallbackQuery)
	default:
		h.log.Warn("ignoring unsupported update type", "update_id", upd.ID)
	}
}

// handleMessage routes a single message based on content type and permissions.
func (h *Handler) handleMessage(msg *Message) {
	if msg.Chat == nil || msg.From == nil {
		return
	}

	if !h.isAllowed(msg.Chat.ID, msg.From.ID) {
		return
	}

	switch {
	case msg.IsCommand():
		h.handleCommand(msg)
	case msg.Voice != nil:
		if h.OnVoiceMessage != nil {
			resp, err := h.OnVoiceMessage(msg.Chat.ID, msg.Voice.FileID)
			if err != nil {
				h.log.Error("voice message handler failed", "chat_id", msg.Chat.ID, "error", err)
				if h.OnError != nil {
					h.OnError(msg.Chat.ID, err)
				}
				return
			}
			if resp != "" {
				h.SendResponse(msg.Chat.ID, resp)
			}
		}
	case msg.Photo != nil:
		if h.OnPhotoMessage != nil {
			fileIDs := make([]string, len(msg.Photo))
			for i, p := range msg.Photo {
				fileIDs[i] = p.FileID
			}
			resp, err := h.OnPhotoMessage(msg.Chat.ID, fileIDs)
			if err != nil {
				h.log.Error("photo message handler failed", "chat_id", msg.Chat.ID, "error", err)
				if h.OnError != nil {
					h.OnError(msg.Chat.ID, err)
				}
				return
			}
			if resp != "" {
				h.SendResponse(msg.Chat.ID, resp)
			}
		}
	case msg.Text != "":
		if h.OnTextMessage != nil {
			resp, err := h.OnTextMessage(msg.Chat.ID, msg.Text)
			if err != nil {
				h.log.Error("text message handler failed", "chat_id", msg.Chat.ID, "error", err)
				if h.OnError != nil {
					h.OnError(msg.Chat.ID, err)
				}
				return
			}
			if resp != "" {
				h.SendResponse(msg.Chat.ID, resp)
			}
		}
	default:
		// Ignore unsupported message types (stickers, locations, etc.)
	}
}

// handleCommand processes a bot command message.
func (h *Handler) handleCommand(msg *Message) {
	cmd, args := extractCommand(msg.Text)
	if cmd == "" {
		return
	}

	// Check if command was targeted at a specific bot via @username.
	// Only respond if the username matches our BotUsername.
	if h.Config.BotUsername != "" {
		parts := strings.SplitN(msg.Text, " ", 2)
		cmdPart := parts[0]
		if atIdx := strings.Index(cmdPart, "@"); atIdx >= 0 {
			mentioned := cmdPart[atIdx+1:]
			if mentioned != "" && !strings.EqualFold(mentioned, h.Config.BotUsername) {
				// Command intended for a different bot — ignore.
				return
			}
		}
	}

	if h.OnCommand != nil {
		resp, err := h.OnCommand(msg.Chat.ID, cmd, args)
		if err != nil {
			h.log.Error("command handler failed", "chat_id", msg.Chat.ID, "command", cmd, "error", err)
			if h.OnError != nil {
				h.OnError(msg.Chat.ID, err)
			}
			return
		}
		if resp != "" {
			h.SendResponse(msg.Chat.ID, resp)
		}
	}
}

// handleCallback processes a callback query from an inline keyboard.
func (h *Handler) handleCallback(cq *CallbackQuery) {
	if cq.Message == nil || cq.Message.Chat == nil {
		return
	}

	// Route approval callbacks to the per-chat TelegramApprover.
	if a := h.GetApprover(cq.Message.Chat.ID); a != nil && a.HandleCallback(cq.Data) {
		// Answer the callback (remove loading state on button).
		if err := h.Bot.AnswerCallbackQuery(cq.ID, "", false); err != nil {
			h.log.Error("answer callback query (approval) failed", "chat_id", cq.Message.Chat.ID, "error", err)
			if h.OnError != nil {
				h.OnError(cq.Message.Chat.ID, err)
			}
		}
		return
	}

	// Answer the callback query to remove the loading state on the button.
	if err := h.Bot.AnswerCallbackQuery(cq.ID, "", false); err != nil {
		h.log.Error("answer callback query failed", "chat_id", cq.Message.Chat.ID, "error", err)
		if h.OnError != nil {
			h.OnError(cq.Message.Chat.ID, err)
		}
		return
	}

	if h.OnCallbackQuery != nil {
		resp, err := h.OnCallbackQuery(cq.Message.Chat.ID, cq.Data)
		if err != nil {
			h.log.Error("callback query handler failed", "chat_id", cq.Message.Chat.ID, "data", cq.Data, "error", err)
			if h.OnError != nil {
				h.OnError(cq.Message.Chat.ID, err)
			}
			return
		}
		if resp != "" {
			h.SendResponse(cq.Message.Chat.ID, resp)
		}
	}
}

// ─── Response Sending ─────────────────────────────────────────────────────

// SendResponse sends a response text to the given chat.
// It handles MEDIA: prefix, chunking, MarkdownV2 formatting, and retry logic.
func (h *Handler) SendResponse(chatID int64, text string) {
	if text == "" {
		return
	}

	// Check for MEDIA: prefix.
	if strings.HasPrefix(text, "MEDIA:") {
		h.sendMedia(chatID, text)
		return
	}

	// Split into chunks via FormatResponse.
	chunks, err := FormatResponse(text)
	if err != nil {
		h.log.Error("format response failed", "chat_id", chatID, "error", err)
		if h.OnError != nil {
			h.OnError(chatID, fmt.Errorf("telegram: format response: %w", err))
		}
		return
	}

	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		h.sendChunk(chatID, chunk)
	}
}

// sendMedia handles a MEDIA: prefixed response.
// Format: "MEDIA:photo:/path/to/file.jpg" or "MEDIA:voice:/path/to/file.ogg"
func (h *Handler) sendMedia(chatID int64, text string) {
	// Strip the "MEDIA:" prefix.
	rest := strings.TrimPrefix(text, "MEDIA:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) < 2 {
		return
	}

	mediaType := parts[0]
	filePath := parts[1]

	// Check if file exists.
	if _, err := os.Stat(filePath); err != nil {
		h.log.Error("media file not found", "chat_id", chatID, "path", filePath, "error", err)
		if h.OnError != nil {
			h.OnError(chatID, fmt.Errorf("telegram: media file not found: %s: %w", filePath, err))
		}
		return
	}

	var err error
	switch mediaType {
	case "photo":
		_, err = h.Bot.SendPhoto(chatID, filePath, "")
	case "voice":
		_, err = h.Bot.SendVoice(chatID, filePath, "")
	default:
		h.log.Error("unknown media type", "chat_id", chatID, "media_type", mediaType)
		if h.OnError != nil {
			h.OnError(chatID, fmt.Errorf("telegram: unknown media type: %s", mediaType))
		}
		return
	}

	if err != nil {
		h.log.Error("send media failed", "chat_id", chatID, "media_type", mediaType, "path", filePath, "error", err)
		if h.OnError != nil {
			h.OnError(chatID, fmt.Errorf("telegram: send media: %w", err))
		}
	}
}

// sendChunk sends a single text chunk, retrying with plain text on parse errors.
func (h *Handler) sendChunk(chatID int64, chunk string) {
	// Try with MarkdownV2 first.
	opts := &SendOpts{ParseMode: ParseModeMarkdownV2}
	_, err := h.Bot.SendMessage(chatID, chunk, opts)
	if err == nil {
		return
	}

	// If it's a "Can't parse entities" error, retry with plain text.
	errStr := err.Error()
	if strings.Contains(errStr, "Can't parse entities") || strings.Contains(errStr, "can't parse") {
		_, err = h.Bot.SendMessage(chatID, chunk, nil)
	}

	if err != nil {
		h.log.Error("send message failed", "chat_id", chatID, "error", err)
		if h.OnError != nil {
			h.OnError(chatID, fmt.Errorf("telegram: send message: %w", err))
		}
	}
}

// ─── Access Control ───────────────────────────────────────────────────────

// isAllowed checks if the given chat and user are allowed to interact.
// Returns true if both pass (or their respective allowlists are empty).
func (h *Handler) isAllowed(chatID int64, userID int64) bool {
	if len(h.Config.AllowedChats) > 0 {
		found := false
		for _, id := range h.Config.AllowedChats {
			if id == chatID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if len(h.Config.AllowedUsers) > 0 {
		found := false
		for _, id := range h.Config.AllowedUsers {
			if id == userID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// ─── Message Helpers ──────────────────────────────────────────────────────

// IsCommand reports whether the message is a bot command.
// It checks the entities for type "bot_command".
func (m *Message) IsCommand() bool {
	if m == nil {
		return false
	}
	for _, e := range m.Entities {
		if e.Type == "bot_command" {
			return true
		}
	}
	// Fallback: check if text starts with "/".
	return strings.HasPrefix(strings.TrimSpace(m.Text), "/")
}

// extractCommand parses a command string into the command name and arguments.
//
//	"/command arg1 arg2"        →  ("command", "arg1 arg2")
//	"/command@BotName arg1"     →  ("command", "arg1 arg2")
//	"plain text"                 →  ("", "plain text")
func extractCommand(text string) (cmd string, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}

	// Split into command token and the rest.
	parts := strings.SplitN(text, " ", 2)
	cmdPart := parts[0]

	// Strip the leading "/".
	cmdPart = cmdPart[1:]

	// Strip @BotUsername if present.
	if atIdx := strings.Index(cmdPart, "@"); atIdx >= 0 {
		cmdPart = cmdPart[:atIdx]
	}

	args = ""
	if len(parts) > 1 {
		args = parts[1]
	}

	return cmdPart, args
}

