package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// TestApplySubagentTrust_EmptyDefaultsToUntrusted confirms that a missing
// trust_level is treated as untrusted, so a parent cannot spawn a full-trust
// sub-agent simply by omitting the field.
func TestApplySubagentTrust_EmptyDefaultsToUntrusted(t *testing.T) {
	dc := danger.DangerousConfig{}
	applySubagentTrust(&dc, "", "")
	if dc.NonInteractive == nil || *dc.NonInteractive != "deny" {
		t.Errorf("NonInteractive should default to 'deny' when trust_level is empty, got %v", dc.NonInteractive)
	}
	for _, cls := range []danger.RiskClass{
		danger.Destructive,
		danger.CodeExecution,
		danger.Install,
		danger.SystemWrite,
		danger.NetworkEgress,
		danger.Unknown,
		danger.Blocked,
	} {
		if got := dc.Classes[cls]; got != danger.Deny {
			t.Errorf("Class %s = %q, want %q when trust_level is empty", cls, got, danger.Deny)
		}
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
		danger.Unknown,
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
		danger.Unknown,
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

// TestApplySubagentTrust_MaxRiskUnknown_KeepsSafeOpen guards the fix for the
// cap miscomputation: before Unknown was added to riskRank's shared ordering,
// max_risk="unknown" computed rank 0 and force-denied even Safe. It must now
// leave Safe/LocalWrite open and deny only the classes above Unknown.
func TestApplySubagentTrust_MaxRiskUnknown_KeepsSafeOpen(t *testing.T) {
	dc := danger.DangerousConfig{}
	applySubagentTrust(&dc, "", "unknown")

	for _, cls := range []danger.RiskClass{danger.Safe, danger.LocalWrite} {
		if got, ok := dc.Classes[cls]; ok && got == danger.Deny {
			t.Errorf("Class %s must stay open with max_risk=unknown, got %q", cls, got)
		}
	}
	// Only classes ranked above Unknown (Destructive, Blocked) are denied.
	for _, cls := range []danger.RiskClass{danger.Destructive, danger.Blocked} {
		if got := dc.Classes[cls]; got != danger.Deny {
			t.Errorf("Class %s = %q, want deny with max_risk=unknown", cls, got)
		}
	}
}

// TestSubagentAllowsMCP_UntrustedDeniesLoading verifies the M5 mitigation:
// untrusted sub-agents must not load MCP servers, because MCP tool adapters
// do not perform their own danger classification and the parent-controlled
// trust cap would otherwise be illusory.
func TestSubagentAllowsMCP_UntrustedDeniesLoading(t *testing.T) {
	for _, tc := range []struct {
		trust string
		want  bool
	}{
		{"untrusted", false},
		{"", false}, // empty defaults to untrusted
		{"trusted", true},
		{"destructive", true},
	} {
		t.Run(tc.trust, func(t *testing.T) {
			if got := subagentAllowsMCP(tc.trust); got != tc.want {
				t.Errorf("subagentAllowsMCP(%q) = %v, want %v", tc.trust, got, tc.want)
			}
		})
	}
}

// TestSubagentTrust_ForcesUnknownDenyForMCP verifies that the DangerousConfig
// applied to untrusted sub-agents denies the Unknown risk class, which is the
// class returned by classifyToolCall for MCP tools (<server>__<tool>).
func TestSubagentTrust_ForcesUnknownDenyForMCP(t *testing.T) {
	dc := danger.DangerousConfig{}
	applySubagentTrust(&dc, "untrusted", "")
	if got := dc.ActionFor(danger.Unknown); got != danger.Deny {
		t.Errorf("untrusted sub-agent ActionFor(Unknown) = %q, want deny", got)
	}
}

// TestIsOdekTempTaskFile verifies the path guard used when cleaning up the
// task file handed from parent to sub-agent.
func TestIsOdekTempTaskFile(t *testing.T) {
	tmp := t.TempDir()
	origTmp := os.Getenv("TMPDIR")
	os.Setenv("TMPDIR", tmp)
	defer os.Setenv("TMPDIR", origTmp)

	valid := filepath.Join(tmp, "odek-task-abc123.json")
	if err := os.WriteFile(valid, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{valid, true},
		{filepath.Join(tmp, "odek-task-abc123.json") + "/extra", false},
		{filepath.Join(tmp, "other-task-abc123.json"), false},
		{filepath.Join(t.TempDir(), "odek-task-abc123.json"), false},
		{"/etc/passwd", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isOdekTempTaskFile(tc.path); got != tc.want {
			t.Errorf("isOdekTempTaskFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
