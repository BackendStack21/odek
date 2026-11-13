package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// r3PromptWait bounds how long a test waits for a prompt to be sent; it only
// limits failure, so it is generous for slow race-instrumented runs.
const r3PromptWait = 30 * time.Second

func r3Approve(t *testing.T, a *TelegramApprover, rec *requestRecorder, card string) {
	t.Helper()
	before := rec.count()
	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.ToolBatchClass, card, "") }()
	var body string
	for deadline := time.Now().Add(r3PromptWait); body == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if rec.count() > before {
			body = rec.last().Body
		}
	}
	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("no prompt")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The elided flag describes only the most recent batch card: an elided card
// approved without any SetTrustAll call (the loop withholds blanket trust
// when an unclassifiable tool is present) must not suppress trust for the
// next card shown whole, while an elided card still never grants it.
func TestRED_ElidedFlagDescribesOnlyLatestBatchCard(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	a := NewTelegramApprover(testBot(t, ts), 1, 0)
	long := "1. `shell` — `" + strings.Repeat("a", 9000) + "`\n2. `shell` — `ls`\n"
	r3Approve(t, a, rec, long)
	a.SetTrustAll(true)
	a.mu.Lock()
	got := a.trustAll
	a.mu.Unlock()
	if got {
		t.Fatal("an elided card must not grant trust-all")
	}
	a.SetTrustAll(false)
	// Approved without any SetTrustAll call in between.
	r3Approve(t, a, rec, long)
	r3Approve(t, a, rec, "1. `shell` — `ls`\n2. `shell` — `pwd`\n")
	a.SetTrustAll(true)
	a.mu.Lock()
	got = a.trustAll
	a.mu.Unlock()
	if !got {
		t.Fatal("a whole, approved batch must be granted trust-all after an earlier elided card")
	}
}
