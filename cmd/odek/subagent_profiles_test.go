package main

// Tests for P4 capability profiles: operator-defined, top-level config,
// overriding the corresponding permissions when selected (profile is
// policy, not escalation — P2/P3 invariants still apply on top).

import (
	"context"
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
	env := newTaskEnvelope("task-1", "goal", "ctx", "guidance", "trusted", "local_write", "research", nil, "untrusted")
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

// TestToolConfigFromResolved_CarriesOperatorSections pins the single
// source of truth for builtinTools' toolConfig. Regression: the serve,
// mcp, and schedule sites built toolConfig literals by hand and omitted
// the Subagent section — Web-UI delegate_tasks ignored the operator's
// subagent.timeout_seconds and ran on the hardcoded 1800s fallback
// (serve.go:716 pre-fix). repl.go omitted Transcription/Vision the same
// way.
func TestToolConfigFromResolved_CarriesOperatorSections(t *testing.T) {
	resolved := config.ResolvedConfig{
		Transcription: config.TranscriptionConfig{AutoTranscribe: true, Model: "whisper-test"},
		WebSearch:     config.WebSearchConfig{BaseURL: "http://searxng-test:8080"},
		Subagent: config.SubagentResolved{
			TimeoutSeconds: 500,
			MaxConcurrency: 7,
			MaxDepth:       3,
		},
		Profiles: map[string]config.ProfileConfig{
			"judge": {MaxRisk: "safe"},
		},
	}
	tc := toolConfigFromResolved(resolved)
	if !tc.Transcription.AutoTranscribe || tc.Transcription.Model != "whisper-test" {
		t.Errorf("Transcription = %+v, want the operator's section", tc.Transcription)
	}
	if tc.WebSearch.BaseURL != "http://searxng-test:8080" {
		t.Errorf("WebSearch = %+v, want the operator's section", tc.WebSearch)
	}
	if tc.Subagent.TimeoutSeconds != 500 || tc.Subagent.MaxConcurrency != 7 || tc.Subagent.MaxDepth != 3 {
		t.Errorf("Subagent = %+v, want operator values (500/7/3)", tc.Subagent)
	}
	if len(tc.Profiles) != 1 {
		t.Errorf("Profiles = %+v, want the operator's profiles", tc.Profiles)
	}
	if tc.Planning == nil {
		t.Error("Planning must be wired (non-nil pointer)")
	}
}

// TestBuiltinTools_DelegateTasksUsesOperatorSubagentLimits locks the
// helper → builtinTools → delegateTasksTool chain: the operator's
// subagent limits must reach the tool, not a hardcoded fallback.
func TestBuiltinTools_DelegateTasksUsesOperatorSubagentLimits(t *testing.T) {
	resolved := config.ResolvedConfig{
		Subagent: config.SubagentResolved{
			TimeoutSeconds: 500,
			MaxConcurrency: 7,
			MaxDepth:       3,
		},
	}
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", toolConfigFromResolved(resolved), nil)
	var dt *delegateTasksTool
	for _, tl := range tools {
		if d, ok := tl.(*delegateTasksTool); ok {
			dt = d
			break
		}
	}
	if dt == nil {
		t.Fatal("builtinTools must register delegate_tasks")
	}
	if dt.timeout != 500*time.Second {
		t.Errorf("delegate_tasks timeout = %v, want the operator's 500s (hardcoded-fallback regression)", dt.timeout)
	}
	if dt.maxConcurrency != 7 {
		t.Errorf("delegate_tasks maxConcurrency = %d, want the operator's 7", dt.maxConcurrency)
	}
	if dt.maxDepth != 3 {
		t.Errorf("delegate_tasks maxDepth = %d, want the operator's 3", dt.maxDepth)
	}
	if dt.profiles != nil {
		t.Errorf("profiles = %+v, want nil (resolved.Profiles empty → child is sole authority)", dt.profiles)
	}
}

// ── Child wiring coverage (unit-level twins of the env-gated E2E) ────────

// TestSubagentCmd_TaskFileProfile_FailsClosed covers the child wiring in
// plain unit runs: a task-file profile the operator has not defined must
// abort before any agent setup.
func TestSubagentCmd_TaskFileProfile_FailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: no operator profiles, no key
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "task.json")
	if err := os.WriteFile(taskPath, []byte(`{"goal":"g","profile":"unit-unknown-profile-xyz"}`), 0644); err != nil {
		t.Fatal(err)
	}
	err := subagentCmd([]string{"--task", taskPath})
	if err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("task-file profile must fail closed with 'unknown profile', got: %v", err)
	}
}

