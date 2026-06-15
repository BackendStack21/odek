package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

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

func TestResolveSchedules_DangerousDefaults(t *testing.T) {
	got := resolveSchedules(nil)
	if got.Dangerous.Classes != nil {
		t.Errorf("Dangerous.Classes should be nil by default, got %+v", got.Dangerous.Classes)
	}
}

func TestResolveSchedules_DangerousFromFile(t *testing.T) {
	got := resolveSchedules(&SchedulesConfig{
		Dangerous: &danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{
				danger.NetworkEgress: danger.Allow,
				danger.SystemWrite:   danger.Allow,
			},
			Allowlist: []string{"/usr/bin/curl"},
		},
	})
	if got.Dangerous.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("network_egress should be allow, got %s", got.Dangerous.Classes[danger.NetworkEgress])
	}
	if got.Dangerous.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("system_write should be allow, got %s", got.Dangerous.Classes[danger.SystemWrite])
	}
	if len(got.Dangerous.Allowlist) != 1 || got.Dangerous.Allowlist[0] != "/usr/bin/curl" {
		t.Errorf("allowlist not preserved: %v", got.Dangerous.Allowlist)
	}
}

func TestLoadConfig_SchedulesDangerousEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_CLASSES", `{"network_egress":"allow","system_write":"allow"}`)
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_ALLOWLIST", "curl, wget")
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_DENYLIST", "rm -rf /")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.Dangerous.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("network_egress should be allow, got %s", cfg.Schedules.Dangerous.Classes[danger.NetworkEgress])
	}
	if cfg.Schedules.Dangerous.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("system_write should be allow, got %s", cfg.Schedules.Dangerous.Classes[danger.SystemWrite])
	}
	if len(cfg.Schedules.Dangerous.Allowlist) != 2 {
		t.Errorf("allowlist = %v, want 2 entries", cfg.Schedules.Dangerous.Allowlist)
	}
	if len(cfg.Schedules.Dangerous.Denylist) != 1 || cfg.Schedules.Dangerous.Denylist[0] != "rm -rf /" {
		t.Errorf("denylist = %v", cfg.Schedules.Dangerous.Denylist)
	}
}

func TestLoadConfig_SchedulesDangerousEnvMergesWithFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalPath := filepath.Join(home, ".odek", "config.json")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := []byte(`{"schedules":{"dangerous":{"classes":{"system_write":"allow"},"allowlist":["top"]}}}`)
	if err := os.WriteFile(globalPath, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_CLASSES", `{"network_egress":"allow"}`)
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_ALLOWLIST", "bottom")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.Dangerous.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("file system_write override lost: %s", cfg.Schedules.Dangerous.Classes[danger.SystemWrite])
	}
	if cfg.Schedules.Dangerous.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("env network_egress override lost: %s", cfg.Schedules.Dangerous.Classes[danger.NetworkEgress])
	}
	if len(cfg.Schedules.Dangerous.Allowlist) != 2 || cfg.Schedules.Dangerous.Allowlist[0] != "top" || cfg.Schedules.Dangerous.Allowlist[1] != "bottom" {
		t.Errorf("allowlist not merged: %v", cfg.Schedules.Dangerous.Allowlist)
	}
}

func TestLoadConfig_SchedulesDangerousProjectIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	projectPath := filepath.Join(wd, "odek.json")
	data := []byte(`{"schedules":{"dangerous":{"classes":{"network_egress":"allow"}}}}`)
	if err := os.WriteFile(projectPath, data, 0600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.Dangerous.Classes[danger.NetworkEgress] != "" {
		t.Errorf("project schedules.dangerous should be ignored, got %s", cfg.Schedules.Dangerous.Classes[danger.NetworkEgress])
	}
}

func TestLoadConfig_SchedulesDangerousEnvInvalidJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_CLASSES", "not-json")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.Dangerous.Classes != nil {
		t.Errorf("invalid classes JSON should be ignored, got %+v", cfg.Schedules.Dangerous.Classes)
	}
}

func TestLoadConfig_SchedulesDangerousEnvOnlyAction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_ACTION", "allow")
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_NON_INTERACTIVE", "prompt")
	cfg := LoadConfig(CLIFlags{})
	if cfg.Schedules.Dangerous.DefaultAction == nil || *cfg.Schedules.Dangerous.DefaultAction != "allow" {
		t.Errorf("default action not set from env")
	}
	if cfg.Schedules.Dangerous.NonInteractive == nil || *cfg.Schedules.Dangerous.NonInteractive != "prompt" {
		t.Errorf("non_interactive not set from env")
	}
}

func TestLoadConfig_SchedulesDangerousEnvEmptyList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_SCHEDULES_DANGEROUS_ALLOWLIST", " , , ")
	cfg := LoadConfig(CLIFlags{})
	if len(cfg.Schedules.Dangerous.Allowlist) != 0 {
		t.Errorf("empty allowlist entries should be dropped, got %v", cfg.Schedules.Dangerous.Allowlist)
	}
}

func TestMergeDangerousConfig(t *testing.T) {
	base := &danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Prompt,
		},
		Allowlist: []string{"base-allow"},
		Denylist:  []string{"base-deny"},
	}
	override := &danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.NetworkEgress: danger.Allow,
			danger.SystemWrite:   danger.Allow,
		},
		Allowlist:      []string{"override-allow"},
		Denylist:       []string{"override-deny"},
		DefaultAction:  strPtr("allow"),
		NonInteractive: strPtr("deny"),
	}
	mergeDangerousConfig(base, override)

	if base.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("network_egress not merged")
	}
	if base.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("system_write not overridden")
	}
	if len(base.Allowlist) != 2 {
		t.Errorf("allowlist not appended")
	}
	if *base.DefaultAction != "allow" {
		t.Errorf("default action not overridden")
	}
	if *base.NonInteractive != "deny" {
		t.Errorf("non_interactive not overridden")
	}
}

func TestMergeDangerousConfig_NilBaseClasses(t *testing.T) {
	base := &danger.DangerousConfig{}
	override := &danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.NetworkEgress: danger.Allow,
		},
	}
	mergeDangerousConfig(base, override)
	if base.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("classes not initialized when base.Classes is nil")
	}
}

func strPtr(s string) *string { return &s }
