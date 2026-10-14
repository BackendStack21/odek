package config

import (
	"testing"
)

// A cap set with --max-cost-usd (no global-file cap) is just as binding.
func TestRED_ProjectModelSwitchDisablesCLICostCap(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{
		"model": "a",
		"limits": {"model_prices": {"a": {"input_cost_per_million_usd": 0.3, "output_cost_per_million_usd": 1.2}}}
	}`)
	writeProjectConfig(t, dir, `{"model": "unpriced"}`)
	cfg := LoadConfig(CLIFlags{MaxCostUSD: 2})
	if !cfg.Limits.ResolveForModel(cfg.Model).CostEnforcementActive() {
		t.Errorf("model %q disabled the --max-cost-usd cap", cfg.Model)
	}
	// An explicit CLI model is operator intent and is honoured.
	cfg = LoadConfig(CLIFlags{MaxCostUSD: 2, Model: "unpriced"})
	if cfg.Model != "unpriced" {
		t.Errorf("CLI model overridden: %q", cfg.Model)
	}
}

// A project model the operator has priced is allowed; the cap stays enforced.
func TestRED_ProjectPricedModelStillAllowed(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{
		"model": "a",
		"limits": {"max_cost_usd": 1.5, "model_prices": {
			"a": {"input_cost_per_million_usd": 0.3, "output_cost_per_million_usd": 1.2},
			"b": {"input_cost_per_million_usd": 1, "output_cost_per_million_usd": 2}}}
	}`)
	writeProjectConfig(t, dir, `{"model": "b"}`)
	cfg := LoadConfig(CLIFlags{})
	if cfg.Model != "b" || !cfg.Limits.ResolveForModel(cfg.Model).CostEnforcementActive() {
		t.Errorf("model=%q: priced project model should be honoured with the cap enforced", cfg.Model)
	}
}

// The project ./odek.json may only LOWER an operator budget, never raise or
// disable one (clampProjectLimits; project prices are rejected outright so a
// repo cannot weaken cost enforcement). But the project may still set
// "model", and cost enforcement is keyed on the run's model ID: when the
// operator prices models through limits.model_prices (the documented way to
// price several models) a repository that names any unpriced model resolves
// to zero prices and the operator's max_cost_usd cap is silently switched off
// (CostEnforcementActive=false — only a stderr warning remains).
func TestRED_ProjectModelSwitchDisablesGlobalCostCap(t *testing.T) {
	dir := redIsolate(t)
	writeGlobalConfig(t, dir, `{
		"model": "deepseek-v4-flash",
		"limits": {
			"max_cost_usd": 1.5,
			"model_prices": {
				"deepseek-v4-flash": {"input_cost_per_million_usd": 0.3, "output_cost_per_million_usd": 1.2}
			}
		}
	}`)
	// Control: without a project config the operator's cap is enforced.
	ctl := LoadConfig(CLIFlags{})
	if !ctl.Limits.ResolveForModel(ctl.Model).CostEnforcementActive() {
		t.Fatalf("precondition: operator cost cap not enforced for %q", ctl.Model)
	}

	writeProjectConfig(t, dir, `{"model": "some-unpriced-frontier-model"}`)
	cfg := LoadConfig(CLIFlags{})
	if !cfg.Limits.ResolveForModel(cfg.Model).CostEnforcementActive() {
		t.Errorf("project ./odek.json {\"model\": %q} disabled the operator's max_cost_usd=%.2f cap (model %q has no price, so cost enforcement is off); a project may only lower budgets",
			cfg.Model, cfg.Limits.MaxCostUSD, cfg.Model)
	}
}
