package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// TestApplySubagentTrust_Noop confirms that with neither trust_level nor
// max_risk set, the DangerousConfig is unchanged.
func TestApplySubagentTrust_Noop(t *testing.T) {
	dc := danger.DangerousConfig{}
	applySubagentTrust(&dc, "", "")
	if len(dc.Classes) != 0 {
		t.Errorf("Classes mutated for noop call: %+v", dc.Classes)
	}
	if dc.NonInteractive != nil {
		t.Errorf("NonInteractive mutated for noop call: %v", *dc.NonInteractive)
	}
}

// TestApplySubagentTrust_Untrusted_LocksDownEscalationClasses verifies
// that an untrusted task forces deny for every class that could cause
// out-of-task damage in a sub-agent process with no TTY.
func TestApplySubagentTrust_Untrusted_LocksDownEscalationClasses(t *testing.T) {
	dc := danger.DangerousConfig{}
	applySubagentTrust(&dc, "untrusted", "")

	if dc.NonInteractive == nil || *dc.NonInteractive != "deny" {
		t.Errorf("NonInteractive should be 'deny' for untrusted task, got %v", dc.NonInteractive)
	}
	for _, cls := range []danger.RiskClass{
		danger.Destructive,
		danger.CodeExecution,
		danger.Install,
		danger.SystemWrite,
		danger.NetworkEgress,
		danger.Blocked,
	} {
		if got := dc.Classes[cls]; got != danger.Deny {
			t.Errorf("Class %s = %q, want %q for untrusted task", cls, got, danger.Deny)
		}
	}
	// LocalWrite + Safe remain open — sub-agents may still write inside
	// the working directory to do real work.
	for _, cls := range []danger.RiskClass{danger.Safe, danger.LocalWrite} {
		if got, ok := dc.Classes[cls]; ok && got == danger.Deny {
			t.Errorf("Class %s should not be force-denied for untrusted task, got %q", cls, got)
		}
	}
}

// TestApplySubagentTrust_MaxRisk_ClampsAbove verifies the max_risk cap.
// Every class strictly above the cap is forced to Deny.
func TestApplySubagentTrust_MaxRisk_ClampsAbove(t *testing.T) {
	dc := danger.DangerousConfig{}
	applySubagentTrust(&dc, "", "local_write")

	// Classes above LocalWrite (rank 2) must be denied.
	for _, cls := range []danger.RiskClass{
		danger.SystemWrite,
		danger.Destructive,
		danger.NetworkEgress,
		danger.CodeExecution,
		danger.Install,
		danger.Blocked,
	} {
		if got := dc.Classes[cls]; got != danger.Deny {
			t.Errorf("Class %s = %q, want %q with max_risk=local_write", cls, got, danger.Deny)
		}
	}
	// Classes at or below the cap must NOT be force-denied.
	for _, cls := range []danger.RiskClass{danger.Safe, danger.LocalWrite} {
		if got, ok := dc.Classes[cls]; ok && got == danger.Deny {
			t.Errorf("Class %s should be allowed with max_risk=local_write, got %q", cls, got)
		}
	}
}
