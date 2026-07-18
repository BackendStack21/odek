package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
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

// pendingRequest holds the response channel and the message ID for a
// pending approval request, so HandleCallback can edit the message
// text and remove the inline keyboard after the user responds.
type pendingRequest struct {
	resp       chan string
	messageID  int
	userID     int64 // originating user; 0 means unknown (legacy allow-all)
	class      danger.RiskClass
	allowTrust bool
}

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
	bot      *Bot
	pending  map[string]*pendingRequest // requestID -> pending request
	mu       sync.Mutex
	trusted  map[danger.RiskClass]bool
	trustAll bool // when true, all PromptCommand calls auto-approve
	log      Logger
	cancel   chan struct{} // closed by Cancel() to interrupt waiting PromptCommand

	// ChatID is the Telegram chat where approval prompts are sent.
	ChatID int64

	// userID is the originating Telegram user whose approval requests this
	// approver will accept. Callbacks from other users are rejected to prevent
	// group-chat approval hijacking. Zero means unknown (legacy allow-all).
	userID int64
}

// NewTelegramApprover creates a TelegramApprover for the given chat and
// originating user. Callbacks are only accepted from userID; use 0 to allow
// callbacks from any user (legacy behavior, not recommended for groups).
func NewTelegramApprover(bot *Bot, chatID, userID int64) *TelegramApprover {
	return &TelegramApprover{
		bot:     bot,
		ChatID:  chatID,
		userID:  userID,
		pending: make(map[string]*pendingRequest),
		trusted: make(map[danger.RiskClass]bool),
		log:     NewNopLogger(),
		cancel:  make(chan struct{}),
	}
}

// SetLogger sets the logger for this approver. If nil, a NopLogger is used.
func (a *TelegramApprover) SetLogger(l Logger) {
	if l == nil {
		a.log = NewNopLogger()
		return
	}
	a.log = l
}

// Cancel interrupts any pending PromptCommand by closing the cancel channel.
// Safe to call multiple times — subsequent calls are no-ops.
func (a *TelegramApprover) Cancel() {
	select {
	case <-a.cancel:
		// Already closed.
	default:
		close(a.cancel)
	}
}

// PromptCommand sends an approval request with inline keyboard and waits
// for the user to respond. Returns nil on approve/trust, error on deny/timeout.
// allowTrustForClass mirrors the TTY/Web approver policy: the highest-impact
// classes must never be session-trusted, and the synthetic `tool_batch` class
// must not be trusted because a single batch approval could hide multiple
// unrelated dangerous tools.
func allowTrustForClass(cls danger.RiskClass) bool {
	return cls != danger.Destructive && cls != danger.Blocked && cls != danger.Unknown && cls != "tool_batch"
}

func (a *TelegramApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	// Check session trust cache
	a.mu.Lock()
	if a.trusted[cls] || a.trustAll {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	id := a.newID()
	allowTrust := allowTrustForClass(cls)

	// Build the approval message — the full command is always shown so the
	// user can make an informed decision.
	text := buildApprovalText(cls, cmd, description)

	// Send with inline keyboard. Hide the Trust Session button for classes
	// that must be approved per-call.
	var keyboard [][]InlineKeyboardButton
	if allowTrust {
		keyboard = [][]InlineKeyboardButton{
			{
				{Text: "✅ Approve", CallbackData: cbPrefixApprove + id},
				{Text: "❌ Deny", CallbackData: cbPrefixDeny + id},
			},
			{
				{Text: "🔒 Trust Session", CallbackData: cbPrefixTrust + id},
			},
		}
	} else {
		keyboard = [][]InlineKeyboardButton{
			{
				{Text: "✅ Approve", CallbackData: cbPrefixApprove + id},
				{Text: "❌ Deny", CallbackData: cbPrefixDeny + id},
			},
		}
	}
	markup := InlineKeyboardMarkup{InlineKeyboard: keyboard}

	msg, err := a.bot.SendMessage(a.ChatID, text, &SendOpts{
		ParseMode:   ParseModeMarkdownV2,
		ReplyMarkup: &markup,
	})
	if err != nil {
		return fmt.Errorf("telegram approver: send prompt: %w", err)
	}

	// Register the pending request with message ID and originating user.
	pr := &pendingRequest{resp: make(chan string, 1), messageID: msg.ID, userID: a.userID, class: cls, allowTrust: allowTrust}
	a.mu.Lock()
	a.pending[id] = pr
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	// Wait for response, cancellation, or timeout.
	select {
	case action := <-pr.resp:
		// Edit the approval message to remove buttons and show the user's decision.
		answerText := ""
		switch action {
		case "approve":
			answerText = "✅ *Approved*"
		case "trust":
			answerText = fmt.Sprintf("🔒 *Trusted:* `%s`", cls)
		case "deny":
			answerText = "❌ *Denied*"
		default:
			answerText = "⏭ *Skipped*"
		}
		if answerText != "" {
			a.bot.EditMessageText(a.ChatID, pr.messageID, answerText,
				&SendOpts{ParseMode: ParseModeMarkdownV2, ReplyMarkup: &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}})
		}

		switch action {
		case "approve":
			return nil
		case "trust":
			if !allowTrust {
				return fmt.Errorf("operation denied: trust-session not available for %s", cls)
			}
			a.mu.Lock()
			a.trusted[cls] = true
			a.mu.Unlock()
			return nil
		case "deny":
			return fmt.Errorf("operation denied by user: %s", cmd)
		default:
			return fmt.Errorf("operation denied: %s", cmd)
		}
	case <-a.cancel:
		return fmt.Errorf("approval cancelled: %s", cmd)
	case <-time.After(approvalTimeout):
		return fmt.Errorf("approval timeout: %s", cmd)
	}
}

