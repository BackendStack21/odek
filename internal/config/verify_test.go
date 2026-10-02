package config

import (
	"testing"
)

func TestResolveVerify_AbsentSectionDisabled(t *testing.T) {
	res := resolveVerify(nil)
	if res.Enabled {
		t.Fatal("absent verify section must resolve disabled")
	}
	if res.Mode != "" || res.Model != "" || res.MaxCycles != 0 {
		t.Fatalf("zero-value expected: %+v", res)
	}
}
func TestResolveVerify_FullSection(t *testing.T) {
	res := resolveVerify(&VerifyFileConfig{
		Enabled:   boolPtr(true),
		Mode:      "strict",
		Model:     "cheap-model",
		MaxCycles: intPtr(2),
	})
	if !res.Enabled || res.Mode != "strict" || res.Model != "cheap-model" || res.MaxCycles != 2 {
		t.Fatalf("unexpected resolution: %+v", res)
	}
}
func TestResolveVerify_DefaultsAndClamps(t *testing.T) {
	res := resolveVerify(&VerifyFileConfig{Enabled: boolPtr(true), MaxCycles: intPtr(99)})
	if res.Mode != "hint" {
		t.Fatalf("mode default = %q, want hint", res.Mode)
	}
	if res.MaxCycles != verifyCyclesFileMax {
		t.Fatalf("max_cycles clamp = %d, want %d", res.MaxCycles, verifyCyclesFileMax)
	}
}
func TestResolveVerify_UnknownModeFallsBackToHint(t *testing.T) {
	res := resolveVerify(&VerifyFileConfig{Mode: "bogus"})
	if res.Mode != "hint" {
		t.Fatalf("mode = %q, want hint", res.Mode)
	}
}
func TestVerify_SubagentExplicitOptIn(t *testing.T) {
	sub := resolveSubagent(nil)
	if sub.Verify.Enabled {
		t.Fatal("sub-agents must never verify by default")
	}
	sub = resolveSubagent(&SubagentConfig{Verify: &VerifyFileConfig{Enabled: boolPtr(true)}})
	if !sub.Verify.Enabled || sub.Verify.Mode != "hint" {
		t.Fatalf("explicit opt-in = %+v", sub.Verify)
	}
}
