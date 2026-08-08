package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGlobalConfig writes ~/.odek/config.json under the given fake HOME.
func writeGlobalConfig(t *testing.T, home, body string) {
	t.Helper()
	cfgDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeProjectConfig writes ./odek.json under the given working directory.
func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "odek.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadConfig_Limits_GlobalOnly: the operator config sets limits; with no
// project config they pass through unchanged.
func TestLoadConfig_Limits_GlobalOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{
		"limits": {
			"max_runtime_seconds": 300,
			"max_tool_calls": 50,
			"max_input_tokens": 100000,
			"max_output_tokens": 20000,
			"max_cost_usd": 1.5,
			"input_cost_per_million_usd": 2.0,
			"output_cost_per_million_usd": 8.0
		}
	}`)

	cfg := LoadConfig(CLIFlags{})
	l := cfg.Limits
	if l.MaxRuntimeSeconds != 300 || l.MaxToolCalls != 50 || l.MaxInputTokens != 100000 ||
		l.MaxOutputTokens != 20000 || l.MaxCostUSD != 1.5 {
		t.Errorf("global limits not resolved: %+v", l)
	}
	if !l.CostEnforcementActive() {
		t.Error("cost enforcement should be active with cap + both prices")
	}
}

// TestLoadConfig_Limits_ProjectCannotRaise is the security bar (review
// note 5): a malicious repo must not be able to raise a globally-set budget.
func TestLoadConfig_Limits_ProjectCannotRaise(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"limits": {"max_tool_calls": 50, "max_runtime_seconds": 300}}`)
	writeProjectConfig(t, dir, `{"limits": {"max_tool_calls": 500000, "max_runtime_seconds": 60}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Limits.MaxToolCalls != 50 {
		t.Errorf("project raised max_tool_calls: got %d, want global 50", cfg.Limits.MaxToolCalls)
	}
	if cfg.Limits.MaxRuntimeSeconds != 60 {
		t.Errorf("project-lowered max_runtime_seconds should win: got %d, want 60", cfg.Limits.MaxRuntimeSeconds)
	}
}

// TestLoadConfig_Limits_ProjectCannotDisable: zeroing out (or simply not
// repeating) a globally-set limit in ./odek.json must not remove it.
func TestLoadConfig_Limits_ProjectCannotDisable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"limits": {"max_tool_calls": 50, "max_input_tokens": 100000}}`)

	// Explicit zero-out attempt on one limit; the other simply absent.
	writeProjectConfig(t, dir, `{"limits": {"max_tool_calls": 0}}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Limits.MaxToolCalls != 50 {
		t.Errorf("project zero-out disabled max_tool_calls: got %d, want 50", cfg.Limits.MaxToolCalls)
	}
	if cfg.Limits.MaxInputTokens != 100000 {
		t.Errorf("project limits section dropped max_input_tokens: got %d, want 100000", cfg.Limits.MaxInputTokens)
	}

	// Absent project limits section must not drop anything either.
	os.Remove(filepath.Join(dir, "odek.json"))
	writeProjectConfig(t, dir, `{"model": "project-model"}`)
	cfg = LoadConfig(CLIFlags{})
	if cfg.Limits.MaxToolCalls != 50 || cfg.Limits.MaxInputTokens != 100000 {
		t.Errorf("limits lost without a project limits section: %+v", cfg.Limits)
	}
}

// TestLoadConfig_Limits_ProjectMayTighten: a project may set limits the
// global config does not have (that only restricts the run further).
func TestLoadConfig_Limits_ProjectMayTighten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"model": "global-model"}`)
	writeProjectConfig(t, dir, `{"limits": {"max_tool_calls": 10}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Limits.MaxToolCalls != 10 {
		t.Errorf("project-tightened limit should apply: got %d, want 10", cfg.Limits.MaxToolCalls)
	}
}

// TestLoadConfig_Limits_ProjectPricesIgnored: per-million prices are not
// limits — a lower project price would weaken cost enforcement — so project
// prices are ignored and global prices survive.
func TestLoadConfig_Limits_ProjectPricesIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"limits": {
		"max_cost_usd": 1.0,
		"input_cost_per_million_usd": 2.0,
		"output_cost_per_million_usd": 8.0
	}}`)
	writeProjectConfig(t, dir, `{"limits": {
		"input_cost_per_million_usd": 0.000001,
		"output_cost_per_million_usd": 0.000001
	}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Limits.InputCostPerMillionUSD != 2.0 || cfg.Limits.OutputCostPerMillionUSD != 8.0 {
		t.Errorf("project prices must not override global prices: %+v", cfg.Limits)
	}
	if !cfg.Limits.CostEnforcementActive() {
		t.Error("cost enforcement should remain active on global prices")
	}
}

// TestLoadConfig_Limits_CLIFlagsSetExplicitly: CLI flags are operator intent
// and override file-configured limits in either direction.
func TestLoadConfig_Limits_CLIFlagsSetExplicitly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"limits": {"max_tool_calls": 50}}`)

	cfg := LoadConfig(CLIFlags{MaxToolCalls: 500, MaxRuntimeSeconds: 120})
	if cfg.Limits.MaxToolCalls != 500 {
		t.Errorf("CLI --max-tool-calls should override global: got %d, want 500", cfg.Limits.MaxToolCalls)
	}
	if cfg.Limits.MaxRuntimeSeconds != 120 {
		t.Errorf("CLI --max-runtime should set the limit: got %d, want 120", cfg.Limits.MaxRuntimeSeconds)
	}
}

