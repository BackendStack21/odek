package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── E2E Security: Danger Config Integration ───────────────────────────
//
// These tests verify that all native tools respect the same danger
// classification rules as the shell tool. They use explicit configs
// (non_interactive=deny) plus a deny approver: since the IPI hardening
// (#145/#146), CheckOperation falls back to a live TTYApprover whenever an
// operation lands in the Prompt class — credential reads like /etc/shadow
// are system_write (prompt-by-design) — which blocks on /dev/tty and hangs
// the suite in interactive environments. The deny approver answers the
// Prompt deterministically, so the tests assert the real contract:
// sensitive paths are refused, safe paths are served.

// denyApprover answers every approval prompt with a denial, standing in for
// the absent operator. Error text contains "denied", which is what the
// assertions below match.
type denyApprover struct{}

func (denyApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	return fmt.Errorf("denied by test approver: %s %s", cls, cmd)
}

func (denyApprover) PromptOperation(op danger.ToolOperation) error {
	return fmt.Errorf("denied by test approver: %s %s (risk: %s)", op.Name, op.Resource, op.Risk)
}

func denyNonInteractive() danger.DangerousConfig {
	deny := "deny"
	return danger.DangerousConfig{
		NonInteractive: &deny,
		Approver:       denyApprover{},
	}
}

// ── read_file ─────────────────────────────────────────────────────────

func TestSecurity_ReadFile_SystemPath(t *testing.T) {
	tool := &readFileTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"/etc/shadow"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for /etc/shadow, got: %s", r.Error)
	}
}

func TestSecurity_ReadFile_LocalPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0644)

	tool := &readFileTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"`+path+`"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("expected allow for local path, got: %s", r.Error)
	}
}

func TestSecurity_ReadFile_DestructivePath(t *testing.T) {
	tool := &readFileTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"/boot/vmlinuz"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for /boot path, got: %s", r.Error)
	}
}

func TestSecurity_ReadFile_SSHKeyPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir")
	}
	// The credential-path classification only applies to real home
	// directories. Sandboxed environments run with HOME=/tmp (or similar),
	// where the temp-dir rule (local write) outranks the .ssh pattern and
	// the tool legitimately reports file-not-found instead of a denial.
	if strings.HasPrefix(home, "/tmp") || strings.HasPrefix(home, "/var/folders") {
		t.Skip("HOME is a temp dir — .ssh paths do not classify as credentials there")
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh")); err != nil {
		t.Skip("no ~/.ssh dir")
	}

	tool := &readFileTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"`+filepath.Join(home, ".ssh", "id_rsa")+`"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for %s/.ssh, got: %s", home, r.Error)
	}
}

// ── write_file ────────────────────────────────────────────────────────

func TestSecurity_WriteFile_SystemPath(t *testing.T) {
	tool := &writeFileTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"/etc/test-odek.txt","content":"test"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for /etc path, got: %s", r.Error)
	}
}

func TestSecurity_WriteFile_TmpPath(t *testing.T) {
	tool := &writeFileTool{dangerousConfig: denyNonInteractive()}
	path := filepath.Join(os.TempDir(), "odek-test-"+strconv.Itoa(os.Getpid())+".txt")
	result := callJSON(t, tool, `{"path":"`+path+`","content":"safe"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("expected allow for /tmp path, got: %s", r.Error)
	}
	os.Remove(path)
}

func TestSecurity_WriteFile_DestructivePath(t *testing.T) {
	tool := &writeFileTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"/dev/null","content":"test"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for /dev path, got: %s", r.Error)
	}
}

func TestSecurity_WriteFile_CustomAllowlist(t *testing.T) {
	// /etc is system_write → prompt by default. An explicit per-class
	// Classes override to allow lifts the security denial. (The old
	// non_interactive=allow override no longer feeds ActionFor since the
	// IPI hardening — #145/#146 — moved non_interactive out of the
	// CheckOperation path.)
	// The actual os.WriteFile fails with a filesystem permission error
	// (CI runners aren't root). We test that the error is a filesystem
	// error, NOT a security-layer denial.
	if os.Geteuid() == 0 {
		t.Skip("running as root would actually write /etc/passwd")
	}
	allow := "allow"
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Allow,
		},
		NonInteractive: &allow,
		Approver:       denyApprover{}, // never fall through to /dev/tty
	}
	tool := &writeFileTool{dangerousConfig: dc}
	result := callJSON(t, tool, `{"path":"/etc/passwd","content":"hack"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	// A live prompt fallthrough would surface as a test-approver denial —
	// that means CheckOperation regressed into the TTY path. Fail loudly.
	if strings.Contains(r.Error, "test approver") {
		t.Fatalf("security layer regressed into the prompt path: %s", r.Error)
	}
	// With the class override, the security layer should NOT issue
	// a denial. A filesystem-level permission error is acceptable.
	if r.Error != "" && (strings.Contains(r.Error, "security") || strings.Contains(r.Error, "config")) {
		t.Errorf("expected security layer to allow, got security denial: %s", r.Error)
	}
	// Verify it's a filesystem-level error
	if r.Error != "" && !strings.Contains(r.Error, "permission") {
		t.Logf("expected FS error (not denial): %s", r.Error)
	}
}

// ── search_files ──────────────────────────────────────────────────────

func TestSecurity_SearchFiles_SystemPath(t *testing.T) {
	tool := &searchFilesTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"pattern":"test","path":"/etc"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for /etc path, got: %s", r.Error)
	}
}

func TestSecurity_SearchFiles_LocalPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)

	tool := &searchFilesTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"pattern":"hello","path":"`+dir+`"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error != "" {
		t.Fatalf("expected allow for temp dir, got: %s", r.Error)
	}
}

