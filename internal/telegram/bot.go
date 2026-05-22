package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Bot represents a Telegram Bot API client.
type Bot struct {
	Token            string
	BaseURL          string
	FileBaseURL      string
	Client           *http.Client
	DailyTokenBudget int64
	log              Logger
}

// NewBot creates a new Bot with the given token and a default HTTP client
// with a 60-second timeout (generous for long-polling getUpdates calls).
func NewBot(token string) *Bot {
	return &Bot{
		Token:       token,
		BaseURL:     fmt.Sprintf("https://api.telegram.org/bot%s", token),
		FileBaseURL: fmt.Sprintf("https://api.telegram.org/file/bot%s", token),
		Client: &http.Client{
			Timeout: 60 * time.Second,
		},
		log: NewNopLogger(),
	}
}

// SetLogger sets the logger for this bot. If nil, a NopLogger is used (no-op).
func (b *Bot) SetLogger(l Logger) {
	if l == nil {
		b.log = NewNopLogger()
		return
	}
	b.log = l
}

// url builds the full API endpoint URL for the given method.
func (b *Bot) url(method string) string {
	return fmt.Sprintf("%s/%s", b.BaseURL, method)
}

// doJSON marshals the request body, sends a POST request, and unmarshals
// the "result" field of the response into the provided destination.
// Retries on transient errors: network errors, 429 (rate limit), and 5xx
// server errors, with exponential backoff (1s, 2s, 4s, 8s; max 4 retries).
// Does NOT retry on 4xx errors (except 429) — those are client errors.
func (b *Bot) doJSON(method string, body any, dest any) error {
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			b.log.Error("marshal request failed", "method", method, "error", err)
			return fmt.Errorf("telegram: marshal request: %w", err)
		}
	}

	url := b.url(method)
	var lastErr error

	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second // 1s, 2s, 4s, 8s
			b.log.Warn("retrying request", "method", method, "attempt", attempt, "backoff", backoff)
			time.Sleep(backoff)
		}

		resp, err := b.Client.Post(url, "application/json", bytes.NewReader(reqBody))
		if err != nil {
			b.log.Error("http post failed", "method", method, "error", err)
			lastErr = fmt.Errorf("telegram: post %s: %w", method, err)
			continue // network error — retry
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			b.log.Error("read response body failed", "method", method, "error", err)
			lastErr = fmt.Errorf("telegram: read response: %w", err)
			continue // read error — retry
		}

		var apiResp struct {
			OK          bool            `json:"ok"`
			Result      json.RawMessage `json:"result"`
			Description string          `json:"description"`
			ErrorCode   int             `json:"error_code"`
		}
		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			b.log.Error("unmarshal response failed", "method", method, "error", err)
			return fmt.Errorf("telegram: unmarshal response: %w", err) // parse error — don't retry
		}

		if !apiResp.OK {
			// 429 (rate limit) — retry
			if apiResp.ErrorCode == 429 {
				b.log.Warn("rate limited", "method", method, "description", apiResp.Description)
				lastErr = fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
				continue
			}
			// 5xx (server error) — retry
			if apiResp.ErrorCode >= 500 && apiResp.ErrorCode < 600 {
				b.log.Warn("server error", "method", method, "error_code", apiResp.ErrorCode, "description", apiResp.Description)
				lastErr = fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
				continue
			}
			// 4xx (client error, not 429) — don't retry
			b.log.Error("api error", "method", method, "description", apiResp.Description, "error_code", apiResp.ErrorCode)
			return fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
		}

		if dest != nil && len(apiResp.Result) > 0 {
			if err := json.Unmarshal(apiResp.Result, dest); err != nil {
				b.log.Error("unmarshal result failed", "method", method, "error", err)
				return fmt.Errorf("telegram: unmarshal result: %w", err)
			}
		}

		return nil
	}

	return lastErr
}