// TestSubagentCmd_TaskFileProfile_ValidAppliesToAgent covers the positive
// child wiring: a profile defined in the operator config resolves, is
// applied (applyProfile + tool filter run), and the command proceeds past
// profile resolution — failing later on LLM setup, which needs no network.
func TestSubagentCmd_TaskFileProfile_ValidAppliesToAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfgDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"profiles":{"judge":{"max_risk":"safe","tools":{"disabled":["shell","write_file"]}}}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(home, "task.json")
	if err := os.WriteFile(taskPath, []byte(`{"goal":"g","profile":"judge"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := subagentCmd([]string{"--task", taskPath})
	if err == nil {
		t.Fatal("expected LLM-setup failure (no API key) — a unit run must not hit the network")
	}
	if strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("defined profile must be accepted and applied, got: %v", err)
	}
}

// TestSubagentCmd_ProfileFlagOutranksTaskFileWiring covers precedence in
// the wired path, not just the helper: with a VALID profile in the task
// file and an unknown one on the flag, the lookup must fail on the flag's
// name — inverted precedence would accept "judge" and proceed instead.
func TestSubagentCmd_ProfileFlagOutranksTaskFileWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"profiles":{"judge":{"max_risk":"safe"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(home, "task.json")
	if err := os.WriteFile(taskPath, []byte(`{"goal":"g","profile":"judge"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := subagentCmd([]string{"--task", taskPath, "--profile", "flag-only-profile"})
	if err == nil || !strings.Contains(err.Error(), `"flag-only-profile"`) {
		t.Fatalf("flag profile must outrank the valid task-file profile, got: %v", err)
	}
}

// TestDelegateTasks_Call_MixedProfilesPartialFailure covers Call-level
// aggregation with a bad profile among passing tasks: the unknown one
// fails closed without spawning, the defined one runs to completion.
func TestDelegateTasks_Call_MixedProfilesPartialFailure(t *testing.T) {
	dir := t.TempDir()
	resultJSON := `{"status":"success","summary":"ok","tokens_used":1,"iterations":1,"files_changed":[]}`
	mock := filepath.Join(dir, "mock-subagent.sh")
	if err := os.WriteFile(mock, []byte("#!/bin/sh\necho '"+resultJSON+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &delegateTasksTool{
		maxConcurrency: 2,
		odekPath:       mock,
		timeout:        10 * time.Second,
		profiles: map[string]config.ProfileConfig{
			"judge": {MaxRisk: "safe"},
		},
	}
	tool.SetContext(context.Background())
	result, err := tool.Call(`{"tasks":[{"goal":"a","profile":"judge"},{"goal":"b","profile":"nope"}],"description":"mixed profiles"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if !strings.Contains(result, "summary: ok") {
		t.Errorf("task with a defined profile must run, got: %s", result)
	}
	if !strings.Contains(result, "unknown profile") {
		t.Errorf("task with an unknown profile must fail closed, got: %s", result)
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
	result := tool.runTask(0, "task-p1", "goal", "", "", "", "", "not-defined", "")
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
	result := tool.runTask(0, "task-p2", "goal", "", "", "", "", "judge", "")
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
	result := tool.runTask(0, "task-p3", "goal", "", "", "", "", "anything", "")
	if !strings.Contains(result, `"ran"`) {
		t.Errorf("nil profiles map must not block the task at the parent, got: %s", result)
	}
}
