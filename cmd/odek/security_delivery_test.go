package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/danger"
)

func securityProbeArgs(v any) string { b, _ := json.Marshal(v); return string(b) }

func securityProbeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".structural-security-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestSecurityDeliveryPartialShellView(t *testing.T) {
	dir := securityProbeDir(t)
	script := filepath.Join(dir, "verify.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'HIDDEN_PAYLOAD'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := danger.WithLedgerKey(context.Background(), t.Name())
	tool := &shellTool{dangerousConfig: danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: danger.Allow, danger.UnreadExec: danger.Deny}}}
	tool.SetContext(ctx)
	if err := tool.checkApproval("sh "+script, ""); err == nil {
		t.Fatal("baseline unread script was not denied")
	}
	out, err := tool.Call(securityProbeArgs(map[string]any{"command": "head -n 1 " + script}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "HIDDEN_PAYLOAD") {
		t.Fatal("probe accidentally showed payload")
	}
	if err := tool.checkApproval("sh "+script, ""); err == nil {
		t.Fatalf("partial head view bypassed unread_exec=deny; output=%s", out)
	}
}

func TestSecurityDeliveryTruncatedFileView(t *testing.T) {
	script := filepath.Join(securityProbeDir(t), "verify.sh")
	body := "#" + strings.Repeat("a", 600000) + "\n#" + strings.Repeat("b", 600000) + "\nprintf 'HIDDEN_PAYLOAD'\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := danger.WithLedgerKey(context.Background(), t.Name())
	tool := &readFileTool{}
	tool.SetContext(ctx)
	out, err := tool.Call(securityProbeArgs(map[string]any{"path": script, "limit": 500}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "HIDDEN_PAYLOAD") || !strings.Contains(out, "truncated") {
		t.Fatal("probe did not hide payload via byte cap")
	}
	if danger.WasReadFreshCtx(ctx, script) {
		t.Fatal("byte-truncated read_file view licensed unseen payload")
	}
}

func TestSecurityDeliveryShellSymlinkPersistence(t *testing.T) {
	dir := t.TempDir()
	target, alias := filepath.Join(dir, ".envrc"), filepath.Join(dir, "notes")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	tool := &shellTool{dangerousConfig: danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{danger.Persistence: danger.Deny}}}
	if err := tool.checkApproval("printf marker > "+target, ""); err == nil {
		t.Fatal("baseline persistence path was not denied")
	}
	command := "printf marker > " + alias
	out, err := tool.Call(securityProbeArgs(map[string]any{"command": command}))
	if err != nil {
		return
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) == "marker" {
		t.Fatalf("persistence=deny bypassed through symlink; classified=%s output=%s", danger.Classify(command), out)
	}
}

func TestSecurityDeliveryInvalidPolicyAction(t *testing.T) {
	cfg := danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{danger.SystemWrite: danger.Action("denny")}}
	if err := cfg.CheckOperation(danger.ToolOperation{Name: "write_file", Resource: "/etc/example", Risk: danger.SystemWrite}, nil); err == nil {
		t.Fatal("invalid configured action silently allowed system write")
	}
}

type securitySwapApprover struct{ swap func() }

func (a securitySwapApprover) PromptCommand(danger.RiskClass, string, string) error {
	a.swap()
	return nil
}
func (a securitySwapApprover) PromptOperation(danger.ToolOperation) error { a.swap(); return nil }

func TestShellRejectsSymlinkSwapDuringApproval(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(dir, "notes")
	ordinary := filepath.Join(dir, "ordinary")
	protected := filepath.Join(dir, ".envrc")
	if err := os.WriteFile(protected, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ordinary, alias); err != nil {
		t.Fatal(err)
	}
	policy := danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{danger.LocalWrite: danger.Prompt, danger.Persistence: danger.Deny}}
	swap := securitySwapApprover{swap: func() {
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(protected, alias); err != nil {
			t.Fatal(err)
		}
	}}
	t.Run("shell", func(t *testing.T) {
		tool := &shellTool{dangerousConfig: policy, approver: swap}
		if _, err := tool.Call(securityProbeArgs(map[string]string{"command": "printf marker > " + alias})); err == nil {
			t.Fatal("changed risk reused approval")
		}
	})
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ordinary, alias); err != nil {
		t.Fatal(err)
	}
	t.Run("parallel_shell", func(t *testing.T) {
		tool := &parallelShellTool{dangerousConfig: policy, approver: swap}
		if _, err := tool.Call(securityProbeArgs(map[string]any{"commands": []map[string]string{{"command": "printf marker > " + alias}}})); err == nil {
			t.Fatal("changed risk reused batch approval")
		}
	})
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ordinary, alias); err != nil {
		t.Fatal(err)
	}
	t.Run("background", func(t *testing.T) {
		manager := bgproc.NewManager(bgproc.Config{MaxOutputBytes: 4096}, nil)
		defer manager.Shutdown()
		tool := &bgStartTool{rt: &bgRuntime{session: t.Name(), mgr: manager}, shell: &shellTool{dangerousConfig: policy, approver: swap}}
		if _, err := tool.Call(securityProbeArgs(map[string]string{"command": "printf marker > " + alias})); err == nil {
			t.Fatal("changed risk reused background approval")
		}
	})
	data, err := os.ReadFile(protected)
	if err != nil || string(data) != "original" {
		t.Fatalf("protected target changed: %q, %v", data, err)
	}
}

func TestReadDeliveryFullNativeReadAndCat(t *testing.T) {
	path := filepath.Join(securityProbeDir(t), "verify.sh")
	body := "#!/bin/sh\nprintf harmless\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, viewer := range []string{"native", "cat", "head", "tail"} {
		t.Run(viewer, func(t *testing.T) {
			ctx := danger.WithLedgerKey(context.Background(), t.Name())
			call := danger.BeginReadDelivery(ctx)
			if viewer == "native" {
				tool := &readFileTool{}
				if _, err := tool.CallContext(call, securityProbeArgs(map[string]string{"path": path})); err != nil {
					t.Fatal(err)
				}
			} else {
				tool := &shellTool{}
				tool.SetContext(call)
				if _, err := tool.Call(securityProbeArgs(map[string]string{"command": viewer + " " + path})); err != nil {
					t.Fatalf("%v (command risk %s, path risk %s)", err, danger.Classify(viewer+" "+path), danger.ClassifyPath(path))
				}
			}
			if danger.WasReadFreshCtx(ctx, path) {
				t.Fatal("receipt visible before delivery")
			}
			danger.FinishReadDelivery(call, true)
			want := viewer == "native" || viewer == "cat"
			if danger.WasReadFreshCtx(ctx, path) != want {
				t.Fatalf("viewer %s license mismatch", viewer)
			}
		})
	}
}

func TestUnreadGateCannotDowngradePolicyDenial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verify.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf harmless\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deny := "deny"
	for _, policy := range []danger.DangerousConfig{
		{DefaultAction: &deny, Classes: map[danger.RiskClass]danger.Action{danger.UnreadExec: danger.Allow}},
		{Classes: map[danger.RiskClass]danger.Action{danger.CodeExecution: "denny"}},
	} {
		tool := &shellTool{dangerousConfig: policy, approver: securitySwapApprover{swap: func() { t.Fatal("denied policy must not prompt") }}}
		if err := tool.checkApproval("sh "+path, ""); err == nil {
			t.Fatal("unread gate downgraded denial")
		}
	}
}
