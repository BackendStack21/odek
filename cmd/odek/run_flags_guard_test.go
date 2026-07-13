package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
)

func TestParseRunFlags_GuardProvider(t *testing.T) {
	f, err := parseRunFlags([]string{"--guard-provider", "piguard", "--guard-url", "http://127.0.0.1:8080/detect", "task"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.GuardProvider != guard.ProviderPiguard {
		t.Errorf("GuardProvider = %q, want %q", f.GuardProvider, guard.ProviderPiguard)
	}
	if f.GuardURL != "http://127.0.0.1:8080/detect" {
		t.Errorf("GuardURL = %q, want detect URL", f.GuardURL)
	}
	if f.Task != "task" {
		t.Errorf("Task = %q, want 'task'", f.Task)
	}
}

func TestParseRunFlags_GuardThresholdAndTimeout(t *testing.T) {
	f, err := parseRunFlags([]string{"--guard-threshold", "0.75", "--guard-timeout", "15", "task"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.GuardThreshold != 0.75 {
		t.Errorf("GuardThreshold = %v, want 0.75", f.GuardThreshold)
	}
	if f.GuardTimeoutSeconds != 15 {
		t.Errorf("GuardTimeoutSeconds = %d, want 15", f.GuardTimeoutSeconds)
	}
}

func TestParseRunFlags_GuardScanToggles(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--guard-scan-memory",
		"--guard-no-scan-system-prompt",
		"--guard-scan-skills",
		"--guard-no-scan-tool-outputs",
		"--guard-no-scan-telegram",
		"--guard-no-scan-mcp",
		"task",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.GuardScanMemory == nil || !*f.GuardScanMemory {
		t.Error("GuardScanMemory should be true")
	}
	if f.GuardScanSystemPrompt == nil || *f.GuardScanSystemPrompt {
		t.Error("GuardScanSystemPrompt should be false")
	}
	if f.GuardScanSkills == nil || !*f.GuardScanSkills {
		t.Error("GuardScanSkills should be true")
	}
	if f.GuardScanToolOutputs == nil || *f.GuardScanToolOutputs {
		t.Error("GuardScanToolOutputs should be false")
	}
	if f.GuardScanTelegram == nil || *f.GuardScanTelegram {
		t.Error("GuardScanTelegram should be false")
	}
	if f.GuardScanMCP == nil || *f.GuardScanMCP {
		t.Error("GuardScanMCP should be false")
	}
}

func TestParseRunFlags_GuardFallbackToggle(t *testing.T) {
	f, err := parseRunFlags([]string{"--guard-no-fallback", "task"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.GuardFallbackToLocal == nil || *f.GuardFallbackToLocal {
		t.Error("GuardFallbackToLocal should be false")
	}
}

func TestParseRunFlags_GuardFlagsAfterTask(t *testing.T) {
	f, err := parseRunFlags([]string{"task", "--guard-scan-memory", "--guard-no-fallback"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.GuardScanMemory == nil || !*f.GuardScanMemory {
		t.Error("GuardScanMemory should be true")
	}
	if f.GuardFallbackToLocal == nil || *f.GuardFallbackToLocal {
		t.Error("GuardFallbackToLocal should be false")
	}
	if f.Task != "task" {
		t.Errorf("Task = %q, want 'task'", f.Task)
	}
}
