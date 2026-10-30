package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	odek "github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// Hardening of unattended (scheduled) runs, written RED first against the
// gaps an adversarial review found after the headless code_execution floor
// landed: the floor was parent-local and did not cover install, allowlist
// entries, or the sandbox the headless runner never started.

// install runs lifecycle scripts (npm ci, pip install) — arbitrary code on
// the host. Like code_execution it must not inherit the operator's
// interactive global grant; only an explicit schedules.dangerous grant counts.
func TestRED_BuildHeadlessDangerConfig_GlobalInstallAllowDoesNotLeak(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{danger.Install: danger.Allow},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)
	if got := cfg.Classes[danger.Install]; got != danger.Deny {
		t.Errorf("global install allow must not reach a headless run, want deny got %s", got)
	}
}

func TestBuildHeadlessDangerConfig_ExplicitInstallGrantHonoured(t *testing.T) {
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{danger.Install: danger.Allow},
			},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)
	if got := cfg.Classes[danger.Install]; got != danger.Allow {
		t.Errorf("explicit schedules.dangerous install allow must be honoured, got %s", got)
	}
}

// An exact allowlist entry is allowed before any class check, so a global
// entry that reaches a floored class ("bash -c …" is code_execution) would
// silently bypass the headless floor. Entries that would run something the
// floor denies are dropped; harmless entries stay.
func TestRED_BuildHeadlessDangerConfig_DropsAllowlistEntriesTheFloorDenies(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Allowlist: []string{"bash -c 'echo hi'", "ls -la"},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)
	joined := strings.Join(cfg.Allowlist, "\n")
	if strings.Contains(joined, "bash -c") {
		t.Errorf("allowlist entry reaching code_execution must be dropped unattended, got %q", cfg.Allowlist)
	}
	if !strings.Contains(joined, "ls -la") {
		t.Errorf("harmless allowlist entry must be kept, got %q", cfg.Allowlist)
	}
}

// The floor is parent-local unless the parent hands a cap down: a child
// spawned by delegate_tasks reloads global config and shipped profiles
// (test-runner et al. carry max_risk: code_execution), so a prompt-injected
// scheduled job could run code on the host one hop down. The headless parent
// must stamp a risk cap derived from what its floored policy actually allows
// unattended — prompt counts as deny with no human present — so that cap is
// network_egress by default and clamps code_execution in every child.
func headlessDelegateTool(t *testing.T, resolved config.ResolvedConfig) *delegateTasksTool {
	t.Helper()
	dc := buildHeadlessDangerConfig(resolved)
	tools := builtinTools(dc, nil, nil, 1, "", headlessToolConfig(resolved, dc), nil)
	for _, tl := range tools {
		if d, ok := tl.(*delegateTasksTool); ok {
			return d
		}
	}
	t.Fatal("headless toolset has no delegate_tasks tool")
	return nil
}

func TestRED_HeadlessParentCapsDelegateChildrenBelowCodeExecution(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow},
		},
	}
	dt := headlessDelegateTool(t, resolved)
	if dt.selfMaxRisk == "" {
		t.Fatal("headless parent must stamp a risk cap for its children, got none")
	}
	if danger.Rank(danger.RiskClass(dt.selfMaxRisk)) >= danger.Rank(danger.CodeExecution) {
		t.Errorf("headless parent cap must rank below code_execution, got %q", dt.selfMaxRisk)
	}
	if dt.selfMaxRisk != string(danger.NetworkEgress) {
		t.Errorf("default headless cap should be network_egress (highest class allowed unattended), got %q", dt.selfMaxRisk)
	}
}

func TestRED_HeadlessParentCapFollowsExplicitScheduleGrant(t *testing.T) {
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow},
			},
		},
	}
	dt := headlessDelegateTool(t, resolved)
	if dt.selfMaxRisk != string(danger.CodeExecution) {
		t.Errorf("an explicit schedules.dangerous grant must raise the children's cap to code_execution, got %q", dt.selfMaxRisk)
	}
}

// --- sandbox: headless runs get the same default-on sandbox as `run` ---

func headlessMockChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func headlessMockResolved(t *testing.T) config.ResolvedConfig {
	t.Helper()
	srv := headlessMockChatServer(t)
	resolved := loadJSONMockResolved()
	resolved.BaseURL = srv.URL + "/v1"
	resolved.APIKey = "test"
	resolved.Model = "mock-model"
	return resolved
}

func TestRED_RunTaskHeadless_StartsDefaultSandbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	var calls, toolsSeen atomic.Int32
	orig := setupSandboxFn
	t.Cleanup(func() { setupSandboxFn = orig })
	setupSandboxFn = func(tools []odek.Tool, cfg sandboxConfig) (string, func() error, error) {
		calls.Add(1)
		toolsSeen.Store(int32(len(tools)))
		return "odek-headless-test", func() error { return nil }, nil
	}
	resolved := headlessMockResolved(t)
	if _, _, err := runTaskHeadless(context.Background(), resolved, "you are a test agent", "say ok", nil); err != nil {
		t.Fatalf("runTaskHeadless: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("a headless run must start the default sandbox exactly once, got %d", got)
	}
	if toolsSeen.Load() == 0 {
		t.Errorf("the sandbox must be handed the built tools so they bind to the container")
	}
}

// Docker-less operators keep working exactly as `run` does: an implicit
// sandbox failure degrades with a warning, and the danger floor (not the
// container) stays the hard guarantee for scheduled jobs.
func TestRunTaskHeadless_DegradesWhenDockerUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	orig := setupSandboxFn
	t.Cleanup(func() { setupSandboxFn = orig })
	setupSandboxFn = func(tools []odek.Tool, cfg sandboxConfig) (string, func() error, error) {
		return "", nil, errors.New("docker daemon not reachable")
	}
	resolved := headlessMockResolved(t)
	if _, _, err := runTaskHeadless(context.Background(), resolved, "you are a test agent", "say ok", nil); err != nil {
		t.Fatalf("implicit sandbox failure must degrade, not fail the scheduled run: %v", err)
	}
}