// PromptOperation implements danger.Approver for tool operations.
func (a *TelegramApprover) PromptOperation(op danger.ToolOperation) error {
	desc := fmt.Sprintf("%s (%s)", op.Resource, op.Name)
	return a.PromptCommand(op.Risk, desc, "")
}

// PromptMedia asks the user to approve an outbound Telegram media upload.
// The file path is shown in full and, when the bot was launched from a broad
// base such as $HOME or /, an explicit warning is added to the prompt.
func (a *TelegramApprover) PromptMedia(path string) error {
	desc := "Outbound Telegram media upload"
	if w := BroadBaseWarning(); w != "" {
		desc += "\n⚠️ " + w
	}
	return a.PromptCommand(danger.NetworkEgress, path, desc)
}

// HandleCallback processes a callback query from an inline keyboard approval.
// It parses the callback data, looks up the pending request, and unblocks
// the waiting goroutine. Callbacks are only accepted from the originating
// user (or any user if userID is unknown/0). Returns true if the callback
// was handled (was an approval callback), false if it should fall through to
// OnCallbackQuery.
func (a *TelegramApprover) HandleCallback(data string, userID int64) bool {
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
	pr, ok := a.pending[id]
	a.mu.Unlock()

	if ok {
		// Reject callbacks from users other than the one who initiated the
		// operation, unless no originating user was recorded (userID == 0).
		if pr.userID != 0 && pr.userID != userID {
			return true
		}
		pr.resp <- action
	}

	return true
}

// telegramMaxMsgLen is Telegram's hard per-message character limit. The
// approval text must fit within it or the send fails.
const telegramMaxMsgLen = 4096

// buildApprovalText renders the approval prompt. The full shell command is
// ALWAYS shown inside a fenced code block — earlier versions hid the command
// entirely whenever a model-supplied description was present, or silently cut
// it at 200 bytes, both of which left the user approving a command they could
// not fully see. The model description (when present) is shown as a separate
// labelled line so it complements rather than replaces the command.
//
// The command is only ever truncated when the whole message would otherwise
// exceed telegramMaxMsgLen, and when it is, the cut is explicit ("… [truncated]")
// and made on a rune boundary.
func buildApprovalText(cls danger.RiskClass, cmd, description string) string {
	var b strings.Builder
	b.WriteString("⚠️ *Approval Required*\n\n")
	fmt.Fprintf(&b, "Risk: `%s`\n", escapeCodeBlock(string(cls)))
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString("Why: " + EscapeMarkdown(d) + "\n")
	}

	// Reserve room for the fixed parts so the command body can be budgeted
	// against Telegram's hard limit. The fences and a possible truncation
	// marker are accounted for here.
	const openFence = "```\n"
	const closeFence = "\n```"
	overhead := b.Len() + len(openFence) + len(closeFence)

	body := escapeCodeBlock(cmd)
	if budget := telegramMaxMsgLen - overhead; len(body) > budget {
		const marker = "\n… [truncated]"
		// truncateRunes clamps a negative budget to an empty string.
		body = truncateRunes(body, budget-len(marker))
		// A dangling backslash left by truncation would escape the closing
		// fence and corrupt the whole message — strip any trailing run.
		body = strings.TrimRight(body, `\`) + marker
	}

	b.WriteString(openFence)
	b.WriteString(body)
	b.WriteString(closeFence)
	return b.String()
}

// escapeCodeBlock escapes the two characters that are special inside a
// Telegram MarkdownV2 code block: backslash and backtick. Without this a
// command containing ``` or a literal backslash would close the block early
// and mangle the rendered command. EscapeMarkdown deliberately leaves code
// spans untouched, so it cannot be used for content destined for a fence.
func escapeCodeBlock(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// truncateRunes returns s shortened to at most maxBytes bytes without
// splitting a multi-byte UTF-8 rune.
func truncateRunes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
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

// SetTrustAll enables or disables blanket trust for all risk classes.
// When enabled, PromptCommand returns nil for every call without prompting.
func (a *TelegramApprover) SetTrustAll(enabled bool) {
	a.mu.Lock()
	a.trustAll = enabled
	a.mu.Unlock()
}
