package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGlobalConfigTemplate_CoversCurrentSections pins that the global
// (`odek init --global`) template carries every operator-facing section of
// the current config surface, so a freshly scaffolded config documents the
// whole schema instead of just the v1.2x-era subset.
func TestGlobalConfigTemplate_CoversCurrentSections(t *testing.T) {
	var fc map[string]any
	if err := json.Unmarshal([]byte(globalConfigTemplate), &fc); err != nil {
		t.Fatalf("globalConfigTemplate is not valid JSON: %v", err)
	}
	for _, section := range []string{
		"provider", "providers", "llm",
		"guard", "limits", "planning", "profiles", "transcription", "vision",
		"trusted_proxies", "dangerous", "tools", "skills", "memory",
		"subagent", "mcp_servers", "web_search", "schedules", "maintenance",
		"telegram",
	} {
		if _, ok := fc[section]; !ok {
			t.Errorf("globalConfigTemplate missing section %q", section)
		}
	}
}

// TestGlobalConfigTemplate_SandboxNotExplicitlyDisabled guards the default-on
// sandbox posture: an explicit `"sandbox": false` in a fresh global config
// silently opts the operator out (the CLI treats *unset* as sandbox-wanted).
func TestGlobalConfigTemplate_SandboxNotExplicitlyDisabled(t *testing.T) {
	if strings.Contains(globalConfigTemplate, "\"sandbox\": false") {
		t.Error(`globalConfigTemplate pins "sandbox": false — remove the key so unset inherits the default-on sandbox posture`)
	}
}

// TestGlobalConfigTemplate_NonInteractiveMatchesDocumentedDefault pins the
// documented built-in `non_interactive` default (read_only) so the template
// cannot drift back to the pre-read_only `deny`.
func TestGlobalConfigTemplate_NonInteractiveMatchesDocumentedDefault(t *testing.T) {
	var fc struct {
		Dangerous struct {
			NonInteractive string `json:"non_interactive"`
		} `json:"dangerous"`
	}
	if err := json.Unmarshal([]byte(globalConfigTemplate), &fc); err != nil {
		t.Fatalf("globalConfigTemplate is not valid JSON: %v", err)
	}
	if fc.Dangerous.NonInteractive != "read_only" {
		t.Errorf("dangerous.non_interactive = %q, want %q (documented built-in default)", fc.Dangerous.NonInteractive, "read_only")
	}
}

// TestGlobalConfigTemplate_NoDeadOrMissingKeys catches template drift: keys
// that no longer exist in the config struct must not linger, and keys whose
// defaults are worth making explicit must be present.
func TestGlobalConfigTemplate_NoDeadOrMissingKeys(t *testing.T) {
	for _, dead := range []string{"skills_skip_max_age_days"} {
		if strings.Contains(globalConfigTemplate, dead) {
			t.Errorf("globalConfigTemplate contains key %q which no longer exists in the config struct", dead)
		}
	}
	for _, want := range []string{
		`"artifacts_max_age_hours"`,
		`"extract_facts"`,
		`"auto_approve_episodes"`,
		`"min_turns_for_extraction"`,
		`"consolidate_on_end"`,
		`"max_depth"`,
		`"announce_budget"`,
		`"budget_inherit"`,
		`"default_chat_id"`,
		`"max_download_size"`,
		`"media_quota_per_chat"`,
		`"trusted_proxies"`,
		`"tool_outputs"`,
	} {
		if !strings.Contains(globalConfigTemplate, want) {
			t.Errorf("globalConfigTemplate missing key %s", want)
		}
	}
}

// TestLocalConfigTemplate_RemainsProjectSafe re-pins the local template's
// contract so global-template work cannot leak operator-only fields into it.
func TestLocalConfigTemplate_RemainsProjectSafe(t *testing.T) {
	for _, op := range []string{
		`"provider"`, `"providers"`, `"api_key"`, `"base_url"`, `"llm"`, `"system"`, `"dangerous"`, `"memory"`,
		`"guard"`, `"maintenance"`, `"telegram"`, `"web_search"`,
		`"embedding"`, `"sessions"`, `"trusted_proxies"`, `"profiles"`,
		`"sandbox"`, `"compaction"`, `"limits"`,
	} {
		if strings.Contains(localConfigTemplate, op) {
			t.Errorf("localConfigTemplate contains operator-only or default-pinning key %s", op)
		}
	}
}