// TestLoadConfig_Limits_CostCapWithoutPrices: a cost cap without prices must
// not disable token budgets (enforcement activity is a property of Limits).
func TestLoadConfig_Limits_CostCapWithoutPrices(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"limits": {"max_cost_usd": 1.0, "max_input_tokens": 1000}}`)

	cfg := LoadConfig(CLIFlags{})
	if cfg.Limits.CostEnforcementActive() {
		t.Error("cost enforcement must stay disabled without prices")
	}
	if cfg.Limits.MaxInputTokens != 1000 {
		t.Error("token budget must remain configured")
	}
}

// TestLoadConfig_Limits_ModelPricesGlobal: the operator config may set
// per-model prices; they parse into the limits section and resolve for
// exact model IDs with flat-pair fallback.
func TestLoadConfig_Limits_ModelPricesGlobal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{
		"model": "example-fast-model",
		"limits": {
			"max_cost_usd": 25.0,
			"input_cost_per_million_usd": 0.30,
			"output_cost_per_million_usd": 1.20,
			"model_prices": {
				"example-fast-model": {"input_cost_per_million_usd": 0.14, "output_cost_per_million_usd": 0.28},
				"example-pro-model": {"input_cost_per_million_usd": 1.25, "output_cost_per_million_usd": 10.0}
			}
		}
	}`)

	cfg := LoadConfig(CLIFlags{})
	l := cfg.Limits
	if len(l.ModelPrices) != 2 {
		t.Fatalf("model_prices not parsed: %+v", l.ModelPrices)
	}
	if got := l.ModelPrices["example-pro-model"]; got.InputCostPerMillionUSD != 1.25 || got.OutputCostPerMillionUSD != 10.0 {
		t.Errorf("example-pro-model prices = %+v", got)
	}
	// Resolved for the configured model: model_prices wins over the flat pair.
	in, out := l.ResolvePrices(cfg.Model)
	if in != 0.14 || out != 0.28 {
		t.Errorf("ResolvePrices(%q) = (%v, %v), want (0.14, 0.28)", cfg.Model, in, out)
	}
	if !l.ResolveForModel(cfg.Model).CostEnforcementActive() {
		t.Error("cost enforcement should be active on model-resolved prices")
	}
	// Unknown models fall back to the flat pair.
	in, out = l.ResolvePrices("some-other-model")
	if in != 0.30 || out != 1.20 {
		t.Errorf("ResolvePrices(some-other-model) = (%v, %v), want flat (0.30, 1.20)", in, out)
	}
}

// TestLoadConfig_Limits_ProjectModelPricesRejected is the security bar for
// per-model prices: a malicious repo must not weaken cost enforcement by
// setting low prices for the operator's model.
func TestLoadConfig_Limits_ProjectModelPricesRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"model": "example-pro-model", "limits": {
		"max_cost_usd": 1.0,
		"input_cost_per_million_usd": 2.0,
		"output_cost_per_million_usd": 8.0,
		"model_prices": {"example-pro-model": {"input_cost_per_million_usd": 1.25, "output_cost_per_million_usd": 10.0}}
	}}`)
	// The project tries to collapse the price of the operator's model to
	// nearly zero — this must be ignored outright, and the global
	// model_prices map must survive.
	writeProjectConfig(t, dir, `{"limits": {
		"model_prices": {"example-pro-model": {"input_cost_per_million_usd": 0.000001, "output_cost_per_million_usd": 0.000001}}
	}}`)

	cfg := LoadConfig(CLIFlags{})
	l := cfg.Limits
	if len(l.ModelPrices) != 1 {
		t.Fatalf("global model_prices must survive a project model_prices section: %+v", l.ModelPrices)
	}
	in, out := l.ResolvePrices("example-pro-model")
	if in != 1.25 || out != 10.0 {
		t.Errorf("project model_prices leaked into resolution: (%v, %v), want (1.25, 10.0)", in, out)
	}
	if !l.ResolveForModel("example-pro-model").CostEnforcementActive() {
		t.Error("cost enforcement must remain active on the global model prices")
	}
}

// TestLoadConfig_Limits_ProjectModelPricesRejectedWithoutGlobal: even when
// the global config has no model_prices at all, a project may not introduce
// one (rejection is unconditional, like the flat prices).
func TestLoadConfig_Limits_ProjectModelPricesRejectedWithoutGlobal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	writeGlobalConfig(t, dir, `{"limits": {"max_tool_calls": 50}}`)
	writeProjectConfig(t, dir, `{"limits": {
		"model_prices": {"any-model": {"input_cost_per_million_usd": 0.000001, "output_cost_per_million_usd": 0.000001}}
	}}`)

	cfg := LoadConfig(CLIFlags{})
	if len(cfg.Limits.ModelPrices) != 0 {
		t.Errorf("project model_prices must be rejected outright, got %+v", cfg.Limits.ModelPrices)
	}
}
