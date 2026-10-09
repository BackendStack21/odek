package telegram

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// A system-initiated turn with no bound user must not show an approval that
// any chat member could answer: the prompt is denied without being sent.
func TestRED_ApproverRequireBoundUserDeniesUnbound(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	a.RequireBoundUser()
	a.SetTrustAll(true) // even a trust-all session must not approve unbound
	err := a.PromptCommand(danger.NetworkEgress, "curl https://evil.example/", "")
	if err == nil {
		t.Fatal("unbound approval on a bound-user approver was granted")
	}
	if !strings.Contains(err.Error(), "no bound user") {
		t.Errorf("error = %v, want a no-bound-user denial", err)
	}
	if rec.count() != 0 {
		t.Errorf("prompt was sent (%d requests); an unanswerable-by-design prompt must not be shown", rec.count())
	}

	// A stray pending entry with no user is never answerable either.
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1)}
	a.pending[id] = pr
	if !a.HandleCallback(cbPrefixApprove+id, 42) {
		t.Fatal("approval callback not recognized")
	}
	select {
	case <-pr.resp:
		t.Fatal("callback accepted for an unbound pending request")
	default:
	}
}

// With a bound user, RequireBoundUser changes nothing: only that user answers.
func TestApproverRequireBoundUserKeepsBoundFlow(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 111)
	a.RequireBoundUser()
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1), userID: 111}
	a.pending[id] = pr
	a.HandleCallback(cbPrefixApprove+id, 999)
	select {
	case <-pr.resp:
		t.Fatal("other user's callback accepted")
	default:
	}
	a.HandleCallback(cbPrefixApprove+id, 111)
	select {
	case got := <-pr.resp:
		if got != "approve" {
			t.Fatalf("action = %q", got)
		}
	default:
		t.Fatal("bound user's callback not accepted")
	}
}
