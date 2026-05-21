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
	"time"
)

// Bot represents a Telegram Bot API client.
type Bot struct {
	Token       string
	BaseURL     string
	FileBaseURL string
	Client      *http.Client
}

// NewBot creates a new Bot with the given token and a default HTTP client
// with a 30-second timeout.
func NewBot(token string) *Bot {
	return &Bot{
		Token:       token,
		BaseURL:     fmt.Sprintf("https://api.telegram.org/bot%s", token),
		FileBaseURL: fmt.Sprintf("https://api.telegram.org/file/bot%s", token),
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// url builds the full API endpoint URL for the given method.
func (b *Bot) url(method string) string {
	return fmt.Sprintf("%s/%s", b.BaseURL, method)
}

// doJSON marshals the request body, sends a POST request, and unmarshals
// the "result" field of the response into the provided destination.
func (b *Bot) doJSON(method string, body any, dest any) error {
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("telegram: marshal request: %w", err)
		}
	}

	url := b.url(method)
	resp, err := b.Client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("telegram: post %s: %w", method, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram: read response: %w", err)
	}

	var apiResp struct {
		OK          bool              `json:"ok"`
		Result      json.RawMessage   `json:"result"`
		Description string            `json:"description"`
		ErrorCode   int               `json:"error_code"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("telegram: unmarshal response: %w", err)
	}

	if !apiResp.OK {
		return fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
	}

	if dest != nil && len(apiResp.Result) > 0 {
		if err := json.Unmarshal(apiResp.Result, dest); err != nil {
			return fmt.Errorf("telegram: unmarshal result: %w", err)
		}
	}

	return nil
}

// doUpload sends a multipart/form-data POST request with a file and optional parameters.
func (b *Bot) doUpload(method string, field string, path string, params map[string]any, dest any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("telegram: open file %s: %w", path, err)
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Write the file part.
	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("telegram: create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("telegram: copy file content: %w", err)
	}

	// Write extra parameters as JSON parts.
	for key, val := range params {
		jsonVal, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("telegram: marshal param %s: %w", key, err)
		}
		if err := writer.WriteField(key, string(jsonVal)); err != nil {
			return fmt.Errorf("telegram: write field %s: %w", key, err)
		}
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("telegram: close multipart writer: %w", err)
	}

	url := b.url(method)
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return fmt.Errorf("telegram: create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := b.Client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: post %s: %w", method, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram: read response: %w", err)
	}

	var apiResp struct {
		OK          bool              `json:"ok"`
		Result      json.RawMessage   `json:"result"`
		Description string            `json:"description"`
		ErrorCode   int               `json:"error_code"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("telegram: unmarshal response: %w", err)
	}

	if !apiResp.OK {
		return fmt.Errorf("telegram: %s failed: %s (code %d)", method, apiResp.Description, apiResp.ErrorCode)
	}

	if dest != nil && len(apiResp.Result) > 0 {
		if err := json.Unmarshal(apiResp.Result, dest); err != nil {
			return fmt.Errorf("telegram: unmarshal result: %w", err)
		}
	}

	return nil
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

// SendPhoto sends a photo from a file path to the specified chat.
func (b *Bot) SendPhoto(chatID int64, path string, caption string) (*Message, error) {
	params := map[string]any{
		"chat_id": chatID,
	}
	if caption != "" {
		params["caption"] = caption
	}

	var msg Message
	if err := b.doUpload("sendPhoto", "photo", path, params, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendVoice sends a voice note from a file path to the specified chat.
func (b *Bot) SendVoice(chatID int64, path string, caption string) (*Message, error) {
	params := map[string]any{
		"chat_id": chatID,
	}
	if caption != "" {
		params["caption"] = caption
	}

	var msg Message
	if err := b.doUpload("sendVoice", "voice", path, params, &msg); err != nil {
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

// GetMe returns basic information about the bot (useful as a health check).
func (b *Bot) GetMe() (*User, error) {
	var user User
	if err := b.doJSON("getMe", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}
