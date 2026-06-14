package config

import "testing"

func TestResolveSchedules_Defaults(t *testing.T) {
	got := resolveSchedules(nil)
	if !got.Enabled {
		t.Error("Enabled should default to true")
	}
	if got.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", got.MaxConcurrent)
	}
	if got.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", got.Timezone)
	}
	if got.Catchup {
		t.Error("Catchup should default to false")
	}
	if !got.AllowTelegramManagement {
		t.Error("AllowTelegramManagement should default to true")
	}
}

func TestResolveSchedules_AllowTelegramManagementOverride(t *testing.T) {
	got := resolveSchedules(&SchedulesConfig{AllowTelegramManagement: boolPtr(false)})
	if got.AllowTelegramManagement {
		t.Error("AllowTelegramManagement should be overridable to false")
	}
	// Unrelated defaults are preserved.
	if !got.Enabled || got.MaxConcurrent != 2 {
		t.Errorf("override disturbed defaults: %+v", got)
	}
}

func TestLoadConfig_SchedulesAllowTelegramManagementEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT", "false")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.AllowTelegramManagement {
		t.Error("ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT=false should disable in-chat management")
	}
}

func TestResolveSchedules_Overrides(t *testing.T) {
	got := resolveSchedules(&SchedulesConfig{
		Enabled:       boolPtr(false),
		MaxConcurrent: 5,
		Timezone:      "Europe/Berlin",
		Catchup:       boolPtr(true),
	})
	if got.Enabled {
		t.Error("Enabled should be false")
	}
	if got.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent = %d, want 5", got.MaxConcurrent)
	}
	if got.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q", got.Timezone)
	}
	if !got.Catchup {
		t.Error("Catchup should be true")
	}
}

func TestResolveSchedules_PartialKeepsDefaults(t *testing.T) {
	// Only MaxConcurrent set; the rest keep defaults.
	got := resolveSchedules(&SchedulesConfig{MaxConcurrent: 8})
	if !got.Enabled || got.Timezone != "UTC" || got.Catchup {
		t.Errorf("partial override disturbed defaults: %+v", got)
	}
	if got.MaxConcurrent != 8 {
		t.Errorf("MaxConcurrent = %d, want 8", got.MaxConcurrent)
	}
}

func TestLoadConfig_SchedulesDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadConfig(CLIFlags{})
	if !cfg.Schedules.Enabled {
		t.Error("Schedules.Enabled should default to true")
	}
	if cfg.Schedules.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", cfg.Schedules.MaxConcurrent)
	}
}

func TestLoadConfig_SchedulesEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_SCHEDULES_ENABLED", "false")
	t.Setenv("ODEK_SCHEDULES_MAX_CONCURRENT", "4")
	t.Setenv("ODEK_SCHEDULES_TIMEZONE", "Europe/Berlin")
	t.Setenv("ODEK_SCHEDULES_CATCHUP", "true")
	t.Setenv("ODEK_SCHEDULES_TELEGRAM_ADMIN_CHATS", "123, 456")
	t.Setenv("ODEK_SCHEDULES_TELEGRAM_ADMIN_USERS", "789")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.Enabled {
		t.Error("ODEK_SCHEDULES_ENABLED=false should disable")
	}
	if cfg.Schedules.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want 4", cfg.Schedules.MaxConcurrent)
	}
	if cfg.Schedules.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q, want Europe/Berlin", cfg.Schedules.Timezone)
	}
	if !cfg.Schedules.Catchup {
		t.Error("ODEK_SCHEDULES_CATCHUP=true should enable catchup")
	}
	if len(cfg.Schedules.TelegramAdminChats) != 2 || cfg.Schedules.TelegramAdminChats[0] != 123 || cfg.Schedules.TelegramAdminChats[1] != 456 {
		t.Errorf("TelegramAdminChats = %v, want [123 456]", cfg.Schedules.TelegramAdminChats)
	}
	if len(cfg.Schedules.TelegramAdminUsers) != 1 || cfg.Schedules.TelegramAdminUsers[0] != 789 {
		t.Errorf("TelegramAdminUsers = %v, want [789]", cfg.Schedules.TelegramAdminUsers)
	}
}

func TestLoadConfig_SchedulesAdminFallbackToDefaultChatID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_TELEGRAM_DEFAULT_CHAT_ID", "424242")
	cfg := LoadConfig(CLIFlags{})
	if !cfg.Schedules.AllowTelegramManagement {
		t.Error("AllowTelegramManagement should default to true")
	}
	if len(cfg.Schedules.TelegramAdminChats) != 1 || cfg.Schedules.TelegramAdminChats[0] != 424242 {
		t.Errorf("admin chats should fall back to default_chat_id, got %v", cfg.Schedules.TelegramAdminChats)
	}
}
