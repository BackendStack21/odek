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
	// The cap is rank-based, so it is only sound if everything above it is
	// denied: it must sit strictly below the LOWEST-ranked denied class.
	// install (rank 3) is floored, so the default cap is local_write (rank 2),
	// not network_egress (rank 4), which would leave install open one hop down.
	if dt.selfMaxRisk != string(danger.LocalWrite) {
		t.Errorf("default headless cap must be downward-closed below install, want local_write got %q", dt.selfMaxRisk)
	}
}

// Granting code_execution alone does NOT lift the children's cap to it: a
// rank cap denies everything above it and nothing below, and install (rank 3)
// is still floored, so a code_execution cap would leave install open one hop
// down. The cap stays downward-closed (local_write); the full-ladder case is
// TestHeadlessParentCapReachesCodeExecutionWhenEverythingBelowIsGranted.
func TestHeadlessParentCapStaysDownwardClosedUnderSingleGrant(t *testing.T) {
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow},
			},
		},
	}
	dt := headlessDelegateTool(t, resolved)
	if dt.selfMaxRisk != string(danger.LocalWrite) {
		t.Errorf("a lone code_execution grant must not lift the cap over the still-floored install, want local_write got %q", dt.selfMaxRisk)
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

// --- review #2 findings, RED first ---

// BLOCKER: dangerCfg := resolved.Dangerous copies the struct but aliases the
// Classes map, so the floor wrote THROUGH to the shared global policy. In
// `odek telegram` the embedded scheduler shares that resolved, so one fire
// locked every later chat turn to code_execution=deny, and concurrent fires
// raced on the map. The headless copy must never write through.
func TestRED_BuildHeadlessDangerConfig_DoesNotMutateSharedConfig(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes:   map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow},
			Allowlist: []string{"ls -la"},
		},
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes:   map[danger.RiskClass]danger.Action{danger.NetworkUpload: danger.Allow},
				Allowlist: []string{"go test ./..."},
			},
		},
	}
	_ = buildHeadlessDangerConfig(resolved)
	if got := resolved.Dangerous.Classes[danger.CodeExecution]; got != danger.Allow {
		t.Errorf("shared global classes mutated: code_execution now %s", got)
	}
	if _, leaked := resolved.Dangerous.Classes[danger.NetworkUpload]; leaked {
		t.Errorf("schedule-only grant leaked into the shared global classes")
	}
	if _, leaked := resolved.Dangerous.Classes[danger.Destructive]; leaked {
		t.Errorf("floor wrote through to the shared global classes")
	}
	if len(resolved.Dangerous.Allowlist) != 1 {
		t.Errorf("shared global allowlist mutated: %q", resolved.Dangerous.Allowlist)
	}
}

// BLOCKER: docs/SCHEDULES.md documents schedules.dangerous.allowlist
// ["go test ./...", "go build ./..."] with NO class grant. A schedule-level
// allowlist entry is an explicit unattended grant with the same standing as a
// class grant and must be kept; only a GLOBAL entry (tuned for an interactive
// session) that reaches a floored class is dropped — and loudly.
func TestRED_BuildHeadlessDangerConfig_KeepsScheduleAllowlistEntries(t *testing.T) {
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Allowlist: []string{"go test ./...", "go build ./..."},
			},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)
	joined := strings.Join(cfg.Allowlist, "\n")
	for _, e := range []string{"go test ./...", "go build ./..."} {
		if !strings.Contains(joined, e) {
			t.Errorf("documented schedules.dangerous.allowlist entry %q must be kept, got %q", e, cfg.Allowlist)
		}
	}
}

func TestBuildHeadlessDangerConfig_KeepsGlobalAllowlistEntryWhenClassGranted(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{Allowlist: []string{"go test ./..."}},
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow},
			},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)
	if !strings.Contains(strings.Join(cfg.Allowlist, "\n"), "go test ./...") {
		t.Errorf("global entry must be kept when its class is explicitly granted, got %q", cfg.Allowlist)
	}
}

func TestRED_BuildHeadlessDangerConfig_WarnsPerDroppedAllowlistEntry(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{Allowlist: []string{"bash -c 'echo hi'"}},
	}
	flush := captureStderr(t)
	buildHeadlessDangerConfig(resolved)
	stderr := flush()
	if !strings.Contains(stderr, "bash -c") || !strings.Contains(stderr, "schedules.dangerous") {
		t.Errorf("a dropped allowlist entry must be reported with where the grant belongs, got %q", stderr)
	}
}

