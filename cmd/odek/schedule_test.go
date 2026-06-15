package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/telegram"
)

func TestParseDeliver(t *testing.T) {
	tests := []struct {
		in       string
		wantKind string
		wantChat int64
		wantErr  bool
	}{
		{"", schedule.DeliverStdout, 0, false},
		{"stdout", schedule.DeliverStdout, 0, false},
		{"log", schedule.DeliverLog, 0, false},
		{"telegram", schedule.DeliverTelegram, 0, false},
		{"telegram:12345", schedule.DeliverTelegram, 12345, false},
		{"telegram:-100999", schedule.DeliverTelegram, -100999, false},
		{"telegram:notanid", "", 0, true},
		{"smoke-signal", "", 0, true},
	}
	for _, tc := range tests {
		got, err := parseDeliver(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseDeliver(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if got.Kind != tc.wantKind || got.ChatID != tc.wantChat {
			t.Errorf("parseDeliver(%q) = %+v, want kind=%s chat=%d", tc.in, got, tc.wantKind, tc.wantChat)
		}
	}
}

func TestDeliverString(t *testing.T) {
	cases := map[schedule.Delivery]string{
		{Kind: schedule.DeliverStdout}:                   "stdout",
		{Kind: schedule.DeliverLog}:                      "log",
		{Kind: schedule.DeliverTelegram}:                 "telegram",
		{Kind: schedule.DeliverTelegram, ChatID: 42}:     "telegram:42",
		{Kind: schedule.DeliverTelegram, ChatID: -10042}: "telegram:-10042",
	}
	for d, want := range cases {
		if got := deliverString(d); got != want {
			t.Errorf("deliverString(%+v) = %q, want %q", d, got, want)
		}
	}
}

func TestFirstWords(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"one two three four", 2, "one two"},
		{"short", 6, "short"},
		{"  extra   spaces   here ", 2, "extra spaces"},
		{"", 3, ""},
	}
	for _, tc := range tests {
		if got := firstWords(tc.in, tc.n); got != tc.want {
			t.Errorf("firstWords(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestJobSchedule(t *testing.T) {
	// Valid, default UTC.
	if _, err := jobSchedule(schedule.Job{Cron: "0 9 * * *"}); err != nil {
		t.Errorf("valid job: unexpected error %v", err)
	}
	// Valid with timezone.
	if _, err := jobSchedule(schedule.Job{Cron: "0 9 * * *", Timezone: "Europe/Berlin"}); err != nil {
		t.Errorf("tz job: unexpected error %v", err)
	}
	// Bad timezone.
	if _, err := jobSchedule(schedule.Job{Cron: "0 9 * * *", Timezone: "Mars/Phobos"}); err == nil {
		t.Error("bad timezone should error")
	}
	// Bad cron.
	if _, err := jobSchedule(schedule.Job{Cron: "nope"}); err == nil {
		t.Error("bad cron should error")
	}
}

func TestCliDeliverer_Log(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := cliDeliverer{resolved: config.ResolvedConfig{}}
	job := schedule.Job{ID: "jb-1", Name: "logjob", Deliver: schedule.Delivery{Kind: schedule.DeliverLog}}
	if err := d.Deliver(context.Background(), job, "hello from cron"); err != nil {
		t.Fatalf("Deliver(log): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".odek", "schedule.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello from cron") || !strings.Contains(string(data), "jb-1") {
		t.Errorf("log missing content: %q", string(data))
	}
}

func TestCliDeliverer_TelegramErrors(t *testing.T) {
	// No token configured → error.
	d := cliDeliverer{resolved: config.ResolvedConfig{}}
	job := schedule.Job{Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram}}
	if err := d.Deliver(context.Background(), job, "x"); err == nil {
		t.Error("expected error when telegram token is unset")
	}

	// Token set but no chat id anywhere → error.
	d = cliDeliverer{resolved: config.ResolvedConfig{Telegram: telegram.TelegramConfig{Token: "t"}}}
	if err := d.Deliver(context.Background(), job, "x"); err == nil {
		t.Error("expected error when no chat id is resolvable")
	}
}

func TestCliDeliverer_UnknownKind(t *testing.T) {
	d := cliDeliverer{resolved: config.ResolvedConfig{}}
	job := schedule.Job{Deliver: schedule.Delivery{Kind: "pigeon"}}
	if err := d.Deliver(context.Background(), job, "x"); err == nil {
		t.Error("unknown delivery kind should error")
	}
}

// ── embedded (bot) deliverer ────────────────────────────────────────────

func TestTelegramDeliverer_SendsViaLiveBot(t *testing.T) {
	bot, msgCh := newRecordingTestBot(t)
	d := telegramDeliverer{bot: bot, fallback: cliDeliverer{resolved: config.ResolvedConfig{}}}
	job := schedule.Job{Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram, ChatID: 555}}
	if err := d.Deliver(context.Background(), job, "scheduled hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case got := <-msgCh:
		if got != "scheduled hello" {
			t.Errorf("sent %q, want %q", got, "scheduled hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bot did not receive a sendMessage")
	}
}

func TestTelegramDeliverer_UsesDefaultChatID(t *testing.T) {
	bot, msgCh := newRecordingTestBot(t)
	d := telegramDeliverer{
		bot:      bot,
		fallback: cliDeliverer{resolved: config.ResolvedConfig{Telegram: telegram.TelegramConfig{DefaultChatID: 999}}},
	}
	// No per-job chat ID → falls back to default_chat_id.
	job := schedule.Job{Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram}}
	if err := d.Deliver(context.Background(), job, "to default"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case <-msgCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bot did not receive a sendMessage to the default chat")
	}
}

func TestTelegramDeliverer_NoChatErrors(t *testing.T) {
	bot, _ := newRecordingTestBot(t)
	d := telegramDeliverer{bot: bot, fallback: cliDeliverer{resolved: config.ResolvedConfig{}}}
	job := schedule.Job{Deliver: schedule.Delivery{Kind: schedule.DeliverTelegram}}
	if err := d.Deliver(context.Background(), job, "x"); err == nil {
		t.Error("telegram delivery with no chat id should error")
	}
}

func TestTelegramDeliverer_FallsBackForLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Non-telegram kinds route to the CLI deliverer; the bot is untouched.
	d := telegramDeliverer{bot: nil, fallback: cliDeliverer{resolved: config.ResolvedConfig{}}}
	job := schedule.Job{ID: "jb-x", Name: "logjob", Deliver: schedule.Delivery{Kind: schedule.DeliverLog}}
	if err := d.Deliver(context.Background(), job, "logged via fallback"); err != nil {
		t.Fatalf("Deliver(log): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".odek", "schedule.log"))
	if err != nil || !strings.Contains(string(data), "logged via fallback") {
		t.Errorf("fallback log path failed: err=%v content=%q", err, string(data))
	}
}

func TestAppendScheduleLog_RedactsSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	job := schedule.Job{ID: "jb-secret", Name: "api_key=sk-12345678901234567890123456789012"}
	if err := appendScheduleLog(job, "token is ghp_123456789012345678901234567890123456"); err != nil {
		t.Fatalf("appendScheduleLog: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".odek", "schedule.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "sk-12345678901234567890123456789012") {
		t.Error("log should not contain the raw OpenAI-style key")
	}
	if strings.Contains(string(data), "ghp_123456789012345678901234567890123456") {
		t.Error("log should not contain the raw GitHub PAT")
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Errorf("log should contain [REDACTED] markers: %q", string(data))
	}
}

func TestMergeScheduleDangerous(t *testing.T) {
	base := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Prompt,
		},
		Allowlist: []string{"base-allow"},
		Denylist:  []string{"base-deny"},
	}
	schedule := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.NetworkEgress: danger.Allow,
			danger.SystemWrite:   danger.Allow, // overrides base
		},
		Allowlist: []string{"schedule-allow"},
		Denylist:  []string{"schedule-deny"},
	}
	mergeScheduleDangerous(&base, schedule)

	if base.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("network_egress not added from schedule: %s", base.Classes[danger.NetworkEgress])
	}
	if base.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("system_write not overridden from schedule: %s", base.Classes[danger.SystemWrite])
	}
	if len(base.Allowlist) != 2 || base.Allowlist[0] != "base-allow" || base.Allowlist[1] != "schedule-allow" {
		t.Errorf("allowlist not merged: %v", base.Allowlist)
	}
	if len(base.Denylist) != 2 || base.Denylist[0] != "base-deny" || base.Denylist[1] != "schedule-deny" {
		t.Errorf("denylist not merged: %v", base.Denylist)
	}
}

