package main

import (
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/kode/internal/danger"
)

// ── Helpers ───────────────────────────────────────────────────────────

var strPtr = func(s string) *string { return &s }

// newDangerShell creates a shellTool with a DangerousConfig and a
// test TTY at the given path. The TTY file must already exist.
func newDangerShell(t *testing.T, dc danger.DangerousConfig, ttyPath string) *shellTool {
	t.Helper()
	return &shellTool{
		dangerousConfig: dc,
		ttyPath:         ttyPath,
	}
}

// writeTTY writes input to a TTY mock file and returns the path and
// a cleanup function. The file is a regular file, not a real TTY,
// but the shell tool reads from it like a TTY for testing.
func writeTTY(t *testing.T, input string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/tty"
	if err := os.WriteFile(path, []byte(input+"\n"), 0644); err != nil {
		t.Fatalf("write tty mock: %v", err)
	}
	return path, func() { os.Remove(path) }
}

// ── Tests ─────────────────────────────────────────────────────────────

func TestShellTool_Danger_SafeNoPrompt(t *testing.T) {
	tty, cleanup := writeTTY(t, "a")
	defer cleanup()

	st := newDangerShell(t, danger.DangerousConfig{}, tty)
	result, err := st.Call(`{"command": "echo hello"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result = %q, want to contain 'hello'", result)
	}
}

func TestShellTool_Danger_DestructiveDeniedByDefault(t *testing.T) {
	tty, cleanup := writeTTY(t, "a")
	defer cleanup()

	st := newDangerShell(t, danger.DangerousConfig{}, tty)
	_, err := st.Call(`{"command": "rm -rf /"}`)
	if err == nil {
		t.Fatal("expected error for blocked operation, got nil")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want 'denied'", err.Error())
	}
}

func TestShellTool_Danger_SystemWritePromptApprove(t *testing.T) {
	tty, cleanup := writeTTY(t, "a")
	defer cleanup()

	st := newDangerShell(t, danger.DangerousConfig{}, tty)
	result, err := st.Call(`{"command": "sudo ls /root", "description": "check root home"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	// `sudo ls /root` — success may vary depending on test env, but should not be denied
	_ = result
}

func TestShellTool_Danger_SystemWritePromptDeny(t *testing.T) {
	tty, cleanup := writeTTY(t, "d")
	defer cleanup()

	st := newDangerShell(t, danger.DangerousConfig{}, tty)
	_, err := st.Call(`{"command": "sudo ls /root", "description": "check root home"}`)
	if err == nil {
		t.Fatal("expected error for denied operation, got nil")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want 'denied'", err.Error())
	}
}

func TestShellTool_Danger_SystemWriteTrustSession(t *testing.T) {
	tty, cleanup := writeTTY(t, "t")
	defer cleanup()

	st := newDangerShell(t, danger.DangerousConfig{}, tty)

	// First call: trust the session
	_, err := st.Call(`{"command": "sudo ls /root"}`)
	if err != nil {
		t.Fatalf("first Call() error: %v", err)
	}

	// Verify trust is cached
	if !st.trustedClasses[danger.SystemWrite] {
		t.Error("trustedClasses should have SystemWrite after trust")
	}

	// Second call: should skip prompt (trusted)
	_, err = st.Call(`{"command": "sudo cat /etc/shadow"}`)
	if err != nil {
		t.Errorf("second Call() (trusted) error: %v", err)
	}
}

func TestShellTool_Danger_ConfigOverrideAllow(t *testing.T) {
	tty, cleanup := writeTTY(t, "d") // user says deny, but config should overrule
	defer cleanup()

	// Override destructive to Allow
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Allow,
		},
	}
	st := newDangerShell(t, dc, tty)

	// Should run without even prompting
	_, err := st.Call(`{"command": "sudo ls /root"}`)
	if err != nil {
		t.Errorf("Call() with override=Allow error: %v", err)
	}
}

func TestShellTool_Danger_NonInteractiveFallbackDeny(t *testing.T) {
	// No TTY file — will fail to open /dev/tty
	dc := danger.DangerousConfig{
		NonInteractive: strPtr("deny"),
	}
	nonInteractive := strPtr("deny")

	// Use empty config with non-interactive set
	_ = nonInteractive
	st := &shellTool{
		dangerousConfig: dc,
		ttyPath:         "/nonexistent/tty",
	}

	_, err := st.Call(`{"command": "sudo ls /root"}`)
	if err == nil {
		t.Fatal("expected error for non-interactive deny, got nil")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want 'denied'", err.Error())
	}
}

func TestShellTool_Danger_NonInteractiveFallbackAllow(t *testing.T) {
	dc := danger.DangerousConfig{
		NonInteractive: strPtr("allow"),
	}
	st := &shellTool{
		dangerousConfig: dc,
		ttyPath:         "/nonexistent/tty",
	}

	_, err := st.Call(`{"command": "sudo ls /root"}`)
	if err != nil {
		t.Errorf("Call() with non-interactive=Allow error: %v", err)
	}
}

func TestShellTool_Danger_AllowlistBypassesPrompt(t *testing.T) {
	dc := danger.DangerousConfig{
		Allowlist: []string{"sudo ls /root"},
	}
	st := &shellTool{
		dangerousConfig: dc,
		ttyPath:         "/nonexistent/tty", // no TTY needed
	}

	_, err := st.Call(`{"command": "sudo ls /root"}`)
	if err != nil {
		t.Errorf("Call() with allowlisted command error: %v", err)
	}
}

func TestShellTool_Danger_DenylistBlocksWithoutPrompt(t *testing.T) {
	dc := danger.DangerousConfig{
		Denylist: []string{"rm -rf /"},
	}
	st := &shellTool{
		dangerousConfig: dc,
		ttyPath:         "/nonexistent/tty",
	}

	_, err := st.Call(`{"command": "rm -rf /"}`)
	if err == nil {
		t.Fatal("expected error for denylisted command, got nil")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, want 'denied'", err.Error())
	}
}

func TestShellTool_Danger_DescriptionInSchema(t *testing.T) {
	st := &shellTool{}
	schema := st.Schema().(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, ok := props["description"]; !ok {
		t.Error("schema should have 'description' property")
	}
}

func TestShellTool_Danger_Description(t *testing.T) {
	st := &shellTool{}
	desc := st.Description()
	if !strings.Contains(desc, "Risk classes") {
		t.Errorf("Description() should mention risk classes, got: %q", desc)
	}
}

func TestShellTool_Danger_CachedTrustNotShared(t *testing.T) {
	// Two different tools should not share trust caches
	tty1, cleanup1 := writeTTY(t, "t")
	defer cleanup1()
	tty2, cleanup2 := writeTTY(t, "a")
	defer cleanup2()

	st1 := newDangerShell(t, danger.DangerousConfig{}, tty1)
	st2 := newDangerShell(t, danger.DangerousConfig{}, tty2)

	// Trust in st1
	_, err := st1.Call(`{"command": "sudo ls /root"}`)
	if err != nil {
		t.Fatalf("st1 Call() error: %v", err)
	}

	// st2 should not have trust
	if st2.trustedClasses[danger.SystemWrite] {
		t.Error("st2 should not share trust with st1")
	}
}