// ── patch ─────────────────────────────────────────────────────────────

func TestSecurity_Patch_SystemPath(t *testing.T) {
	// patch temp file → then patch system file → denied
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0644)

	// First, successful patch on local path
	tool := &patchTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"path":"`+path+`","old_string":"hello","new_string":"world"}`)
	var r struct {
		Success bool `json:"success"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatal("expected success for local patch")
	}

	// Now try system path
	result = callJSON(t, tool, `{"path":"/etc/motd","old_string":"x","new_string":"y"}`)
	var r2 struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r2)
	if r2.Error == "" || !strings.Contains(r2.Error, "denied") {
		t.Errorf("expected denial for /etc path, got: %s", r2.Error)
	}
}

// ── browser ───────────────────────────────────────────────────────────

func TestSecurity_Browser_ExternalURL(t *testing.T) {
	// External URLs are network_egress → prompt by default. The per-class
	// Classes override to allow replaces the old non_interactive=allow
	// contract (no longer consulted by ActionFor since the IPI hardening).
	allow := "allow"
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.NetworkEgress: danger.Allow,
		},
		NonInteractive: &allow,
		Approver:       denyApprover{}, // never fall through to /dev/tty
	}

	tool := &browserTool{dangerousConfig: dc}
	// Don't actually fetch — just test the classification check passes
	result := callJSON(t, tool, `{"action":"navigate","url":"http://nonexistent-test-12345.com"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	// A live prompt fallthrough would surface as a test-approver denial —
	// fail loudly instead of letting it masquerade as a connect error.
	if strings.Contains(r.Error, "test approver") {
		t.Fatalf("security layer regressed into the prompt path: %s", r.Error)
	}
	// Error should be connection-related, not security-denial
	if r.Error != "" && strings.Contains(r.Error, "denied") {
		t.Errorf("external URL should not be denied: %s", r.Error)
	}
}

func TestSecurity_Browser_Localhost(t *testing.T) {
	tool := &browserTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"action":"navigate","url":"http://localhost:9200"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for localhost, got: %s", r.Error)
	}
}

func TestSecurity_Browser_InternalIP(t *testing.T) {
	tool := &browserTool{dangerousConfig: denyNonInteractive()}
	result := callJSON(t, tool, `{"action":"navigate","url":"http://192.168.1.1"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial for internal IP, got: %s", r.Error)
	}
}

// ── Consistent config across tools ────────────────────────────────────
//
// Verify that all write tools classify the same path the same way.

func TestSecurity_ConfigConsistency_SystemPath(t *testing.T) {
	dc := denyNonInteractive()
	path := "/etc/passwd"

	// All four tools should deny /etc in non-interactive mode
	tools := []struct {
		name string
		tool interface{ Call(string) (string, error) }
	}{
		{"read_file", &readFileTool{dangerousConfig: dc}},
		{"write_file", &writeFileTool{dangerousConfig: dc}},
		{"search_files", &searchFilesTool{dangerousConfig: dc}},
		{"patch", &patchTool{dangerousConfig: dc}},
	}

	for _, tt := range tools {
		var args string
		switch tt.name {
		case "read_file":
			args = `{"path":"` + path + `"}`
		case "write_file":
			args = `{"path":"` + path + `","content":"x"}`
		case "search_files":
			args = `{"pattern":"x","path":"` + path + `"}`
		case "patch":
			args = `{"path":"` + path + `","old_string":"x","new_string":"y"}`
		}

		result, err := tt.tool.Call(args)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tt.name, err)
		}
		var r struct {
			Error string `json:"error"`
		}
		mustUnmarshal(t, result, &r)
		if r.Error == "" || !strings.Contains(r.Error, "denied") {
			t.Errorf("%s(%s) = %q, want denial", tt.name, path, r.Error)
		}
	}
}