func TestMergeScheduleDangerous_NilBaseClasses(t *testing.T) {
	base := danger.DangerousConfig{}
	schedule := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.NetworkEgress: danger.Allow,
		},
	}
	mergeScheduleDangerous(&base, schedule)
	if base.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("network_egress not added when base.Classes is nil: %s", base.Classes[danger.NetworkEgress])
	}
}

func TestMergeScheduleDangerous_NilScheduleClasses(t *testing.T) {
	base := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Allow,
		},
	}
	schedule := danger.DangerousConfig{
		Allowlist: []string{"schedule-allow"},
	}
	mergeScheduleDangerous(&base, schedule)
	if base.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("base classes mutated when schedule.Classes is nil")
	}
	if len(base.Allowlist) != 1 || base.Allowlist[0] != "schedule-allow" {
		t.Errorf("allowlist not merged when schedule.Classes is nil: %v", base.Allowlist)
	}
}

func TestMergeScheduleDangerous_ActionAndNonInteractive(t *testing.T) {
	base := danger.DangerousConfig{}
	schedule := danger.DangerousConfig{
		DefaultAction:  strPtr("allow"),
		NonInteractive: strPtr("prompt"),
	}
	mergeScheduleDangerous(&base, schedule)
	if base.DefaultAction == nil || *base.DefaultAction != "allow" {
		t.Errorf("DefaultAction not overridden")
	}
	if base.NonInteractive == nil || *base.NonInteractive != "prompt" {
		t.Errorf("NonInteractive not overridden")
	}
}

