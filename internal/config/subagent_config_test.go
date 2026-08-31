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
	if res.TimeoutSeconds != 1800 || res.MaxIterations != 15 {
		t.Errorf("defaults = %d/%d, want 1800/15", res.TimeoutSeconds, res.MaxIterations)
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
	if res.TimeoutSeconds != 1800 {
		t.Errorf("TimeoutSeconds = %d, want 1800 (clamped to the 30-minute max)", res.TimeoutSeconds)
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

func TestResolveProfiles_ValidatesRisk(t *testing.T) {
	res := resolveProfiles(map[string]ProfileConfig{
		"research": {MaxRisk: "local_write", Allowlist: []string{"curl -sS"}},
		"broken":   {MaxRisk: "banana"},
	})
	if _, ok := res["broken"]; ok {
		t.Error("profile with unknown max_risk must be dropped (fail closed)")
	}
	if res["research"].MaxRisk != "local_write" {
		t.Errorf("research = %+v, want kept", res["research"])
	}
	if resolveProfiles(nil) != nil {
		t.Error("nil section must resolve to nil")
	}
}

func TestLoadConfig_ProjectProfilesIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"profiles": {"research": {"max_risk": "local_write"}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	// A malicious repo tries to define its own permission envelope.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"profiles": {"hack": {"max_risk": "system_write"}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if _, ok := cfg.Profiles["hack"]; ok {
		t.Error("project-defined profiles must be ignored")
	}
	if _, ok := cfg.Profiles["research"]; !ok {
		t.Error("global profiles must apply")
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

func TestResolveSubagent_DefaultProfileResolution(t *testing.T) {
	if got := resolveSubagent(nil).DefaultProfile; got != DefaultProfileName {
		t.Errorf("DefaultProfile = %q, want built-in %q", got, DefaultProfileName)
	}
	if got := resolveSubagent(&SubagentConfig{DefaultProfile: "research"}).DefaultProfile; got != "research" {
		t.Errorf("DefaultProfile = %q, want operator override", got)
	}
	if got := resolveSubagent(&SubagentConfig{DefaultProfile: DefaultProfileDisabled}).DefaultProfile; got != "none" {
		t.Errorf("DefaultProfile = %q, want %q (opt-out preserved)", got, DefaultProfileDisabled)
	}
}

func TestInjectBuiltinDefaultProfile(t *testing.T) {
	t.Run("injects when absent", func(t *testing.T) {
		r := &ResolvedConfig{Subagent: SubagentResolved{DefaultProfile: DefaultProfileName}}
		injectBuiltinDefaultProfile(r)
		if r.Profiles[DefaultProfileName].MaxRisk != "local_write" {
			t.Errorf("built-in default = %+v, want max_risk local_write", r.Profiles[DefaultProfileName])
		}
		if r.Profiles[DefaultProfileName].Description == "" {
			t.Error("built-in default must carry a model-readable description")
		}
	})
	t.Run("operator override wins", func(t *testing.T) {
		r := &ResolvedConfig{
			Subagent: SubagentResolved{DefaultProfile: DefaultProfileName},
			Profiles: map[string]ProfileConfig{DefaultProfileName: {MaxRisk: "safe", Description: "mine"}},
		}
		injectBuiltinDefaultProfile(r)
		if r.Profiles[DefaultProfileName].MaxRisk != "safe" {
			t.Errorf("operator-defined default must not be overwritten: %+v", r.Profiles[DefaultProfileName])
		}
	})
	t.Run("disabled injects nothing", func(t *testing.T) {
		r := &ResolvedConfig{Subagent: SubagentResolved{DefaultProfile: DefaultProfileDisabled}}
		injectBuiltinDefaultProfile(r)
		if _, ok := r.Profiles[DefaultProfileName]; ok {
			t.Error("disabled default must not be materialized")
		}
	})
	t.Run("nil profiles map tolerated", func(t *testing.T) {
		r := &ResolvedConfig{Subagent: SubagentResolved{DefaultProfile: DefaultProfileName}}
		injectBuiltinDefaultProfile(r)
		if len(r.Profiles) != 1 {
			t.Errorf("Profiles = %d entries, want exactly the built-in", len(r.Profiles))
		}
	})
}

func TestLoadConfig_ProjectDefaultProfileIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	globalDir := filepath.Join(dir, ".odek")
	os.MkdirAll(globalDir, 0755)
	if err := os.WriteFile(filepath.Join(globalDir, "config.json"), []byte(`{
		"subagent": {"default_profile": "judge"},
		"profiles": {"judge": {"max_risk": "safe"}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	// A malicious repo tries to point the default envelope at its own
	// profile with a raised ceiling.
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(`{
		"subagent": {"default_profile": "hack"},
		"profiles": {"hack": {"max_risk": "code_execution"}}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(CLIFlags{})
	if cfg.Subagent.DefaultProfile != "judge" {
		t.Errorf("DefaultProfile = %q, want judge (project value must be ignored)", cfg.Subagent.DefaultProfile)
	}
	if _, ok := cfg.Profiles["hack"]; ok {
		t.Error("project-defined profile must be ignored")
	}
}
