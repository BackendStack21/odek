package main

import (
	"testing"

	"github.com/BackendStack21/kode/internal/danger"
)

// ── Tests ─────────────────────────────────────────────────────────────

func TestE2E_YOLO_OneLiner(t *testing.T) {
	skipIfNoE2E(t)

	allow := "allow"
	dc := &danger.DangerousConfig{DefaultAction: &allow}

	for _, cls := range []danger.RiskClass{
		danger.Safe, danger.LocalWrite, danger.SystemWrite,
		danger.Destructive, danger.NetworkEgress,
		danger.CodeExecution, danger.Install,
	} {
		t.Run(string(cls), func(t *testing.T) {
			if got := dc.ActionFor(cls); got != danger.Allow {
				t.Errorf("ActionFor(%q) = %q, want %q", cls, got, danger.Allow)
			}
		})
	}
}

func TestE2E_YOLO_BlockedStillDenied(t *testing.T) {
	skipIfNoE2E(t)

	allow := "allow"
	dc := &danger.DangerousConfig{DefaultAction: &allow}

	got := dc.ActionForCommand(":(){ :|:& };:")
	if got != danger.Deny {
		t.Errorf("fork bomb = %q, want %q (blocked even in YOLO)", got, danger.Deny)
	}
	if got := dc.ActionFor(danger.Blocked); got != danger.Deny {
		t.Errorf("ActionFor(blocked) = %q, want %q", got, danger.Deny)
	}
}

func TestE2E_YOLO_PerClassOverride(t *testing.T) {
	skipIfNoE2E(t)

	allow := "allow"
	dc := &danger.DangerousConfig{
		DefaultAction: &allow,
		Classes:       map[danger.RiskClass]danger.Action{danger.Destructive: danger.Deny},
	}

	if got := dc.ActionFor(danger.Destructive); got != danger.Deny {
		t.Errorf("ActionFor(destructive) = %q, want %q (per-class wins)", got, danger.Deny)
	}
	if got := dc.ActionFor(danger.SystemWrite); got != danger.Allow {
		t.Errorf("ActionFor(system_write) = %q, want %q", got, danger.Allow)
	}
}

func TestE2E_YOLO_Denied(t *testing.T) {
	skipIfNoE2E(t)

	deny := "deny"
	dc := &danger.DangerousConfig{DefaultAction: &deny}

	if got := dc.ActionFor(danger.Safe); got != danger.Deny {
		t.Errorf("ActionFor(safe) with deny config = %q, want %q", got, danger.Deny)
	}
}

func TestE2E_YOLO_ShellTool(t *testing.T) {
	skipIfNoE2E(t)

	allow := "allow"
	st := &shellTool{
		dangerousConfig: danger.DangerousConfig{DefaultAction: &allow},
		ttyPath:         "/nonexistent/tty",
	}

	_, err := st.Call(`{"command": "whoami", "description": "yolo"}`)
	if err != nil {
		t.Errorf("YOLO shell tool should allow, got: %v", err)
	}
}

func TestE2E_YOLO_Commands(t *testing.T) {
	skipIfNoE2E(t)

	allow := "allow"
	dc := danger.DangerousConfig{DefaultAction: &allow}

	cases := []struct {
		name, cmd string
	}{
		{"system_write", "sudo systemctl restart nginx"},
		{"network_egress", "wget http://example.com/file"},
		{"code_execution", "node server.js"},
		{"install", "pip install flask"},
		{"destructive", "rm -rf /var/log/old"},
		{"safe", "ls -la"},
		{"local_write", "mkdir -p /tmp/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dc.ActionForCommand(tc.cmd); got != danger.Allow {
				cls := danger.Classify(tc.cmd)
				t.Errorf("ActionForCommand(%q) = %q (class: %s), want %q",
					tc.cmd, got, cls, danger.Allow)
			}
		})
	}
}
