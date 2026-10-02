package main

import (
	"encoding/json"
	"testing"
)

// TestGlobalTemplateDocumentsFullDefaultSurface pins the holistic init fix:
// the global template must carry every config section/key that has a
// meaningful default, so `odek init --global` scaffolds the complete
// operator surface instead of a stale subset. Keys the loader cannot
// represent empty (tts/stt — validation rejects them) are exempt and
// surfaced in the help text instead.
func TestGlobalTemplateDocumentsFullDefaultSurface(t *testing.T) {
	var fc map[string]any
	if err := json.Unmarshal([]byte(globalConfigTemplate), &fc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"tool_progress", "tool_progress_cleanup", "sessions", "embedding",
	} {
		if _, ok := fc[key]; !ok {
			t.Errorf("globalConfigTemplate missing key %q", key)
		}
	}
	var mem map[string]any
	if m, ok := fc["memory"].(map[string]any); ok {
		mem = m
	} else {
		t.Fatal("missing memory section")
	}
	for _, key := range []string{
		"facts_limit_user", "facts_limit_env", "buffer_lines",
		"consolidate_at_cap_pct", "llm_search", "llm_extract",
		"llm_consolidate", "merge_threshold", "add_threshold", "extended",
	} {
		if _, ok := mem[key]; !ok {
			t.Errorf("memory section missing key %q", key)
		}
	}
	var sched map[string]any
	if s, ok := fc["schedules"].(map[string]any); ok {
		sched = s
	} else {
		t.Fatal("missing schedules section")
	}
	if _, ok := sched["allow_telegram_management"]; !ok {
		t.Error("schedules section missing allow_telegram_management")
	} else if v, _ := sched["allow_telegram_management"].(bool); !v {
		t.Error(`schedules.allow_telegram_management must be true (the code default) — pinning false silently disables Telegram schedule management for every init user`)
	}
}

// TestLocalTemplateDocumentsProjectSurface pins that the local template
// carries every key the loader HONORS from project-level config, so a
// fresh `odek init` shows the full project-honorable surface. Values are
// the inherit-neutral/zero forms: bools match built-in defaults, limits
// zeros mean "no project cap".
func TestLocalTemplateDocumentsProjectSurface(t *testing.T) {
	var fc map[string]any
	if err := json.Unmarshal([]byte(localConfigTemplate), &fc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"prompt_caching", "stream", "compaction", "announce_budget",
		"planning", "limits",
	} {
		if _, ok := fc[key]; !ok {
			t.Errorf("localConfigTemplate missing project-honored key %q", key)
		}
	}
}