// doUpload sends a multipart/form-data POST request with a file and optional parameters.
// Retries on transient errors with the same backoff strategy as doJSON.
func (b *Bot) doUpload(method string, field string, path string, params map[string]any, dest any) error {
	file, err := os.Open(path)
	if err != nil {
		b.log.Error("open file failed", "method", method, "path", path, "error", err)
		return fmt.Errorf("telegram: open file %s: %w", path, err)
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Write the file part.
	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		b.log.Error("create form file failed", "method", method, "field", field, "error", err)
		return fmt.Errorf("telegram: create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		b.log.Error("copy file content failed", "method", method, "path", path, "error", err)
		return fmt.Errorf("telegram: copy file content: %w", err)
	}

	// Write extra parameters as JSON parts.
	for key, val := range params {
		jsonVal, err := json.Marshal(val)
		if err != nil {
			b.log.Error("marshal param failed", "method", method, "key", key, "error", err)
			return fmt.Errorf("telegram: marshal param %s: %w", key, err)
		}
		if err := writer.WriteField(key, string(jsonVal)); err != nil {
			b.log.Error("write field failed", "method", method, "key", key, "error", err)
			return fmt.Errorf("telegram: write field %s: %w", key, err)
		}
	}

	if err := writer.Close(); err != nil {
		b.log.Error("close multipart writer failed", "method", method, "error", err)
		return fmt.Errorf("telegram: close multipart writer: %w", err)
	}

	bodyBytes := buf.Bytes()
	contentType := writer.FormDataContentType()
	url := b.url(method)
	var lastErr error

	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			b.log.Warn("retrying upload", "method", method, "attempt", attempt, "backoff", backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			b.log.Error("create request failed", "method", method, "error", err)
			return fmt.Errorf("telegram: create request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)

		resp, err := b.Client.Do(req)
		if err != nil {
			b.log.Error("http post failed", "method", method, "error", err)
			lastErr = fmt.Errorf("telegram: post %s: %w", method, err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			b.log.Error("read response body failed", "method", method, "error", err)
			lastErr = fmt.Errorf("telegram: read response: %w", err)
			continue
		}

		var apiResp struct {
			OK          bool            `json:"ok"`
			Result      json.RawMessage `json:"result"`
			Description string          `json:"description"`
			ErrorCode   int             `json:"error_code"`
		}
		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			b.log.Error("unmarshal response failed", "method", method, "error", err)
			return fmt.Errorf("telegram: unmarshal response: %w", err)
		}

		if !apiResp.OK {
			if apiResp.ErrorCode == 429 {
				b.log.Warn("rate limited", "method", method, "description", apiResp.Description)
				lastErr = fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
				continue
			}
			if apiResp.ErrorCode >= 500 && apiResp.ErrorCode < 600 {
				b.log.Warn("server error", "method", method, "error_code", apiResp.ErrorCode, "description", apiResp.Description)
				lastErr = fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
				continue
			}
			b.log.Error("api error", "method", method, "description", apiResp.Description, "error_code", apiResp.ErrorCode)
			return fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
		}

		if dest != nil && len(apiResp.Result) > 0 {
			if err := json.Unmarshal(apiResp.Result, dest); err != nil {
				b.log.Error("unmarshal result failed", "method", method, "error", err)
				return fmt.Errorf("telegram: unmarshal result: %w", err)
			}
		}

		return nil
	}

	return lastErr
}

// IsFatalAPIError reports whether a Telegram API error is fatal (should not
// be retried). Errors with status codes 401 (Unauthorized), 403 (Forbidden),
// and 409 (Conflict — duplicate polling instance) are fatal.
func IsFatalAPIError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// Match the format from doJSON: "telegram: <method> failed: <desc> (code NNN)"
	// Check for known fatal codes.
	for _, code := range []string{"(code 401)", "(code 403)", "(code 409)"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}

// SendMessage sends a text message to the specified chat.
func (b *Bot) SendMessage(chatID int64, text string, opts *SendOpts) (*Message, error) {
	params := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if opts != nil {
		if opts.ParseMode != "" {
			params["parse_mode"] = opts.ParseMode
		}
		if opts.ReplyMarkup != nil {
			params["reply_markup"] = opts.ReplyMarkup
		}
		if opts.DisableWebPagePreview {
			params["disable_web_page_preview"] = true
		}
		if opts.ReplyToMessageID != 0 {
			params["reply_to_message_id"] = opts.ReplyToMessageID
		}
	}

	var msg Message
	if err := b.doJSON("sendMessage", params, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// EditMessageText edits a previously sent message in the given chat.
// The messageID must identify an existing message sent by the bot.
// Supports SendOpts for parse_mode. Returns an error if the message
// hasn't changed (Telegram "Bad Request: message is not modified").
func (b *Bot) EditMessageText(chatID int64, messageID int, text string, opts *SendOpts) error {
	params := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if opts != nil && opts.ParseMode != "" {
		params["parse_mode"] = opts.ParseMode
	}
	return b.doJSON("editMessageText", params, nil)
}

// DeleteMessage deletes a message previously sent by the bot.
// Requires the bot to have can_delete_messages permission in groups/supergroups.
func (b *Bot) DeleteMessage(chatID int64, messageID int) error {
	params := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	return b.doJSON("deleteMessage", params, nil)
}

// SendPhoto sends a photo from a file path to the specified chat.
// opts may contain ReplyToMessageID to reply to a specific message.
func (b *Bot) SendPhoto(chatID int64, path string, caption string, opts *SendOpts) (*Message, error) {
	params := map[string]any{
		"chat_id": chatID,
	}
	if caption != "" {
		params["caption"] = caption
	}
	if opts != nil && opts.ReplyToMessageID != 0 {
		params["reply_to_message_id"] = opts.ReplyToMessageID
	}

	var msg Message
	if err := b.doUpload("sendPhoto", "photo", path, params, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendVoice sends a voice note from a file path to the specified chat.
// opts may contain ReplyToMessageID to reply to a specific message.
func (b *Bot) SendVoice(chatID int64, path string, caption string, opts *SendOpts) (*Message, error) {
	params := map[string]any{
		"chat_id": chatID,
	}
	if caption != "" {
		params["caption"] = caption
	}
	if opts != nil && opts.ReplyToMessageID != 0 {
		params["reply_to_message_id"] = opts.ReplyToMessageID
	}

	var msg Message
	if err := b.doUpload("sendVoice", "voice", path, params, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendDocument sends a document from a file path to the specified chat.
// opts may contain ReplyToMessageID to reply to a specific message.
func (b *Bot) SendDocument(chatID int64, path string, caption string, opts *SendOpts) (*Message, error) {
	params := map[string]any{
		"chat_id": chatID,
	}
	if caption != "" {
		params["caption"] = caption
	}
	if opts != nil && opts.ReplyToMessageID != 0 {
		params["reply_to_message_id"] = opts.ReplyToMessageID
	}

	var msg Message
	if err := b.doUpload("sendDocument", "document", path, params, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// GetUpdates retrieves incoming updates using long polling.
func (b *Bot) GetUpdates(offset int, timeout int) ([]Update, error) {
	params := map[string]any{
		"offset":  offset,
		"timeout": timeout,
	}

	var updates []Update
	if err := b.doJSON("getUpdates", params, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// GetFile returns basic information about a file and prepares it for downloading.
func (b *Bot) GetFile(fileID string) (*File, error) {
	params := map[string]any{
		"file_id": fileID,
	}

	var file File
	if err := b.doJSON("getFile", params, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

// DownloadFile downloads a file from Telegram's file server and returns its raw bytes.
func (b *Bot) DownloadFile(filePath string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s", b.FileBaseURL, filePath)

	resp, err := b.Client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("telegram: download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram: download file: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("telegram: read file data: %w", err)
	}
	return data, nil
}

// AnswerCallbackQuery sends an answer to a callback query.
func (b *Bot) AnswerCallbackQuery(callbackID string, text string, showAlert bool) error {
	params := map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        showAlert,
	}
	return b.doJSON("answerCallbackQuery", params, nil)
}

// SetMyCommands sets the list of the bot's commands.
func (b *Bot) SetMyCommands(commands []BotCommand) error {
	params := map[string]any{
		"commands": commands,
	}
	return b.doJSON("setMyCommands", params, nil)
}

// SetFallbackURLs configures fallback Telegram API endpoints to try if the
// primary endpoint is unreachable. Each URL should be a base API URL such as
// "https://api.telegram.org" (without the /bot<token> suffix). The fallback
// transport rewrites the host on each request, keeping the original path
// (which includes the token).
func (b *Bot) SetFallbackURLs(urls []string) {
	if len(urls) == 0 {
		return
	}
	ft := NewFallbackTransport(urls)
	ft.WrapBot(b)
}

// SetDailyTokenBudget sets the daily token usage budget for the bot.
// When non-zero, CheckDailyBudget will reject token usage that exceeds
// this limit within a calendar day.
func (b *Bot) SetDailyTokenBudget(budget int64) {
	b.DailyTokenBudget = budget
}

// budgetFilePath returns the path to the daily token usage tracking file.
// The file is scoped to the current date so budgets reset each day.
func budgetFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	date := time.Now().Format("2006-01-02")
	return filepath.Join(home, ".odek", "telegram_token_usage_"+date)
}

// CheckDailyBudget reads the current daily token usage tracking file,
// adds the given number of tokens, and returns an error if the total
// exceeds the configured DailyTokenBudget. If the budget is zero (unset),
// no check is performed and nil is returned.
func (b *Bot) CheckDailyBudget(tokens int64) error {
	if b.DailyTokenBudget <= 0 {
		return nil // budget not configured
	}
	if tokens <= 0 {
		return nil // nothing to track
	}

	path := budgetFilePath()

	// Ensure the parent .odek directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("telegram: create budget dir: %w", err)
	}

	// Read current usage (file may not exist yet — that's fine).
	var current int64
	data, err := os.ReadFile(path)
	if err == nil {
		if parsed, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			current = parsed
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("telegram: read budget file: %w", err)
	}

	total := current + tokens
	if total > b.DailyTokenBudget {
		return fmt.Errorf(
			"daily token budget exceeded: %d used + %d new = %d total, limit is %d",
			current, tokens, total, b.DailyTokenBudget,
		)
	}

	// Write the updated count.
	if err := os.WriteFile(path, []byte(strconv.FormatInt(total, 10)), 0644); err != nil {
		return fmt.Errorf("telegram: write budget file: %w", err)
	}

	return nil
}

// DailyTokenUsage returns the current token usage and budget limit.
// Returns (0, 0) when the budget is not configured.
func (b *Bot) DailyTokenUsage() (used int64, limit int64) {
	if b.DailyTokenBudget <= 0 {
		return 0, 0
	}
	path := budgetFilePath()
	data, err := os.ReadFile(path)
	if err == nil {
		if parsed, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			used = parsed
		}
	}
	return used, b.DailyTokenBudget
}

// GetMe returns basic information about the bot (useful as a health check).
func (b *Bot) GetMe() (*User, error) {
	var user User
	if err := b.doJSON("getMe", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// SendChatAction tells the user that the bot is doing something on their
// behalf (e.g., "typing"). The action is shown as a status in the chat for
// ~5 seconds or until the next message is sent. Callers should re-send every
// 4 seconds for long-running operations.
func (b *Bot) SendChatAction(chatID int64, action string) error {
	params := map[string]any{
		"chat_id": chatID,
		"action":  action,
	}
	return b.doJSON("sendChatAction", params, nil)
}
