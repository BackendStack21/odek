package main

// RED-first tests for the new-user defaults workstream:
//  1. network_egress defaults to allow in the built-in danger policy
//  2. the odek init --global template must NOT set a global dangerous.action
//     override (it used to force "prompt", downgrading even safe/local_write
//     to prompting and wrecking the out-of-box experience)

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitTemplate_NoGlobalDangerousActionOverride(t *testing.T) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(globalConfigTemplate), &cfg); err != nil {
		t.Fatalf("global config template is not valid JSON: %v", err)
	}
	dangerRaw, ok := cfg["dangerous"].(map[string]any)
	if !ok {
		t.Fatal("template lacks a dangerous section")
	}
	if v, present := dangerRaw["action"]; present {
		t.Errorf("dangerous.action must not be set in the template (got %q) — it overrides ALL per-class defaults, downgrading safe/local_write to prompt", v)
	}
}

func TestInitTemplate_NetworkEgressAllowsByDefault(t *testing.T) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(globalConfigTemplate), &cfg); err != nil {
		t.Fatalf("global config template is not valid JSON: %v", err)
	}
	// With no classes override for network_egress, the built-in default
	// (allow) must apply. Explicitly assert the template does not pin it
	// back to prompt.
	if dangerRaw, ok := cfg["dangerous"].(map[string]any); ok {
		if classes, ok := dangerRaw["classes"].(map[string]any); ok {
			if v, present := classes["network_egress"]; present && v == "prompt" {
				t.Error("template pins network_egress to prompt; leave it unset so the allow default applies")
			}
		}
	}
	if strings.Contains(globalConfigTemplate, `"action": "prompt"`) {
		t.Error("template still contains a global dangerous.action=prompt override")
	}
}