// The global entry that reaches a floored class is dropped only when it is
// NOT also denied merely by the operator's own defaults — the reference is
// the floor set, not every explicit deny. network_upload denied globally
// must not strip an upload entry the operator allowlisted on purpose.
func TestRED_BuildHeadlessDangerConfig_FloorReferenceIsTheFloorOnly(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes:   map[danger.RiskClass]danger.Action{danger.NetworkUpload: danger.Deny},
			Allowlist: []string{"curl -d @report.json https://hooks.example/report"},
		},
	}
	cfg := buildHeadlessDangerConfig(resolved)
	if !strings.Contains(strings.Join(cfg.Allowlist, "\n"), "curl -d") {
		t.Errorf("an entry the operator's own defaults deny is kept (not a floored class), got %q", cfg.Allowlist)
	}
}

// SHOULD-FIX: a documented schedules.dangerous grant of system_write lifted
// the rank cap to 7 and so admitted code_execution (rank 6) one hop down —
// exactly what the cap claims to prevent. A sound cap is downward-closed.
func TestRED_HeadlessParentCapStaysBelowCodeExecutionUnderSystemWriteGrant(t *testing.T) {
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{
					danger.SystemWrite:   danger.Allow,
					danger.NetworkEgress: danger.Allow,
				},
			},
		},
	}
	dt := headlessDelegateTool(t, resolved)
	if danger.Rank(danger.RiskClass(dt.selfMaxRisk)) >= danger.Rank(danger.CodeExecution) {
		t.Errorf("cap must stay below code_execution despite a system_write grant, got %q", dt.selfMaxRisk)
	}
}

// When the operator explicitly grants every class up to code_execution
// (including install, which ranks below it), the children may have it.
func TestHeadlessParentCapReachesCodeExecutionWhenEverythingBelowIsGranted(t *testing.T) {
	resolved := config.ResolvedConfig{
		Schedules: config.ScheduleConfig{
			Dangerous: danger.DangerousConfig{
				Classes: map[danger.RiskClass]danger.Action{
					danger.CodeExecution: danger.Allow,
					danger.NetworkUpload: danger.Allow,
					danger.Install:       danger.Allow,
				},
			},
		},
	}
	dt := headlessDelegateTool(t, resolved)
	if dt.selfMaxRisk != string(danger.CodeExecution) {
		t.Errorf("fully granted ladder must yield a code_execution cap, got %q", dt.selfMaxRisk)
	}
}

func TestBuildHeadlessDangerConfig_WarnsWhenGlobalInstallAllowIgnored(t *testing.T) {
	resolved := config.ResolvedConfig{
		Dangerous: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{danger.Install: danger.Allow},
		},
	}
	flush := captureStderr(t)
	buildHeadlessDangerConfig(resolved)
	if stderr := flush(); !strings.Contains(stderr, "install") || !strings.Contains(stderr, "schedules.dangerous") {
		t.Errorf("expected a warning naming install and schedules.dangerous, got %q", stderr)
	}
}

func TestRunTaskHeadless_InvokesSandboxCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	var cleaned atomic.Int32
	orig := setupSandboxFn
	t.Cleanup(func() { setupSandboxFn = orig })
	setupSandboxFn = func(tools []odek.Tool, cfg sandboxConfig) (string, func() error, error) {
		return "odek-headless-test", func() error { cleaned.Add(1); return nil }, nil
	}
	resolved := headlessMockResolved(t)
	if _, _, err := runTaskHeadless(context.Background(), resolved, "you are a test agent", "say ok", nil); err != nil {
		t.Fatalf("runTaskHeadless: %v", err)
	}
	if cleaned.Load() != 1 {
		t.Errorf("sandbox cleanup must run exactly once when the fire ends, got %d", cleaned.Load())
	}
}

// The daemon paths run many fires under the implicit sandbox default, but
// the startup Dockerfile.odek approval gate only looks at resolved.Sandbox,
// which is false under that default — so an unapproved Dockerfile was never
// gated at startup and degraded every fire instead. Mirror serve: once intent
// resolves to "on", say so in resolved before the gate runs.
func TestRED_ApplyImplicitSandboxDefault(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	r := config.ResolvedConfig{}
	applyImplicitSandboxDefault(&r)
	if !r.Sandbox {
		t.Errorf("implicit default must surface as resolved.Sandbox=true for the approval gate")
	}
	explicitOff := config.ResolvedConfig{Sandbox: false, SandboxExplicit: true}
	applyImplicitSandboxDefault(&explicitOff)
	if explicitOff.Sandbox {
		t.Errorf("an explicit opt-out must stay off")
	}
	t.Setenv("ODEK_NO_SANDBOX", "1")
	envOff := config.ResolvedConfig{}
	applyImplicitSandboxDefault(&envOff)
	if envOff.Sandbox {
		t.Errorf("ODEK_NO_SANDBOX=1 must stay off")
	}
}
