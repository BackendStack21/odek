package telegram

import (
	"errors"
	"strings"
)

// IsEntityParseError reports whether err is the Bot API rejecting a formatted
// message because its markup could not be parsed (a 400 "can't parse
// entities" error). Only this class is worth retrying as plain text: other
// failures (a blocked bot, a missing chat, a rate limit) would just repeat.
func IsEntityParseError(err error) bool {
	var te *TelegramError
	if !errors.As(err, &te) {
		return false
	}
	return (te.Code == 400 || te.Code == 0) && strings.Contains(strings.ToLower(te.Description), "can't parse entities")
}

// StripMarkdown turns MarkdownV2-escaped text into readable plain text: the
// escaping backslashes are removed and unescaped emphasis and code markers are
// dropped, so a plain-text retry does not show literal "\." or "\*".
func StripMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '*', '_', '`', '~':
			// markup marker: dropped
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// SendWithPlainFallback sends text with opts and, when Telegram rejects the
// markup (IsEntityParseError), retries once as plain text with the markup
// stripped. Any other error is returned as is, without a second send.
func (b *Bot) SendWithPlainFallback(chatID int64, text string, opts *SendOpts) (*Message, error) {
	msg, err := b.SendMessage(chatID, text, opts)
	if err == nil || opts == nil || opts.ParseMode == "" || !IsEntityParseError(err) {
		return msg, err
	}
	plain := *opts
	plain.ParseMode = ""
	return b.SendMessage(chatID, StripMarkdown(text), &plain)
}
