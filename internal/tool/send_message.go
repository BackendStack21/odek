// Package tool provides the send_message tool — send intermediate messages,
// files, or interactive prompts to the Telegram chat during an agent run.
package tool

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/telegram"
)

// ── Constants ───────────────────────────────────────────────────────────

// ReservedCallbackPrefixes lists callback-data prefixes that are reserved for
// internal odek UI flows (approval, trust, clarify, skill suggestions). The
// send_message tool rejects buttons using these prefixes so a compromised
// agent cannot forge an approval/skill UI.
var ReservedCallbackPrefixes = []string{
	"apr:",
	"den:",
	"trs:",
	"clarify:",
	"skill_save:",
	"skill_skip:",
}

// IsReservedCallbackPrefix reports whether data starts with a reserved
// internal callback-data prefix.
func IsReservedCallbackPrefix(data string) bool {
	for _, p := range ReservedCallbackPrefixes {
		if strings.HasPrefix(data, p) {
			return true
		}
	}
	return false
}

// ── Types ──────────────────────────────────────────────────────────────

// SendMessageTool lets the agent send arbitrary messages to the Telegram
// chat with optional inline keyboards and file attachments. Unlike the
// final answer (which flows through the handler's SendResponse), this tool
// sends messages immediately mid-task.
type SendMessageTool struct {
	// Sender delivers the message. Called synchronously — returns nil on
	// success or an error if delivery failed. The tool does NOT wait for
	// user response; use clarify for interactive prompts.
	Sender func(text string, file string, buttons [][]map[string]string) error

	// ChatID scopes outbound media path validation to the originating chat.
	// When non-zero, files inside ~/.odek/media must be tagged for this chat.
	ChatID int64
}

// sendMessageArgs is the JSON schema for send_message tool arguments.
type sendMessageArgs struct {
	Text    string                   `json:"text"`
	File    string                   `json:"file,omitempty"`
	Buttons [][]sendMessageButtonArg `json:"buttons,omitempty"`
}

type sendMessageButtonArg struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// ── Tool interface ─────────────────────────────────────────────────────

func (t *SendMessageTool) Name() string { return "send_message" }

func (t *SendMessageTool) Description() string {
	return `Send an intermediate message, file, or interactive prompt to the Telegram chat.
Use this for:
- Multi-step progress updates (before the final answer)
- Sending generated files (images, CSVs, zip archives)
- Asking questions with custom inline keyboard buttons
- Showing notifications or warnings mid-task

For simple Yes/No questions, prefer the clarify tool.
For final answers, just return the text — no need to use send_message.

File type is auto-detected from extension:
  .png/.jpg/.webp → photo   .ogg/.mp3 → voice   everything else → document`
}

func (t *SendMessageTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "Message text (MarkdownV2). Can be empty if only sending a file.",
			},
			"file": map[string]any{
				"type":        "string",
				"description": "Absolute path to a file to send as attachment (photo/document/voice/zip).",
			},
			"buttons": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text": map[string]any{
								"type":        "string",
								"description": "Button label shown to the user.",
							},
							"callback_data": map[string]any{
								"type":        "string",
								"description": "Callback data sent when user clicks. Must start with 'cb:' for agent-routed callbacks. Reserved internal prefixes (apr:, den:, trs:, clarify:, skill_save:, skill_skip:) are rejected.",
							},
						},
						"required": []string{"text", "callback_data"},
					},
				},
				"description": "Inline keyboard button rows. Each inner array is one row of buttons.",
			},
		},
		"required": []string{"text"},
	}
}

// Call sends the message via the injected Sender function.
func (t *SendMessageTool) Call(argsJSON string) (string, error) {
	if t.Sender == nil {
		return "", fmt.Errorf("send_message: Sender not configured (platform must wire it)")
	}

	var args sendMessageArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("send_message: parse args: %w", err)
	}

	// Validate file path if provided. Outbound media is restricted to an
	// allowlist of directories and symlinks are rejected. When a chat ID is
	// available, files inside ~/.odek/media must belong to that chat so one
	// chat cannot exfiltrate another chat's downloads.
	if args.File != "" {
		if !filepath.IsAbs(args.File) {
			return "", fmt.Errorf("send_message: file path must be absolute: %s", args.File)
		}
		var resolved string
		var err error
		if t.ChatID != 0 {
			resolved, err = telegram.ResolveMediaPathForChat(args.File, t.ChatID)
		} else {
			resolved, err = telegram.ResolveMediaPath(args.File)
		}
		if err != nil {
			return "", fmt.Errorf("send_message: file not found or not allowed: %s: %w", args.File, err)
		}
		args.File = resolved
	}

	// Normalise buttons to the expected format.
	buttons := make([][]map[string]string, len(args.Buttons))
	for i, row := range args.Buttons {
		buttons[i] = make([]map[string]string, len(row))
		for j, btn := range row {
			// Validate callback_data prefix convention.
			cd := btn.CallbackData
			if IsReservedCallbackPrefix(cd) {
				return "", fmt.Errorf("send_message: callback_data %q uses reserved internal prefix; only 'cb:' callbacks are allowed", cd)
			}
			if !strings.HasPrefix(cd, "cb:") {
				cd = "cb:" + cd
			}
			buttons[i][j] = map[string]string{
				"text":          btn.Text,
				"callback_data": cd,
			}
		}
	}

	if err := t.Sender(args.Text, args.File, buttons); err != nil {
		return "", fmt.Errorf("send_message: %w", err)
	}

	// Build a short confirmation.
	parts := []string{}
	if args.File != "" {
		parts = append(parts, "file sent")
	}
	if len(args.Buttons) > 0 {
		parts = append(parts, "buttons sent")
	}
	if len(parts) == 0 {
		parts = append(parts, "message sent")
	}
	return "✅ " + strings.Join(parts, ", "), nil
}

// ── Convenience ────────────────────────────────────────────────────────

// NewSendMessageTool creates a SendMessageTool with the given sender function.
// It does not enforce chat-scoped media isolation; prefer
// NewSendMessageToolForChat for Telegram callers that know the chat ID.
func NewSendMessageTool(sender func(text string, file string, buttons [][]map[string]string) error) *SendMessageTool {
	return &SendMessageTool{Sender: sender}
}

// NewSendMessageToolForChat creates a SendMessageTool scoped to chatID.
// Outbound media paths inside ~/.odek/media must be tagged for this chat.
func NewSendMessageToolForChat(chatID int64, sender func(text string, file string, buttons [][]map[string]string) error) *SendMessageTool {
	return &SendMessageTool{Sender: sender, ChatID: chatID}
}
