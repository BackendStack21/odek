package telegram

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

func TestRED_BatchCardAlwaysFitsTelegramLimit(t *testing.T) {
	kinds := map[string]string{
		"short":     "rm -rf /tmp/x1",
		"long":      "shell: " + strings.Repeat("echo `a` \\ b; ", 60),
		"multibyte": "shell: " + strings.Repeat("é`\\日", 40),
	}
	for name, line := range kinds {
		for _, n := range []int{1, 50, 800, 3000, 6000} {
			card := strings.TrimSuffix(strings.Repeat(line+"\n", n), "\n")
			txt, el := buildApprovalTextElided(danger.ToolBatchClass, card, strings.Repeat("é", 500))
			if len(txt) > telegramMaxMsgLen {
				t.Errorf("%s n=%d: len=%d > %d", name, n, len(txt), telegramMaxMsgLen)
			}
			if !utf8.ValidString(txt) {
				t.Errorf("%s n=%d: invalid UTF-8", name, n)
			}
			if n >= 800 {
				if !el {
					t.Errorf("%s n=%d: card must be reported elided", name, n)
				}
				if name != "long" && !strings.Contains(txt, "more entries not shown") {
					t.Errorf("%s n=%d: missing hidden-entries line", name, n)
				}
			}
		}
	}
}

func TestRED_BatchCardHiddenEntriesDoNotGrantTrustAll(t *testing.T) {
	card := strings.TrimSuffix(strings.Repeat("rm -rf /tmp/x1\n", 3000), "\n")
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	a := NewTelegramApprover(testBot(t, ts), 1, 0)
	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.ToolBatchClass, card, "") }()
	var body string
	for deadline := time.Now().Add(2 * time.Second); body == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		rec.mu.Lock()
		if len(rec.requests) > 0 {
			body = rec.requests[len(rec.requests)-1].Body
		}
		rec.mu.Unlock()
	}
	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("no prompt was sent")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	a.SetTrustAll(true)
	a.mu.Lock()
	got := a.trustAll
	a.mu.Unlock()
	if got {
		t.Fatal("approving a card with hidden entries must not grant trust-all")
	}
}
