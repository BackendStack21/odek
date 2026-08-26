package danger

import (
	"strings"
	"testing"
)

// ── H-7: non_interactive "read_only" — useful containment ────────────────
//
// The study's deny posture produced 51 runs that never reached the payload
// because the agent couldn't ls/tree/cat. Real containment, but not
// resistance — and teams flip it to "allow" to get work done, losing every
// protection. read_only: inspection proceeds, mutation fails closed.

func newNoTTYApprover(mode string) *TTYApprover {
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr(mode)})
	a.TTYPath = "/nonexistent/tty-for-test"
	return a
}

func TestReadOnly_NoTTY_ReadsProceed(t *testing.T) {
	a := newNoTTYApprover("read_only")

	// A Safe-classified shell command (the classifier already judged it
	// non-mutating) — the exact `ls` that the deny posture blocked.
	if err := a.PromptCommand(Safe, "ls -la", ""); err != nil {
		t.Errorf("Safe command under read_only = %v, want allowed", err)
	}

	// A native read tool over an ordinary path.
	op := ToolOperation{Name: "read_file", Resource: "src/main.go", Risk: LocalWrite}
	if err := a.PromptOperation(op); err != nil {
		t.Errorf("read_file under read_only = %v, want allowed", err)
	}
}

func TestReadOnly_NoTTY_MutationsDenied(t *testing.T) {
	a := newNoTTYApprover("read_only")

	// Shell mutation (would be prompted interactively).
	err := a.PromptCommand(CodeExecution, "bash build.sh", "")
	if err == nil || !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("exec under read_only = %v, want read_only denial", err)
	}

	// Write tool.
	op := ToolOperation{Name: "write_file", Resource: "src/main.go", Risk: LocalWrite}
	if err := a.PromptOperation(op); err == nil {
		t.Fatal("write_file under read_only must be denied")
	}

	// A read tool aimed at a sensitive location still gates.
	sensitive := ToolOperation{Name: "read_file", Resource: "/etc/shadow", Risk: SystemWrite}
	if err := a.PromptOperation(sensitive); err == nil {
		t.Fatal("sensitive-location read under read_only must be denied")
	}

	// Non-read tools never get the read carve-out regardless of class.
	notRead := ToolOperation{Name: "write_file", Resource: "/etc/x", Risk: SystemWrite}
	if err := a.PromptOperation(notRead); err == nil {
		t.Fatal("write tool must never be treated as a read")
	}
}

func TestReadOnly_DefaultNoTTY(t *testing.T) {
	// Unset non_interactive defaults to read_only: same behavior.
	a := NewTTYApprover(&DangerousConfig{})
	a.TTYPath = "/nonexistent/tty-for-test"

	if err := a.PromptCommand(Safe, "cat notes.txt", ""); err != nil {
		t.Errorf("Safe read under default read_only = %v, want allowed", err)
	}
	if err := a.PromptCommand(SystemWrite, "echo x >> ~/.zshrc", ""); err == nil {
		t.Fatal("persistence-class write under default read_only must be denied")
	}
}

func TestDeny_NoTTY_BlocksEvenReads(t *testing.T) {
	// Explicit deny remains the strictest posture — including reads.
	a := newNoTTYApprover("deny")
	if err := a.PromptCommand(Safe, "ls -la", ""); err == nil {
		t.Fatal("explicit deny must block even Safe commands")
	}
}
