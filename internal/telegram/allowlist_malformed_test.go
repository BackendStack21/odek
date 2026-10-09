package telegram

import (
	"strings"
	"testing"
)

// A fully malformed ODEK_TELEGRAM_ALLOWED_USERS value must never widen access:
// ConfigFromEnv fails closed with an error naming the variable and entry, and
// the returned config keeps the (narrower) base list.
func TestRED_MalformedUserAllowlistFailsOpen(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_USERS", "12345x")
	t.Setenv("ODEK_TELEGRAM_BOT_TOKEN", "t")
	cfg, err := ConfigFromEnv(TelegramConfig{AllowedChats: []int64{-100}, AllowedUsers: []int64{777}})
	if err == nil || !strings.Contains(err.Error(), "ODEK_TELEGRAM_ALLOWED_USERS") || !strings.Contains(err.Error(), "12345x") {
		t.Fatalf("err = %v, want error naming variable and first bad entry", err)
	}

	h := &Handler{Config: HandlerConfig{AllowedChats: cfg.AllowedChats, AllowedUsers: cfg.AllowedUsers, AllowAllUsers: cfg.AllowAllUsers}}
	if h.isAllowed(-100, 999999) {
		t.Fatalf("stranger 999999 authorised in group after allowlist typo (users=%v)", cfg.AllowedUsers)
	}
}

// A partially valid list replaces the base list with its valid entries, so a
// typo can only narrow access: '111, 222x' on base [111,222,333] allows 111.
func TestRED_PartialMalformedListNarrows(t *testing.T) {
	var buf strings.Builder
	old := warnWriter
	warnWriter = &buf
	defer func() { warnWriter = old }()

	t.Setenv("ODEK_TELEGRAM_ALLOWED_USERS", "111, 222x")
	cfg, err := ConfigFromEnv(TelegramConfig{AllowedUsers: []int64{111, 222, 333}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedUsers) != 1 || cfg.AllowedUsers[0] != 111 {
		t.Fatalf("AllowedUsers = %v, want [111]", cfg.AllowedUsers)
	}
	if !strings.Contains(buf.String(), `"222x"`) {
		t.Fatalf("no warning naming the bad entry: %q", buf.String())
	}
}

func TestConfigFromEnv_EmptyEntriesValueFailsClosed(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_CHATS", " , ")
	if _, err := ConfigFromEnv(TelegramConfig{AllowedChats: []int64{1}}); err == nil {
		t.Fatal("value with no entries must fail closed")
	}
}
