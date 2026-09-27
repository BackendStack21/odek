package telegram

import (
	"testing"
	"time"
)

// A duplicate approval callback (double-tap, Telegram retry) must never
// block HandleCallback: the reader of pr.resp consumes at most one action,
// so a second send on the buffered channel used to block the serialized
// update loop forever, freezing the whole bot.
func TestHandleCallback_DuplicateDoesNotBlock(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1)}
	a.mu.Lock()
	a.pending[id] = pr
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.HandleCallback(cbPrefixApprove+id, 0) // first — fills the buffer
		a.HandleCallback(cbPrefixApprove+id, 0) // duplicate — must not block
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("HandleCallback blocked on duplicate callback (deadlock)")
	}

	action := <-pr.resp
	if action != "approve" {
		t.Errorf("action = %q, want %q", action, "approve")
	}
}