func TestSecurity_ConfigConsistency_TmpPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	dc := denyNonInteractive()

	// All four tools should allow tmp paths
	tools := []struct {
		name string
		tool interface{ Call(string) (string, error) }
		prep func()
	}{
		{"read_file", &readFileTool{dangerousConfig: dc}, func() {
			os.WriteFile(path, []byte("hello"), 0644)
		}},
		{"write_file", &writeFileTool{dangerousConfig: dc}, func() {}},
		{"search_files", &searchFilesTool{dangerousConfig: dc}, func() {
			os.WriteFile(path, []byte("hello"), 0644)
		}},
		{"patch", &patchTool{dangerousConfig: dc}, func() {
			os.WriteFile(path, []byte("hello"), 0644)
		}},
	}

	for _, tt := range tools {
		tt.prep()

		var args string
		switch tt.name {
		case "read_file":
			args = `{"path":"` + path + `"}`
		case "write_file":
			args = `{"path":"` + path + `","content":"x"}`
		case "search_files":
			args = `{"pattern":"hello","path":"` + dir + `"}`
		case "patch":
			args = `{"path":"` + path + `","old_string":"hello","new_string":"world"}`
		}

		// Re-create tools to reset trusted state
		result, err := tt.tool.Call(args)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tt.name, err)
		}
		var r struct {
			Error string `json:"error"`
		}
		mustUnmarshal(t, result, &r)
		if r.Error != "" {
			t.Errorf("%s(%s) = %q, want allow", tt.name, path, r.Error)
		}
	}
}

// ── Per-class config override ─────────────────────────────────────────
//
// Verify that changing a risk class in config changes tool behavior.

func TestSecurity_ClassOverride_LocalWriteDenied(t *testing.T) {
	// Override local_write to deny (default is allow)
	deny := "deny"
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.LocalWrite: danger.Deny,
		},
		NonInteractive: &deny,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// write_file to temp dir → local_write → should be denied now
	tool := &writeFileTool{dangerousConfig: dc}
	result := callJSON(t, tool, `{"path":"`+path+`","content":"x"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	if r.Error == "" || !strings.Contains(r.Error, "denied") {
		t.Errorf("expected denial after local_write override, got: %s", r.Error)
	}
}

func TestSecurity_ClassOverride_SystemWriteAllowed(t *testing.T) {
	// Override system_write to allow (default is prompt)
	allow := "allow"
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.SystemWrite: danger.Allow,
		},
		NonInteractive: &allow,
	}

	// write_file to /etc → system_write → should be allowed now
	tool := &writeFileTool{dangerousConfig: dc}
	result := callJSON(t, tool, `{"path":"/etc/odek-test-override","content":"x"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	// Write will fail because /etc is not writable, but it should NOT be a security denial
	if r.Error != "" && (strings.Contains(r.Error, "security") || strings.Contains(r.Error, "class")) {
		t.Errorf("expected no security denial after system_write=allow, got: %s", r.Error)
	}
	// Error should be "cannot create directory" or similar FS error, not a config denial
	if r.Error != "" && !strings.Contains(r.Error, "denied") {
		t.Logf("expected FS error (not denial): %s", r.Error) // expected
	}
}

func TestSecurity_ClassOverride_NetworkEgressAllowed(t *testing.T) {
	// Override network_egress to allow (default is prompt)
	allow := "allow"
	dc := danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.NetworkEgress: danger.Allow,
		},
		NonInteractive: &allow,
	}

	tool := &browserTool{dangerousConfig: dc}
	result := callJSON(t, tool, `{"action":"navigate","url":"http://example.com"}`)
	var r struct {
		Error string `json:"error"`
	}
	mustUnmarshal(t, result, &r)
	// Should NOT be a security denial
	if r.Error != "" && strings.Contains(r.Error, "denied") {
		t.Errorf("expected no security denial after network_egress=allow, got: %s", r.Error)
	}
}
