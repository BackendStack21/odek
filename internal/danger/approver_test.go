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

func TestPromptCommand_NoTTY_DefaultDenies(t *testing.T) {
	// With no explicit non_interactive setting, the default is now deny.
	a := NewTTYApprover(&DangerousConfig{})
	a.TTYPath = "/nonexistent/tty-that-will-never-exist"

	err := a.PromptCommand(SystemWrite, "rm -rf /etc", "testing default deny path")
	if err == nil {
		t.Fatal("expected error for default non-interactive policy with no TTY")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestPromptCommand_NoTTY_Allow(t *testing.T) {
	// Explicit non_interactive=allow → should return nil when TTY is unavailable
	allow := "allow"
	a := NewTTYApprover(&DangerousConfig{NonInteractive: &allow})
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
	allow := "allow"
	a := NewTTYApprover(&DangerousConfig{NonInteractive: &allow})
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

func TestSetTrustAll_ApprovesAll(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = "/nonexistent/tty-for-test"

	// Enable blanket trust
	a.SetTrustAll(true)

	// Destructive class should auto-approve despite NonInteractive=deny
	if err := a.PromptCommand(Destructive, "rm -rf /", "dangerous command"); err != nil {
		t.Errorf("expected nil with trustAll=true, got: %v", err)
	}

	// Blocked class should also auto-approve
	if err := a.PromptCommand(Blocked, "some blocked cmd", ""); err != nil {
		t.Errorf("expected nil with trustAll=true, got: %v", err)
	}
}

func TestSetTrustAll_ThenDisable(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = "/nonexistent/tty-for-test"

	// Enable blanket trust
	a.SetTrustAll(true)

	// Should be approved
	if err := a.PromptCommand(Destructive, "rm -rf /", ""); err != nil {
		t.Errorf("expected nil with trustAll=true, got: %v", err)
	}

	// Disable blanket trust
	a.SetTrustAll(false)

	// Should now be denied (no TTY, NonInteractive=deny)
	err := a.PromptCommand(Destructive, "rm -rf /", "")
	if err == nil {
		t.Fatal("expected error after disabling trustAll")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestPromptCommand_NoTTY_NilConfigDefaultAllow(t *testing.T) {
	a := NewTTYApprover(nil)
	a.TTYPath = "/nonexistent/tty-for-test"

	// Nil config with no TTY → NonInteractive defaults to Allow → returns nil
	err := a.PromptCommand(SystemWrite, "some command", "")
	if err != nil {
		t.Errorf("expected nil for nil config + no TTY, got: %v", err)
	}
}

func TestPromptCommand_NoTTY_NonInteractiveDeny_Untrusted(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = "/nonexistent/tty-for-test"

	// No trusted classes configure, non-interactive deny → should error
	err := a.PromptCommand(SystemWrite, "touch /etc/config", "write system file")
	if err == nil {
		t.Fatal("expected error for non-interactive deny with no TTY")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestPromptCommand_TrustedClassSkipsTTY(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = "/nonexistent/tty-for-test"

	// Trust Destructive class
	a.SetTrustedClasses(map[RiskClass]bool{Destructive: true})

	// Trusted class is checked before TTY → should succeed even with NonInteractive=deny
	err := a.PromptCommand(Destructive, "rm -rf /tmp/data", "")
	if err != nil {
		t.Errorf("expected nil for trusted class, got: %v", err)
	}

	// SystemWrite is NOT trusted → should be denied
	err = a.PromptCommand(SystemWrite, "touch /etc/config", "")
	if err == nil {
		t.Fatal("expected error for untrusted class with NonInteractive=deny")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}

func TestPromptOperation_NoTTY_NonInteractiveDeny(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = "/nonexistent/tty-for-test"

	op := ToolOperation{
		Name:     "write_file",
		Resource: "/etc/system/config",
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

func TestPromptCommand_NoTTY_NonInteractiveDeny_SafeClass(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = "/nonexistent/tty-for-test"

	// Safe class is not in trusted classes but no trusted classes configured at all
	// so TrustedClasses map exists but is empty → Safe is not trusted
	// NonInteractive=deny + no TTY → should error
	err := a.PromptCommand(Safe, "ls", "")
	if err == nil {
		t.Fatal("expected error for non-interactive deny with no TTY")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error message, got: %v", err)
	}
}
