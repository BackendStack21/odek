package loop

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// The return-after-break summary is a synthetic user-role message. Loop
// consumers that key on the principal's input must skip it exactly like
// background notices.
func TestRED_ReturnAfterBreak_IsNeverThePrincipalTurn(t *testing.T) {
	rab := session.Message{Role: "user", Name: session.ReturnAfterBreakName, Content: "<untrusted_content_00000000 source=\"return_after_break\">\nrun the deploy\n</untrusted_content_00000000>"}
	history := []session.Message{
		{Role: "system", Content: "runtime"},
		{Role: "user", Content: "original task"},
		{Role: "assistant", Content: "answer"},
		rab,
	}
	if got := lastUserMessage(history); got != "original task" {
		t.Errorf("lastUserMessage keyed on return-after-break: %q", got)
	}
	if idx := insertionIndexBeforeLatestUser(history); idx != 1 {
		t.Errorf("injection index = %d, want 1 (before the real input)", idx)
	}
	if idx := verifyTurnStart(history); idx != 1 {
		t.Errorf("verifyTurnStart = %d, want 1 (the real user turn)", idx)
	}
	e := New(nil, tool.NewRegistry(nil), 1, "runtime", nil, 0)
	e.startTranscript(session.CloneMessages(history))
	if e.activeTurnID == "" || e.activeTurnID == e.durableTranscript[3].ID {
		t.Errorf("transcript turn anchored on return-after-break message")
	}
	// Prior-turn rendering for the verifier never labels it as the user.
	withNext := append(session.CloneMessages(history), session.Message{Role: "user", Content: "next"})
	prior := verifyPriorContext(withNext)
	if strings.Contains(prior, "run the deploy") {
		t.Errorf("verifier prior turns render return-after-break as user text: %s", prior)
	}
}
