package config

import (
	"os"
	"path/filepath"
	"testing"
)

// tgHuntWriteGlobalConfig installs global as ~/.odek/config.json in a fresh
// HOME (and cwd) and clears the ODEK_TELEGRAM_* variables this test reads.
func tgHuntWriteGlobalConfig(t *testing.T, global string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	for _, v := range []string{
		"ODEK_TELEGRAM_AGENT_TIMEOUT", "ODEK_TELEGRAM_HEALTH_ADDR",
		"ODEK_TELEGRAM_ALLOW_ALL", "ODEK_TELEGRAM_ALLOWED_CHATS", "ODEK_TELEGRAM_ALLOWED_USERS",
	} {
		t.Setenv(v, "")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".odek"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".odek", "config.json"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
}

// docs/TELEGRAM.md and docs/CONFIG.md document telegram.agent_timeout_seconds
// (ODEK_TELEGRAM_AGENT_TIMEOUT, "0 = unlimited"), telegram.health_addr
// (ODEK_TELEGRAM_HEALTH_ADDR) and telegram.allow_all_users
// (ODEK_TELEGRAM_ALLOW_ALL). resolveTelegram rebuilds the section from
// telegram.DefaultConfig() and copies only a list of fields, and these three
// are not on it: every configured or env value is dropped. Chat turns are
// always cut at 900 s, the health endpoint never starts, and the documented
// open-bot opt-in makes `odek telegram` refuse to start (ValidateConfig sees
// AllowAllUsers=false with no allowlist).
func TestRED_TelegramConfigKeepsAgentTimeoutHealthAddrAllowAll(t *testing.T) {
	tgHuntWriteGlobalConfig(t, `{"telegram":{"bot_token":"t","agent_timeout_seconds":3600,`+
		`"health_addr":"127.0.0.1:9090","allow_all_users":true}}`)
	tg := LoadConfig(CLIFlags{}).Telegram
	if tg.AgentTimeout != 3600 {
		t.Errorf("config agent_timeout_seconds=3600 resolved to %d", tg.AgentTimeout)
	}
	if tg.HealthAddr != "127.0.0.1:9090" {
		t.Errorf("config health_addr resolved to %q", tg.HealthAddr)
	}
	if !tg.AllowAllUsers {
		t.Error("config allow_all_users=true resolved to false")
	}

	// The documented environment overrides are dropped the same way.
	tgHuntWriteGlobalConfig(t, `{"telegram":{"bot_token":"t"}}`)
	t.Setenv("ODEK_TELEGRAM_AGENT_TIMEOUT", "7200")
	t.Setenv("ODEK_TELEGRAM_HEALTH_ADDR", "127.0.0.1:9191")
	t.Setenv("ODEK_TELEGRAM_ALLOW_ALL", "true")
	tg = LoadConfig(CLIFlags{}).Telegram
	if tg.AgentTimeout != 7200 {
		t.Errorf("ODEK_TELEGRAM_AGENT_TIMEOUT=7200 resolved to %d", tg.AgentTimeout)
	}
	if tg.HealthAddr != "127.0.0.1:9191" {
		t.Errorf("ODEK_TELEGRAM_HEALTH_ADDR resolved to %q", tg.HealthAddr)
	}
	if !tg.AllowAllUsers {
		t.Error("ODEK_TELEGRAM_ALLOW_ALL=true resolved to false: the documented open-bot opt-in cannot pass ValidateConfig")
	}
}
