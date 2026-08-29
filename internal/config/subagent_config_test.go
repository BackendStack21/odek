package config

// Tests for the operator-only "subagent" section (SUB_AGENTS_IMPROVEMENTS.md
// M1.4/M1.5/M1.6): defaults, documented clamp ceilings, JSON parsing, and the
// project-config trust split (a cloned repo must not be able to extend its
// own sub-agents' lifespans or re-widen budget inheritance).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSubagent_Defaults(t *testing.T) {
	res := resolveSubagent(nil)
	if res.MaxConcurrency != 0 {
		t.Errorf("MaxConcurrency = %d, want 0 (fall back to global)", res.MaxConcurrency)
	}
	if res.TimeoutSeconds != 120 || res.MaxIterations != 15 {
		t.Errorf("defaults = %d/%d, want 120/15", res.TimeoutSeconds, res.MaxIterations)
	}
	if res.MaxDepth != 2 {
		t.Errorf("MaxDepth = %d, want 2", res.MaxDepth)
	}
	if !res.AnnounceBudget {
		t.Error("AnnounceBudget = false, want true (default on)")
	}
	if res.BudgetInherit != BudgetInheritOperator {
		t.Errorf("BudgetInherit = %q, want %q", res.BudgetInherit, BudgetInheritOperator)
	}
}

func TestResolveSubagent_OverridesAndClamps(t *testing.T) {
	nc := 99
	nt := 5000
	ni := -5
	nd := 99
	share := "share"
	res := resolveSubagent(&SubagentConfig{
		MaxConcurrency: &nc,
		TimeoutSeconds: &nt,
		MaxIterations:  &ni,
		MaxDepth:       &nd,
		BudgetInherit:  share,
	})
	if res.MaxConcurrency != 8 {
		t.Errorf("MaxConcurrency = %d, want 8 (clamped)", res.MaxConcurrency)
	}
	if res.TimeoutSeconds != 3600 {
		t.Errorf("TimeoutSeconds = %d, want 3600 (clamped)", res.TimeoutSeconds)
	}
	if res.MaxIterations != 0 {
		t.Errorf("MaxIterations = %d, want 0 (explicit non-positive falls through to the built-in default at the consumer)", res.MaxIterations)
	}
	if res.MaxDepth != 8 {
		t.Errorf("MaxDepth = %d, want 8 (clamped)", res.MaxDepth)
	}
	if res.BudgetInherit != BudgetInheritShare {
		t.Errorf("BudgetInherit = %q, want share", res.BudgetInherit)
	}

	nd = 0
	res = resolveSubagent(&SubagentConfig{MaxDepth: &nd})
	if res.MaxDepth != 1 {
		t.Errorf("MaxDepth = %d, want 1 (explicit 0 clamps to minimum)", res.MaxDepth)
	}
}

func TestSubagentSection_FileConfigJSON(t *testing.T) {
	var fc FileConfig
	if err := json.Unmarshal([]byte(`{"subagent":{"timeout_seconds":240,"max_iterations":20,"announce_budget":false}}`), &fc); err != nil {
		t.Fatal(err)
	}
	if fc.Subagent == nil {
		t.Fatal("subagent section not parsed")
	}
	if fc.Subagent.TimeoutSeconds == nil || *fc.Subagent.TimeoutSeconds != 240 {
		t.Errorf("TimeoutSeconds = %+v, want 240", fc.Subagent.TimeoutSeconds)
	}
	res := resolveSubagent(fc.Subagent)
	if res.TimeoutSeconds != 240 || res.MaxIterations != 20 {
		t.Errorf("resolved = %d/%d, want 240/20", res.TimeoutSeconds, res.MaxIterations)
	}
	if res.AnnounceBudget {
		t.Error("AnnounceBudget = true, want false (explicit opt-out)")
	}
}

func TestLoadConfig_ProjectSubagentIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"subagent": {"timeout_seconds": 300, "max_depth": 3}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	// A malicious repo tries to extend its own sub-agents' lifespan budgets
	// and re-widen budget inheritance.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"subagent": {"timeout_seconds": 3600, "max_iterations": 100, "budget_inherit": "operator", "announce_budget": true}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Subagent.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds = %d, want 300 (global value; project must be ignored)", cfg.Subagent.TimeoutSeconds)
	}
	if cfg.Subagent.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want 3 (global value)", cfg.Subagent.MaxDepth)
	}
	if cfg.Subagent.MaxIterations != 15 {
		t.Errorf("MaxIterations = %d, want 15 (project's 100 ignored; global unset → default)", cfg.Subagent.MaxIterations)
	}
}
