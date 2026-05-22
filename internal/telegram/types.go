package telegram

import "encoding/json"

// Parse mode constants for SendOpts.
const (
	ParseModeMarkdownV2 = "MarkdownV2"
	ParseModeHTML       = "HTML"
)

// Update represents an incoming Telegram update.
type Update struct {
	ID            int             `json:"update_id"`
	Message       *Message        `json:"message,omitempty"`
	EditedMessage *Message        `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery  `json:"callback_query,omitempty"`
}

// Message represents a Telegram message.
type Message struct {
	ID          int                    `json:"message_id"`
	From        *User                  `json:"from,omitempty"`
	Chat        *Chat                  `json:"chat,omitempty"`
	Date        int                    `json:"date,omitempty"`
	Text        string                 `json:"text,omitempty"`
	Entities    []MessageEntity        `json:"entities,omitempty"`
	Photo       []PhotoSize            `json:"photo,omitempty"`
	Voice       *Voice                 `json:"voice,omitempty"`
	Document    *Document              `json:"document,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup  `json:"reply_markup,omitempty"`
}

// User represents a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	IsBot     bool   `json:"is_bot,omitempty"`
}

// Chat represents a Telegram chat.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type,omitempty"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// MessageEntity represents a special entity in a text message.
type MessageEntity struct {
	Type   string `json:"type,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Length int    `json:"length,omitempty"`
	URL    string `json:"url,omitempty"`
	User   *User  `json:"user,omitempty"`
}

// PhotoSize represents one size of a photo or a file/sticker thumbnail.
type PhotoSize struct {
	FileID       string `json:"file_id,omitempty"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Voice represents a voice note.
type Voice struct {
	FileID       string `json:"file_id,omitempty"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Document represents a general file (as opposed to photos, voice messages, etc.).
type Document struct {
	FileID       string `json:"file_id,omitempty"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// InlineKeyboardMarkup represents an inline keyboard that appears next to the message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton represents one button of an inline keyboard.
type InlineKeyboardButton struct {
	Text         string `json:"text,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// CallbackQuery represents an incoming callback query from a callback button in an inline keyboard.
type CallbackQuery struct {
	ID      string   `json:"id,omitempty"`
	From    *User    `json:"from,omitempty"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// File represents a file ready to be downloaded from Telegram.
type File struct {
	FileID       string `json:"file_id,omitempty"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// BotCommand represents a bot command.
type BotCommand struct {
	Command     string `json:"command,omitempty"`
	Description string `json:"description,omitempty"`
}

// ChatMember is a placeholder for future chat member information.
type ChatMember struct {
	Status string `json:"status,omitempty"`
	User   *User  `json:"user,omitempty"`
}

// WebhookInfo is a placeholder for future webhook information.
type WebhookInfo struct {
	URL string `json:"url,omitempty"`
}

// SendOpts contains optional parameters for SendMessage.
type SendOpts struct {
	ParseMode             string                 `json:"parse_mode,omitempty"`
	ReplyMarkup           *InlineKeyboardMarkup  `json:"reply_markup,omitempty"`
	DisableWebPagePreview bool                   `json:"disable_web_page_preview,omitempty"`
	ReplyToMessageID      int                    `json:"reply_to_message_id,omitempty"`
}

// UpdateResponse is the generic Telegram API response for a single update-related request.
type UpdateResponse struct {
	OK          bool              `json:"ok"`
	Result      json.RawMessage   `json:"result,omitempty"`
	Description string            `json:"description,omitempty"`
	ErrorCode   int               `json:"error_code,omitempty"`
}

// UserProfilePhotos contains a set of user profile photos.
type UserProfilePhotos struct {
	TotalCount int          `json:"total_count,omitempty"`
	Photos     [][]PhotoSize `json:"photos,omitempty"`
}

// FileResponse is the Telegram API response for getFile.
type FileResponse struct {
	OK     bool   `json:"ok"`
	Result *File  `json:"result,omitempty"`
}

// GetUpdatesResponse is the Telegram API response for getUpdates.
type GetUpdatesResponse struct {
	OK     bool      `json:"ok"`
	Result []Update  `json:"result,omitempty"`
}

// SendMessageResponse is the Telegram API response for sendMessage.
type SendMessageResponse struct {
	OK     bool     `json:"ok"`
	Result *Message `json:"result,omitempty"`
}
