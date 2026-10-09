package telegram

import (
	"strings"
	"testing"
)

// A malformed allowlist variable keeps the base list and says so.
func TestRED_MalformedAllowlistWarns(t *testing.T) {
	var buf strings.Builder
	old := warnWriter
	warnWriter = &buf
	defer func() { warnWriter = old }()

	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "123,abc")
	t.Setenv("ODEK_TELEGRAM_ALLOWED_USERS", ",")
	base := TelegramConfig{AllowedChats: []int64{1}, AllowedUsers: []int64{2}}
	cfg := ConfigFromEnv(base)
	if len(cfg.AllowedChats) != 1 || cfg.AllowedChats[0] != 1 {
		t.Fatalf("base chats not kept: %v", cfg.AllowedChats)
	}
	out := buf.String()
	if !strings.Contains(out, "ODEK_TELEGRAM_ALLOWED_CHATS") || !strings.Contains(out, `"abc"`) {
		t.Fatalf("no warning naming variable and entry: %q", out)
	}
	if !strings.Contains(out, "ODEK_TELEGRAM_ALLOWED_USERS") {
		t.Fatalf("empty list not reported: %q", out)
	}
}

func TestAllowlistValidValueDoesNotWarn(t *testing.T) {
	var buf strings.Builder
	old := warnWriter
	warnWriter = &buf
	defer func() { warnWriter = old }()
	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "5, 6")
	cfg := ConfigFromEnv(TelegramConfig{})
	if len(cfg.AllowedChats) != 2 || buf.Len() != 0 {
		t.Fatalf("cfg=%v warn=%q", cfg.AllowedChats, buf.String())
	}
}
