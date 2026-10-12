package telegram

import (
	"context"
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
	inSpan, inPre := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSpan || inPre {
			// Inside code MarkdownV2 escapes only '\\' and '`'; every other
			// byte is literal content and must survive.
			switch {
			case c == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '`'):
				i++
				b.WriteByte(s[i])
			case c == '`' && inPre:
				if strings.HasPrefix(s[i:], "```") {
					inPre = false
					i += 2
				} else {
					b.WriteByte(c)
				}
			case c == '`':
				inSpan = false
			default:
				b.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '`':
			if strings.HasPrefix(s[i:], "```") {
				inPre = true
				i += 2
			} else {
				inSpan = true
			}
		case '*', '_', '~':
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
	return b.SendWithPlainFallbackContext(context.Background(), chatID, text, opts)
}

// SendWithPlainFallbackContext is SendWithPlainFallback that aborts the sends
// (and their retry backoff) when ctx is cancelled.
func (b *Bot) SendWithPlainFallbackContext(ctx context.Context, chatID int64, text string, opts *SendOpts) (*Message, error) {
	msg, err := b.SendMessageContext(ctx, chatID, text, opts)
	if err == nil || opts == nil || opts.ParseMode == "" || !IsEntityParseError(err) {
		return msg, err
	}
	plain := *opts
	plain.ParseMode = ""
	return b.SendMessageContext(ctx, chatID, StripMarkdown(text), &plain)
}
