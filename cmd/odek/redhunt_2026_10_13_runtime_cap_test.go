package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

func TestRED_ParentCapDropsAllowlistEntriesAboveCap(t *testing.T) {
	dc := danger.DangerousConfig{Allowlist: []string{"touch gc_out.txt", "ls"}}
	applyParentRiskCap(&dc, "safe")
	if got := dc.ActionForCommand("touch gc_out.txt"); got != danger.Deny {
		t.Errorf("allowlisted write under a safe parent cap = %v, want deny", got)
	}
	if got := dc.ActionForCommand("ls"); got == danger.Deny {
		t.Errorf("allowlisted safe command denied under safe cap")
	}
}

func TestRED_ParentCapNoneDeniesEverything(t *testing.T) {
	dc := danger.DangerousConfig{Allowlist: []string{"ls"}}
	applyParentRiskCap(&dc, parentRiskCapNone)
	if got := dc.ActionForCommand("ls"); got != danger.Deny {
		t.Errorf("ls under a none parent cap = %v, want deny", got)
	}
}

func TestRED_LockedDownChildStampsNoneCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	dc := danger.DangerousConfig{}
	applyParentRiskCap(&dc, parentRiskCapNone)
	tcfg := toolConfig{SelfTrust: "trusted"}
	var dt *delegateTasksTool
	for _, tl := range builtinTools(dc, nil, nil, 1, "", tcfg, nil) {
		if d, ok := tl.(*delegateTasksTool); ok {
			dt = d
		}
	}
	if dt == nil || dt.selfMaxRisk != parentRiskCapNone {
		t.Fatalf("selfMaxRisk = %+v, want %q", dt, parentRiskCapNone)
	}
}

// runtimehuntRunChildTask drives subagentCmd as a depth-2 process with the
// given task JSON and returns the danger config it resolved (nil when it
// failed before reaching that point) plus the error.
func runtimehuntRunChildTask(t *testing.T, task string) (*danger.DangerousConfig, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv(subagentDepthEnvVar, "2")
	t.Chdir(t.TempDir())
	cfgDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"profiles":{"builder":{"max_risk":"code_execution","allowlist":["touch gc_out.txt"]}}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(home, "task.json")
	if err := os.WriteFile(taskPath, []byte(task), 0o644); err != nil {
		t.Fatal(err)
	}
	var got *danger.DangerousConfig
	subagentDangerObserver = func(dc danger.DangerousConfig) { got = &dc }
	t.Cleanup(func() { subagentDangerObserver = nil })
	err := subagentCmd([]string{"--task", taskPath})
	return got, err
}

// Drives the production path: a grandchild that selects an allowlisting
// profile still runs under the spawning sub-agent's cap.
func TestRED_SubagentCmdAppliesParentMaxRisk(t *testing.T) {
	dc, _ := runtimehuntRunChildTask(t, `{"goal":"g","trust_level":"trusted","parent_trust":"trusted","profile":"builder","parent_max_risk":"safe"}`)
	if dc == nil {
		t.Fatal("subagentCmd never reached the parent-cap step")
	}
	if got := dc.ActionFor(danger.LocalWrite); got != danger.Deny {
		t.Errorf("local_write = %v, want deny under parent cap safe", got)
	}
	if got := dc.ActionForCommand("touch gc_out.txt"); got != danger.Deny {
		t.Errorf("profile allowlist bypassed the parent cap: %v", got)
	}
}

func TestRED_SubagentCmdMissingParentMaxRiskFailsClosedAtDepth(t *testing.T) {
	dc, err := runtimehuntRunChildTask(t, `{"goal":"g","trust_level":"trusted","parent_trust":"trusted"}`)
	if dc != nil || err == nil || !strings.Contains(err.Error(), "parent_max_risk") {
		t.Fatalf("want parent_max_risk error before resolving, got dc=%v err=%v", dc, err)
	}
}
