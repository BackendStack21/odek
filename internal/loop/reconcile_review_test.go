package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── Review fixes: ledger fidelity and notice attribution ────────────────

func TestRecordMutation_PerEntryParallelShell(t *testing.T) {
	// HIGH-003: one failed sibling must not erase its successful neighbors.
	e := &Engine{}
	output, _ := json.Marshal(map[string]any{"results": []map[string]any{
		{"command": "echo a >> ~/.zshrc", "error": ""},
		{"command": "false", "error": "exit status 1"},
		{"command": "echo b >> ~/.profile", "error": ""},
	}})
	args, _ := json.Marshal(map[string]any{"commands": []map[string]any{
		{"command": "echo a >> ~/.zshrc"},
		{"command": "false"},
		{"command": "echo b >> ~/.profile"},
	}})
	e.recordMutation("parallel_shell", string(args), string(output))
	if len(e.runMutations) != 2 {
		t.Fatalf("runMutations = %v, want the two successful writes only", e.runMutations)
	}
}

func TestRecordMutation_ShellStdoutContainingErrorWordStillLedgered(t *testing.T) {
	// HIGH-003: successful mutating command whose stdout mentions "error"
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
	// HIGH-002: the notice header includes a nonce the model cannot
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
