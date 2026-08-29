package main

// Tests for P4 capability profiles: operator-defined, top-level config,
// overriding the corresponding permissions when selected (profile is
// policy, not escalation — P2/P3 invariants still apply on top).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

func TestApplyProfile_OverridesAllowlistAndClamps(t *testing.T) {
	dc := danger.DangerousConfig{
		Allowlist: []string{"old-global-command"},
	}
	applyProfile(&dc, config.ProfileConfig{
		MaxRisk:   "local_write",
		Allowlist: []string{"go test ./..."},
	})
	if len(dc.Allowlist) != 1 || dc.Allowlist[0] != "go test ./..." {
		t.Errorf("Allowlist = %v, want the profile's (override, not merge)", dc.Allowlist)
	}
	for _, cls := range []danger.RiskClass{
		danger.SystemWrite, danger.Destructive, danger.NetworkEgress,
		danger.CodeExecution, danger.Install, danger.Unknown,
	} {
		if dc.Classes[cls] != danger.Deny {
			t.Errorf("class %v = %v, want deny (above the local_write cap)", cls, dc.Classes[cls])
		}
	}
	if dc.Classes[danger.Safe] == danger.Deny || dc.Classes[danger.LocalWrite] == danger.Deny {
		t.Error("classes at or below the cap must not be clamped to deny")
	}
}

func TestApplyProfile_AllowlistOnlyLeavesClasses(t *testing.T) {
	dc := danger.DangerousConfig{}
	applyProfile(&dc, config.ProfileConfig{Allowlist: []string{"docker compose ps"}})
	if len(dc.Allowlist) != 1 {
		t.Errorf("Allowlist = %v, want the profile's", dc.Allowlist)
	}
	if len(dc.Classes) != 0 {
		t.Errorf("Classes = %v, want untouched (empty max_risk expresses no cap)", dc.Classes)
	}
}

// TestApplyProfile_TrustLockdownStillApplies pins the ordering invariant:
// profile selection is policy, not escalation — the trust lockdown applied
// AFTER the profile still denies.
func TestApplyProfile_TrustLockdownStillApplies(t *testing.T) {
	dc := danger.DangerousConfig{}
	applyProfile(&dc, config.ProfileConfig{MaxRisk: "system_write"})
	applySubagentTrust(&dc, "untrusted", "")
	for _, cls := range []danger.RiskClass{danger.NetworkEgress, danger.SystemWrite, danger.Install} {
		if dc.Classes[cls] != danger.Deny {
			t.Errorf("class %v = %v, want deny (untrusted lockdown survives profile selection)", cls, dc.Classes[cls])
		}
	}
}

func TestTaskEnvelope_Profile(t *testing.T) {
	env := newTaskEnvelope("goal", "ctx", "guidance", "trusted", "local_write", "research", nil, "untrusted")
	if env.Profile != "research" {
		t.Errorf("Profile = %q, want research", env.Profile)
	}
	if env.ParentTrust != "untrusted" {
		t.Errorf("ParentTrust = %q, want untrusted", env.ParentTrust)
	}
}

func TestSubagentCmd_UnknownProfileFailsClosed(t *testing.T) {
	err := subagentCmd([]string{"--goal", "test", "--profile", "no-such-profile"})
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
	if !strings.Contains(err.Error(), "unknown profile") {
		t.Errorf("error should mention the unknown profile, got: %v", err)
	}
}

// ── Task-file profile wiring (P4) ────────────────────────────────────────
//
// The delegate_tasks path passes the profile via the task file, not via
// the --profile flag. Regression: the child-side task-file parser dropped
// the field entirely, so profiled delegate_tasks tasks ran bare and
// unknown profile names never failed closed (docs/CONFIG.md contract).

