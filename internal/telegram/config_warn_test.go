package telegram

import (
	"strings"
	"testing"
)

// A partially malformed allowlist variable keeps its valid entries and warns.
func TestRED_MalformedAllowlistWarns(t *testing.T) {
	var buf strings.Builder
	old := warnWriter
	warnWriter = &buf
	defer func() { warnWriter = old }()

	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "123,abc")
	base := TelegramConfig{AllowedChats: []int64{1}, AllowedUsers: []int64{2}}
	cfg, err := ConfigFromEnv(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedChats) != 1 || cfg.AllowedChats[0] != 123 {
		t.Fatalf("valid entries not applied: %v", cfg.AllowedChats)
	}
	out := buf.String()
	if !strings.Contains(out, "ODEK_TELEGRAM_ALLOWED_CHATS") || !strings.Contains(out, `"abc"`) {
		t.Fatalf("no warning naming variable and entry: %q", out)
	}
}

func TestAllowlistValidValueDoesNotWarn(t *testing.T) {
	var buf strings.Builder
	old := warnWriter
	warnWriter = &buf
	defer func() { warnWriter = old }()
	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", "5, 6")
	cfg, err := ConfigFromEnv(TelegramConfig{})
	if err != nil || len(cfg.AllowedChats) != 2 || buf.Len() != 0 {
		t.Fatalf("cfg=%v err=%v warn=%q", cfg.AllowedChats, err, buf.String())
	}
}