func TestBuildHeadlessDangerConfig_Defaults(t *testing.T) {
	resolved := config.ResolvedConfig{}
	cfg := buildHeadlessDangerConfig(resolved)

	if cfg.NonInteractive == nil || *cfg.NonInteractive != "deny" {
		t.Errorf("non_interactive should be forced to deny, got %v", cfg.NonInteractive)
	}
	if cfg.Classes[danger.Destructive] != danger.Deny {
		t.Errorf("destructive should be denied, got %s", cfg.Classes[danger.Destructive])
	}
	if cfg.Classes[danger.Blocked] != danger.Deny {
		t.Errorf("blocked should be denied, got %s", cfg.Classes[danger.Blocked])
	}
}

func TestBuildHeadlessDangerConfig_ScheduleOverridesAllowed(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{
				danger.SystemWrite: danger.Prompt,
			},
		},
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{
					danger.NetworkEgress: danger.Allow,
					danger.SystemWrite:   danger.Allow,
					danger.Destructive:   danger.Allow, // should be floored back to deny
				},
			},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)

	if cfg.Classes[danger.NetworkEgress] != danger.Allow {
		t.Errorf("network_egress should be allow via schedule override, got %s", cfg.Classes[danger.NetworkEgress])
	}
	if cfg.Classes[danger.SystemWrite] != danger.Allow {
		t.Errorf("system_write should be allow via schedule override, got %s", cfg.Classes[danger.SystemWrite])
	}
	if cfg.Classes[danger.Destructive] != danger.Deny {
		t.Errorf("destructive should remain denied by safety floor, got %s", cfg.Classes[danger.Destructive])
	}
	if cfg.Classes[danger.Blocked] != danger.Deny {
		t.Errorf("blocked should remain denied by safety floor, got %s", cfg.Classes[danger.Blocked])
	}
	if *cfg.NonInteractive != "deny" {
		t.Errorf("non_interactive should remain denied by safety floor, got %s", *cfg.NonInteractive)
	}
}
