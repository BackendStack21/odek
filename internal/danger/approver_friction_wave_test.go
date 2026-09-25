package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// In friction mode a reflex short answer ("a", "y", or bare Enter) must
// re-prompt once with explicit instructions instead of silently denying;
// an explicit denial input still denies immediately.
func TestFrictionReflexAnswerReprompts(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tty-script")
	if err := os.WriteFile(script, []byte("a\napprove\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = script
	a.FrictionThreshold = 3
	a.FrictionWindow = time.Minute
	a.pauseFn = func(time.Duration) {}
	for i := 0; i < 3; i++ {
		a.recordApproval(CodeExecution)
	}
	if err := a.PromptCommand(CodeExecution, "echo hi", "test"); err != nil {
		t.Fatalf("reflex 'a' should re-prompt, then 'approve' should succeed: %v", err)
	}
}

func TestFrictionExplicitDenialStillDenies(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tty-script")
	if err := os.WriteFile(script, []byte("d\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = script
	a.FrictionThreshold = 3
	a.FrictionWindow = time.Minute
	a.pauseFn = func(time.Duration) {}
	for i := 0; i < 3; i++ {
		a.recordApproval(CodeExecution)
	}
	err := a.PromptCommand(CodeExecution, "echo hi", "test")
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("explicit 'd' must deny immediately, got: %v", err)
	}
}
