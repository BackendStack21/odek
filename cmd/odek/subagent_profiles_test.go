package main

// Tests for P4 capability profiles: operator-defined, top-level config,
// overriding the corresponding permissions when selected (profile is
// policy, not escalation — P2/P3 invariants still apply on top).

import (
	"strings"
	"testing"

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