func TestDecodeTaskFileSpec_ProfileField(t *testing.T) {
	spec, err := decodeTaskFileSpec([]byte(`{
		"goal": "g",
		"context": "c",
		"guidance": "guid",
		"trust_level": "untrusted",
		"max_risk": "local_write",
		"profile": "judge",
		"parent_trust": "trusted"
	}`))
	if err != nil {
		t.Fatalf("decodeTaskFileSpec: %v", err)
	}
	if spec.Profile != "judge" {
		t.Errorf("Profile = %q, want judge — the task-file parser must carry the profile", spec.Profile)
	}
	if spec.Goal != "g" || spec.Context != "c" || spec.Guidance != "guid" ||
		spec.TrustLevel != "untrusted" || spec.MaxRisk != "local_write" || spec.ParentTrust != "trusted" {
		t.Errorf("spec = %+v, want base fields preserved", spec)
	}
}

func TestResolveProfileName_CLIFlagWinsOverTaskFile(t *testing.T) {
	// The operator's direct --profile invocation outranks the parent's
	// task-file declaration.
	if got := resolveProfileName("cli-profile", "task-profile"); got != "cli-profile" {
		t.Errorf("resolveProfileName = %q, want the CLI flag to win", got)
	}
	if got := resolveProfileName("", "task-profile"); got != "task-profile" {
		t.Errorf("resolveProfileName = %q, want the task-file profile when no flag is set", got)
	}
	if got := resolveProfileName("", ""); got != "" {
		t.Errorf("resolveProfileName = %q, want empty when neither is set", got)
	}
}

// TestDelegateTasks_UnknownProfileFailsWithoutSpawn pins parent-side
// fail-closed: an unknown profile must fail the task BEFORE a child is
// spawned. The marker file proves whether the mock child ever ran.
func TestDelegateTasks_UnknownProfileFailsWithoutSpawn(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned.marker")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	mock := filepath.Join(dir, "mock-subagent.sh")
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatalf("write mock: %v", err)
	}

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       mock,
		timeout:        10 * time.Second,
		profiles: map[string]config.ProfileConfig{
			"judge": {MaxRisk: "safe"},
		},
	}
	result := tool.runTask(0, "goal", "", "", "", "", "not-defined")
	if !strings.Contains(result, "unknown profile") {
		t.Errorf("result should fail closed with 'unknown profile', got: %s", result)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("child must not be spawned for an unknown profile")
	}
}

// TestDelegateTasks_KnownProfileSpawns guards against over-blocking: a
// profile that IS defined must pass parent-side validation and spawn.
func TestDelegateTasks_KnownProfileSpawns(t *testing.T) {
	dir := t.TempDir()
	resultJSON := `{"status":"success","summary":"ok","tokens_used":1,"iterations":1,"files_changed":[]}`
	mock := filepath.Join(dir, "mock-subagent.sh")
	script := "#!/bin/sh\necho '" + resultJSON + "'\n"
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatalf("write mock: %v", err)
	}

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       mock,
		timeout:        10 * time.Second,
		profiles: map[string]config.ProfileConfig{
			"judge": {MaxRisk: "safe"},
		},
	}
	result := tool.runTask(0, "goal", "", "", "", "", "judge")
	if !strings.Contains(result, `"ok"`) {
		t.Errorf("defined profile must spawn normally, got: %s", result)
	}
}

// TestDelegateTasks_ProfileValidationSkippedWhenNoProfiles documents the
// nil-map contract: when the operator defines no profiles at all, the
// parent-side check is inactive and the child remains the fail-closed
// authority (its resolved.Profiles is equally empty, so any selection
// still fails there).
func TestDelegateTasks_ProfileValidationSkippedWhenNoProfiles(t *testing.T) {
	dir := t.TempDir()
	resultJSON := `{"status":"success","summary":"ran","tokens_used":1,"iterations":1,"files_changed":[]}`
	mock := filepath.Join(dir, "mock-subagent.sh")
	script := "#!/bin/sh\necho '" + resultJSON + "'\n"
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatalf("write mock: %v", err)
	}

	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       mock,
		timeout:        10 * time.Second,
		profiles:       nil,
	}
	result := tool.runTask(0, "goal", "", "", "", "", "anything")
	if !strings.Contains(result, `"ran"`) {
		t.Errorf("nil profiles map must not block the task at the parent, got: %s", result)
	}
}
