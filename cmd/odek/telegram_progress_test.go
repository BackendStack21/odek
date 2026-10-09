package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/telegram"
)

type fakeProgressAPI struct {
	mu      sync.Mutex
	edits   []string
	sends   []string
	editErr error
	block   chan struct{} // when non-nil, edits wait for it
}

func (f *fakeProgressAPI) EditMessageText(_ int64, _ int, text string, _ *telegram.SendOpts) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, text)
	return f.editErr
}

func (f *fakeProgressAPI) SendMessage(_ int64, text string, _ *telegram.SendOpts) (*telegram.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, text)
	return &telegram.Message{ID: 100 + len(f.sends)}, nil
}

// A slow Telegram edit must not stall the agent loop that reports progress.
func TestRED_Telegram_SubmitDoesNotBlockOnNetwork(t *testing.T) {
	api := &fakeProgressAPI{block: make(chan struct{})}
	p := newProgressBubble(api, 1, 2, 0)
	p.setMessageID(7)
	start := time.Now()
	for i := 0; i < 20; i++ {
		p.submit("text", "line")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("submit blocked for %v while an edit was in flight", d)
	}
	close(api.block)
	p.finish(true)
}

func TestProgressLatestWinsAndFinishFlushes(t *testing.T) {
	api := &fakeProgressAPI{block: make(chan struct{})}
	p := newProgressBubble(api, 1, 2, 0)
	p.setMessageID(7)
	p.submit("one", "l1")
	time.Sleep(50 * time.Millisecond) // worker picks up "one" and blocks in the edit
	p.submit("two", "l2")
	p.submit("three", "l3")
	close(api.block)
	p.finish(true)
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.edits) != 2 || api.edits[0] != "one" || api.edits[1] != "three" {
		t.Fatalf("edits=%v, want [one three]", api.edits)
	}
}

func TestProgressFinishWithoutFlushDiscardsPendingAndWaits(t *testing.T) {
	api := &fakeProgressAPI{}
	p := newProgressBubble(api, 1, 2, time.Hour) // throttle keeps the update pending
	p.setMessageID(7)
	p.submit("first", "l")
	time.Sleep(50 * time.Millisecond)
	p.submit("second", "l")
	p.finish(false)
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.edits) != 1 || api.edits[0] != "first" {
		t.Fatalf("edits=%v", api.edits)
	}
}

func TestProgressFloodFallsBackToNewMessages(t *testing.T) {
	api := &fakeProgressAPI{editErr: errors.New("Too Many Requests: retry after 5")}
	p := newProgressBubble(api, 1, 2, 0)
	p.setMessageID(7)
	p.submit("t1", "line1")
	time.Sleep(50 * time.Millisecond)
	p.submit("t2", "line2")
	p.finish(true)
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sends) != 2 || api.sends[0] != "line1" || api.sends[1] != "line2" {
		t.Fatalf("sends=%v edits=%v", api.sends, api.edits)
	}
	if p.messageID() == 7 {
		t.Fatal("message id not advanced to the fallback message")
	}
}

func TestProgressResetAndNoMessage(t *testing.T) {
	api := &fakeProgressAPI{}
	p := newProgressBubble(api, 1, 2, 0)
	p.submit("ignored", "l") // no message id yet
	p.setMessageID(7)
	p.reset()
	p.submit("ignored", "l")
	p.finish(true)
	if len(api.edits)+len(api.sends) != 0 {
		t.Fatalf("unexpected traffic: %v %v", api.edits, api.sends)
	}
	p.finish(true) // idempotent
}

// The bubble is cosmetic: a final edit stuck behind Telegram rate limiting
// must not hold back the answer beyond the finish bound.
func TestRED_Telegram_ProgressFinishBoundedWhenEditStalls(t *testing.T) {
	api := &fakeProgressAPI{block: make(chan struct{})}
	t.Cleanup(func() { close(api.block) })
	p := newProgressBubble(api, 1, 2, 0)
	p.setMessageID(7)
	p.submit("text", "line")
	start := time.Now()
	p.finish(true)
	if d := time.Since(start); d > progressFinishWait+time.Second {
		t.Fatalf("finish waited %v on a stalled edit, want at most about %v", d, progressFinishWait)
	}
}
