package main

import (
	"testing"
	"time"
)

// TestGetChatMutex_Cleanup verifies that chat mutexes can be dropped when
// they are no longer needed, preventing unbounded growth of chatSlots.
func TestGetChatMutex_Cleanup(t *testing.T) {
	resetChatMutexes()

	chatIDs := []int64{1, 2, 3, 4, 5}
	for _, id := range chatIDs {
		mu := getChatMutex(id)
		if mu == nil {
			t.Fatalf("getChatMutex(%d) returned nil", id)
		}
	}

	if count := chatMutexCount(); count != len(chatIDs) {
		t.Errorf("expected %d mutexes, got %d", len(chatIDs), count)
	}

	for _, id := range chatIDs {
		deleteChatMutex(id)
	}

	if count := chatMutexCount(); count != 0 {
		t.Errorf("expected 0 mutexes after cleanup, got %d", count)
	}

	for _, id := range chatIDs {
		mu := getChatMutex(id)
		if mu == nil {
			t.Fatalf("getChatMutex(%d) returned nil after cleanup", id)
		}
		if !mu.TryLock() {
			t.Errorf("fresh mutex for chat %d should be immediately lockable", id)
			continue
		}
		mu.Unlock()
	}
}

func TestGetChatMutex_LoadOrStore(t *testing.T) {
	resetChatMutexes()

	mu1 := getChatMutex(int64(42))
	mu2 := getChatMutex(int64(42))

	if mu1 != mu2 {
		t.Error("getChatMutex should return the same mutex for the same chat ID")
	}

	mu1.Lock()
	locked := make(chan bool, 1)
	go func() {
		mu2.Lock()
		locked <- true
		mu2.Unlock()
	}()
	select {
	case <-locked:
		t.Error("mu2 should not be able to lock while mu1 holds the lock")
	default:
	}
	mu1.Unlock()
	<-locked
}

func TestPinChat_ReapsIdleSlot(t *testing.T) {
	resetChatMutexes()
	id := int64(99)
	s := pinChat(id)
	s.mu.Lock()
	if chatMutexCount() != 1 {
		t.Fatalf("pinned slot missing from map")
	}
	unpinChat(id, s)
	if chatMutexCount() != 0 {
		t.Fatalf("idle slot was not reaped after last unpin, count=%d", chatMutexCount())
	}
}

func TestPinChat_KeepsSlotWhileWaiterQueued(t *testing.T) {
	resetChatMutexes()
	id := int64(100)
	a := pinChat(id)
	a.mu.Lock()

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		b := pinChat(id)
		close(started)
		b.mu.Lock()
		unpinChat(id, b)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("queued waiter did not pin")
	}
	if chatMutexCount() != 1 {
		t.Fatalf("slot dropped while a waiter was queued, count=%d", chatMutexCount())
	}
	unpinChat(id, a)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queued waiter did not acquire after unpin")
	}
	if chatMutexCount() != 0 {
		t.Fatalf("slot not reaped after both waiters released, count=%d", chatMutexCount())
	}
}
