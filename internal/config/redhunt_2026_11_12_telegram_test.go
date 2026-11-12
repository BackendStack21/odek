package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BackendStack21/odek/internal/telegram"
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

// Every field of the telegram section must survive resolveTelegram, so a
// field added later cannot be dropped silently.
func TestResolveTelegramKeepsEveryField(t *testing.T) {
	var in telegram.TelegramConfig
	v := reflect.ValueOf(&in).Elem()
	// Retired fields: runtime logging is configured elsewhere and these
	// legacy settings are intentionally ignored.
	retired := map[string]bool{"LogLevel": true, "LogFile": true}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if retired[v.Type().Field(i).Name] {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("x" + v.Type().Field(i).Name)
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(7 + i))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			switch f.Type().Elem().Kind() {
			case reflect.Int64:
				f.Set(reflect.ValueOf([]int64{int64(i + 1)}))
			case reflect.String:
				f.Set(reflect.ValueOf([]string{"u" + v.Type().Field(i).Name}))
			default:
				t.Fatalf("field %s: unsupported slice kind", v.Type().Field(i).Name)
			}
		default:
			t.Fatalf("field %s: unsupported kind %s", v.Type().Field(i).Name, f.Kind())
		}
	}
	out := resolveTelegram(&in)
	ov := reflect.ValueOf(out)
	{
		for i := 0; i < v.NumField(); i++ {
			if !retired[v.Type().Field(i).Name] && !reflect.DeepEqual(v.Field(i).Interface(), ov.Field(i).Interface()) {
				t.Errorf("telegram field %s dropped by resolveTelegram: in=%v out=%v", v.Type().Field(i).Name, v.Field(i).Interface(), ov.Field(i).Interface())
			}
		}
	}
}

// An omitted agent_timeout_seconds keeps the default; an explicit 0 means
// unlimited; the project config cannot set the section at all.
func TestTelegramAgentTimeoutPresenceSemantics(t *testing.T) {
	tgHuntWriteGlobalConfig(t, `{"telegram":{"bot_token":"t"}}`)
	if got := LoadConfig(CLIFlags{}).Telegram.AgentTimeout; got != 900 {
		t.Errorf("omitted agent_timeout_seconds resolved to %d, want 900", got)
	}
	tgHuntWriteGlobalConfig(t, `{"telegram":{"bot_token":"t","agent_timeout_seconds":0}}`)
	if got := LoadConfig(CLIFlags{}).Telegram.AgentTimeout; got != 0 {
		t.Errorf("explicit 0 resolved to %d, want 0 (unlimited)", got)
	}
	tgHuntWriteGlobalConfig(t, `{"telegram":{"bot_token":"t"}}`)
	if err := os.WriteFile("odek.json", []byte(`{"telegram":{"bot_token":"evil","agent_timeout_seconds":0,"allow_all_users":true,"health_addr":"0.0.0.0:1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tg := LoadConfig(CLIFlags{}).Telegram
	if tg.AllowAllUsers || tg.HealthAddr != "" || tg.AgentTimeout != 900 || tg.Token != "t" {
		t.Errorf("project odek.json influenced the telegram section: %+v", tg)
	}
}
