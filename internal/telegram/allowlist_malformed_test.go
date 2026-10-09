package telegram

import "testing"

// A malformed ODEK_TELEGRAM_ALLOWED_USERS entry must never widen access.
// Dropping an unparsable item silently could leave an empty user list, which
// means "no user filter" and authorises every member of an allowed chat.
func TestRED_MalformedUserAllowlistFailsOpen(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_USERS", "12345x")
	t.Setenv("ODEK_TELEGRAM_BOT_TOKEN", "t")
	cfg := ConfigFromEnv(TelegramConfig{AllowedChats: []int64{-100}, AllowedUsers: []int64{777}})

	h := &Handler{Config: HandlerConfig{AllowedChats: cfg.AllowedChats, AllowedUsers: cfg.AllowedUsers, AllowAllUsers: cfg.AllowAllUsers}}
	if h.isAllowed(-100, 999999) {
		t.Fatalf("stranger 999999 authorised in group after allowlist typo (users=%v)", cfg.AllowedUsers)
	}
}

// A partially malformed list is rejected as a whole: the base list stays.
func TestConfigFromEnv_MalformedListKeepsBase(t *testing.T) {
	t.Setenv("ODEK_TELEGRAM_ALLOWED_USERS", "1,2,oops")
	cfg := ConfigFromEnv(TelegramConfig{AllowedUsers: []int64{777}})
	if len(cfg.AllowedUsers) != 1 || cfg.AllowedUsers[0] != 777 {
		t.Fatalf("AllowedUsers = %v, want base [777]", cfg.AllowedUsers)
	}
}
