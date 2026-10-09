package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

func countGateCalls(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := shellScriptGate
	shellScriptGate = func(ctx context.Context, cmd string) (danger.RiskClass, []string) {
		n++
		return orig(ctx, cmd)
	}
	t.Cleanup(func() { shellScriptGate = orig })
	return &n
}

// An allowed command is classified once up front and once more for the
// pre-dispatch revalidation; the approval steps reuse the first result.
func TestRED_Shell_ApprovalClassifiesScriptGateOncePlusRevalidation(t *testing.T) {
	calls := countGateCalls(t)
	tool := &shellTool{dangerousConfig: danger.DangerousConfig{}}
	args, _ := json.Marshal(map[string]string{"command": "echo hello"})
	out, err := tool.Call(string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("unexpected output %q", out)
	}
	if *calls > 2 {
		t.Fatalf("script gate classified the command %d times; want 2 (snapshot + revalidation)", *calls)
	}
}

type gateCountApprover struct{ prompts int }

func (a *gateCountApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	a.prompts++
	return nil
}
func (a *gateCountApprover) PromptOperation(op danger.ToolOperation) error { return nil }

func TestRED_Shell_PromptedCommandClassifiesScriptGateOncePlusRevalidation(t *testing.T) {
	calls := countGateCalls(t)
	ap := &gateCountApprover{}
	tool := &shellTool{
		dangerousConfig: danger.DangerousConfig{
			Classes: map[danger.RiskClass]danger.Action{danger.LocalWrite: danger.Prompt},
		},
		approver: ap,
	}
	args, _ := json.Marshal(map[string]string{"command": "touch " + filepath.Join(t.TempDir(), "f")})
	_, _ = tool.Call(string(args))
	if ap.prompts != 1 {
		t.Fatalf("expected the command to be prompted once, got %d", ap.prompts)
	}
	if *calls > 2 {
		t.Fatalf("script gate classified the command %d times on the prompting path; want 2", *calls)
	}
}
