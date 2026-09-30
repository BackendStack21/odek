package loop

import (
	"strings"
	"testing"
)

// ── Ledger fidelity and notice attribution ──────────────────────────

func TestRecordMutation_IndividualShellOutcomes(t *testing.T) {
	e := &Engine{}
	e.recordMutation("shell", `{"command":"echo a >> ~/.zshrc"}`, "")
	e.recordMutation("shell", `{"command":"echo failed >> ~/.profile"}`, "error: denied")
	e.recordMutation("shell", `{"command":"echo b >> ~/.profile"}`, "")
	if len(e.runMutations) != 2 {
		t.Fatalf("mutations=%v, want only the two successful writes", e.runMutations)
	}
}

func TestRecordMutation_ShellStdoutContainingErrorWordStillLedgered(t *testing.T) {
	// successful mutating command whose stdout mentions "error"
	// (build logs, JSON) stays in the ledger.
	e := &Engine{}
	args := `{"command":"python build.py out.bin"}`
	e.recordMutation("shell", args, `{"status":"ok","warnings":["error codes parsed"]}`)
	if len(e.runMutations) != 1 {
		t.Fatalf("runMutations = %v, want the mutation kept", e.runMutations)
	}
}

func TestRecordMutation_JsonToolFailureExcluded(t *testing.T) {
	e := &Engine{}
	e.recordMutation("write_file", `{"path":"/root/x","content":"x"}`, `{"error":"denied by configuration"}`)
	if len(e.runMutations) != 0 {
		t.Fatalf("failed write must not be ledgered: %v", e.runMutations)
	}
}

func TestReconcileFinalReply_NoticeCarriesUnpredictableRef(t *testing.T) {
	// the notice header includes a nonce the model cannot
	// pre-forge.
	e := &Engine{runMutations: []string{"shell: echo hook >> ~/.zshrc"}}
	out := e.reconcileFinalReply("Nothing was executed.")
	if !strings.Contains(out, "[ref ") {
		t.Fatalf("notice missing ref nonce:\n%s", out)
	}
	// A second run gets a fresh notice (and in practice a fresh nonce).
	if !strings.Contains(e.reconcileFinalReply("Nothing was executed."), "[ref ") {
		t.Fatal("reconcile must be repeatable")
	}
}
