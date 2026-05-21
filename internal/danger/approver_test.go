package danger

import (
	"strings"
	"testing"
)

func TestNewTTYApprover_NilConfig(t *testing.T) {
	a := NewTTYApprover(nil)
	if a == nil {
		t.Fatal("NewTTYApprover(nil) returned nil")
	}
	if a.DangerousConfig != nil {
		t.Errorf("DangerousConfig = %v, want nil", a.DangerousConfig)
	}
	if a.TTYPath != "/dev/tty" {
		t.Errorf("TTYPath = %q, want /dev/tty", a.TTYPath)
	}
}

func TestNewTTYApprover_WithConfig(t *testing.T) {
	cfg := &DangerousConfig{
		DefaultAction: strPtr("deny"),
	}
	a := NewTTYApprover(cfg)
	if a == nil {
		t.Fatal("NewTTYApprover(cfg) returned nil")
	}
	if a.DangerousConfig != cfg {
		t.Error("DangerousConfig should match the passed config")
	}
	if a.TTYPath != "/dev/tty" {
		t.Errorf("TTYPath = %q, want /dev/tty", a.TTYPath)
	}
	// TrustedClasses map should be initialized (non-nil)
	if a.TrustedClasses == nil {
		t.Error("TrustedClasses map should be initialized")
	}
}

func TestSetTrustedClasses_PopulatesMap(t *testing.T) {
	a := NewTTYApprover(nil)
	classes := map[RiskClass]bool{
		Safe:        true,
		LocalWrite:  true,
		SystemWrite: false,
	}
	a.SetTrustedClasses(classes)

	// Verify via prompt behavior: trusted classes skip TTY prompt
	// Set TTYPath to non-existent to prove we never try to open it
	a.TTYPath = "/nonexistent/tty-for-test"

	// Safe is trusted → should return nil without opening TTY
	err := a.PromptCommand(Safe, "ls", "")
	if err != nil {
		t.Errorf("PromptCommand for trusted class Safe should succeed, got: %v", err)
	}

	// LocalWrite is trusted → should return nil
	err = a.PromptCommand(LocalWrite, "touch file", "")
	if err != nil {
		t.Errorf("PromptCommand for trusted class LocalWrite should succeed, got: %v", err)
	}
}

func TestSetTrustedClasses_ReplaceMap(t *testing.T) {
	a := NewTTYApprover(nil)
	a.TTYPath = "/nonexistent/tty-for-test"

	// Set initial trusted classes
	a.SetTrustedClasses(map[RiskClass]bool{Safe: true})

	// Safe trusted → should succeed without TTY
	if err := a.PromptCommand(Safe, "ls", ""); err != nil {
		t.Errorf("expected nil for trusted Safe, got: %v", err)
	}

	// Replace with empty map
	a.SetTrustedClasses(map[RiskClass]bool{})

	// Safe no longer trusted (empty map) → should fail because TTY doesn't exist
	// and NonInteractive default is Allow, so it won't deny... 
	// Actually with default NonInteractive=Allow, no TTY returns nil.
	// Let me change the config to deny.
	a.DangerousConfig = &DangerousConfig{NonInteractive: strPtr("deny")}

	err := a.PromptCommand(Safe, "ls", "")
	if err == nil {
		t.Error("expected error after trusted classes cleared (no TTY + deny)")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error, got: %v", err)
	}
}

func TestPromptCommand_NoTTY_Deny(t *testing.T) {
	deny := "deny"
	a := NewTTYApprover(&DangerousConfig{NonInteractive: &deny})
	a.TTYPath = "/nonexistent/tty-that-will-never-exist"

	err := a.PromptCommand(SystemWrite, "rm -rf /etc", "testing deny path")
	if err == nil {
		t.Fatal("expected error for non-interactive deny with no TTY")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestPromptCommand_NoTTY_Allow(t *testing.T) {
	// Default NonInteractive is Allow → should return nil when TTY is unavailable
	a := NewTTYApprover(nil)
	a.TTYPath = "/nonexistent/tty-that-will-never-exist"

	err := a.PromptCommand(SystemWrite, "rm -rf /etc", "testing allow path")
	if err != nil {
		t.Errorf("expected nil for non-interactive allow, got: %v", err)
	}
}

func TestPromptOperation_NoTTY_Deny(t *testing.T) {
	deny := "deny"
	a := NewTTYApprover(&DangerousConfig{NonInteractive: &deny})
	a.TTYPath = "/nonexistent/tty-that-will-never-exist"

	op := ToolOperation{
		Name:     "read_file",
		Resource: "/etc/passwd",
		Risk:     SystemWrite,
	}
	err := a.PromptOperation(op)
	if err == nil {
		t.Fatal("expected error for non-interactive deny with no TTY")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestPromptOperation_NoTTY_Allow(t *testing.T) {
	a := NewTTYApprover(nil)
	a.TTYPath = "/nonexistent/tty-that-will-never-exist"

	op := ToolOperation{
		Name:     "read_file",
		Resource: "/etc/passwd",
		Risk:     SystemWrite,
	}
	err := a.PromptOperation(op)
	if err != nil {
		t.Errorf("expected nil for non-interactive allow, got: %v", err)
	}
}
