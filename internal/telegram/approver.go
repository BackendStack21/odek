package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/kode/internal/danger"
)

// ── Constants ──────────────────────────────────────────────────────────────

// approvalTimeout is how long the agent blocks waiting for a user response
// via inline keyboard. If the user doesn't respond in time, the operation
// is denied with a timeout error.
const approvalTimeout = 120 * time.Second

// callbackDataPrefixes
const (
	cbPrefixApprove = "apr:"
	cbPrefixDeny    = "den:"
	cbPrefixTrust   = "trs:"
)

// ── TelegramApprover ───────────────────────────────────────────────────────

// TelegramApprover implements danger.Approver by sending approval requests
// via Telegram inline keyboards. The agent loop calls PromptCommand which:
//
//  1. Sends the command details + [Approve] [Deny] [Trust] keyboard
//  2. Blocks on a channel waiting for the user's callback response
//  3. Returns nil on approve/trust, error on deny/timeout
//
// The poller goroutine calls HandleCallback when a callback query arrives.
// The callback data encodes the action and request ID so HandleCallback
// can wake the correct blocked goroutine.
//
// Thread-safe: PromptCommand and HandleCallback are safe to call concurrently.
type TelegramApprover struct {
	bot     *Bot
	pending map[string]chan string // requestID -> response channel
	mu      sync.Mutex
	trusted map[danger.RiskClass]bool

	// ChatID is the Telegram chat where approval prompts are sent.
	ChatID int64

	// OnError logs errors (if nil, errors are silently ignored).
	OnError func(err error)
}

// NewTelegramApprover creates a TelegramApprover for the given chat.
func NewTelegramApprover(bot *Bot, chatID int64) *TelegramApprover {
	return &TelegramApprover{
		bot:     bot,
		ChatID:  chatID,
		pending: make(map[string]chan string),
		trusted: make(map[danger.RiskClass]bool),
	}
}

// PromptCommand sends an approval request with inline keyboard and waits
// for the user to respond. Returns nil on approve/trust, error on deny/timeout.
func (a *TelegramApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	// Check session trust cache
	a.mu.Lock()
	if a.trusted[cls] {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	id := a.newID()

	// Build description text.
	var desc string
	if description != "" {
		desc = description
	} else {
		desc = cmd
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
	}

	// Build the approval message.
	text := fmt.Sprintf(
		"⚠️ *Approval Required*\n\n"+
			"Risk: `%s`\n"+
			"```\n%s\n```",
		cls, desc,
	)

	// Send with inline keyboard.
	markup := InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "✅ Approve", CallbackData: cbPrefixApprove + id},
				{Text: "❌ Deny", CallbackData: cbPrefixDeny + id},
			},
			{
				{Text: "🔒 Trust Session", CallbackData: cbPrefixTrust + id},
			},
		},
	}

	_, err := a.bot.SendMessage(a.ChatID, text, &SendOpts{
		ParseMode:   ParseModeMarkdownV2,
		ReplyMarkup: &markup,
	})
	if err != nil {
		return fmt.Errorf("telegram approver: send prompt: %w", err)
	}

	// Register the pending request.
	resp := make(chan string, 1)
	a.mu.Lock()
	a.pending[id] = resp
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	// Wait for response or timeout.
	select {
	case action := <-resp:
		switch action {
		case "approve":
			return nil
		case "trust":
			a.mu.Lock()
			a.trusted[cls] = true
			a.mu.Unlock()
			// Confirm trust to the user
			if _, err := a.bot.SendMessage(a.ChatID,
				fmt.Sprintf("🔒 Class `%s` trusted for this session.", cls),
				&SendOpts{ParseMode: ParseModeMarkdownV2}); err != nil {
				if a.OnError != nil {
					a.OnError(fmt.Errorf("telegram approver: confirm trust: %w", err))
				}
			}
			return nil
		case "deny":
			return fmt.Errorf("operation denied by user: %s", cmd)
		default:
			return fmt.Errorf("operation denied: %s", cmd)
		}
	case <-time.After(approvalTimeout):
		return fmt.Errorf("approval timeout: %s", cmd)
	}
}

// PromptOperation implements danger.Approver for tool operations.
func (a *TelegramApprover) PromptOperation(op danger.ToolOperation) error {
	desc := fmt.Sprintf("%s (%s)", op.Resource, op.Name)
	return a.PromptCommand(op.Risk, desc, "")
}

// HandleCallback processes a callback query from an inline keyboard approval.
// It parses the callback data, looks up the pending request, and unblocks
// the waiting goroutine. Returns true if the callback was handled (was an
// approval callback), false if it should fall through to OnCallbackQuery.
func (a *TelegramApprover) HandleCallback(data string) bool {
	// Parse callback data: "apr:<id>", "den:<id>", "trs:<id>"
	var action string
	var id string

	switch {
	case strings.HasPrefix(data, cbPrefixApprove):
		action = "approve"
		id = strings.TrimPrefix(data, cbPrefixApprove)
	case strings.HasPrefix(data, cbPrefixDeny):
		action = "deny"
		id = strings.TrimPrefix(data, cbPrefixDeny)
	case strings.HasPrefix(data, cbPrefixTrust):
		action = "trust"
		id = strings.TrimPrefix(data, cbPrefixTrust)
	default:
		return false // not an approval callback
	}

	a.mu.Lock()
	resp, ok := a.pending[id]
	a.mu.Unlock()

	if ok {
		resp <- action
	}

	return true
}

// newID generates a unique request ID.
func (a *TelegramApprover) newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// IsTrusted reports whether the given risk class is already trusted for
// this session. Primarily used for testing.
func (a *TelegramApprover) IsTrusted(cls danger.RiskClass) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trusted[cls]
}

// ResetTrust clears all trusted risk classes. Used by /new command.
func (a *TelegramApprover) ResetTrust() {
	a.mu.Lock()
	a.trusted = make(map[danger.RiskClass]bool)
	a.mu.Unlock()
}

// ── File descriptor check ──────────────────────────────────────────────────

// ensureApproverHasFD is a no-op on Telegram (no TTY needed).
// Kept for interface compatibility — never called directly.
var _ = func() string {
	// Satisfy the os import
	return os.DevNull
}
