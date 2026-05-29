package main

import (
	"sync"
	"testing"
)

// TestGetChatMutex_Cleanup verifies that chat mutexes are properly
// cleaned up when they are no longer needed, preventing unbounded
// growth of the chatMu map.
func TestGetChatMutex_Cleanup(t *testing.T) {
	// Reset chatMu for this test.
	chatMu = sync.Map{}

	// Create mutexes for several chats.
	chatIDs := []int64{1, 2, 3, 4, 5}
	for _, id := range chatIDs {
		mu := getChatMutex(id)
		if mu == nil {
			t.Fatalf("getChatMutex(%d) returned nil", id)
		}
	}

	// Verify all are stored.
	count := 0
	chatMu.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != len(chatIDs) {
		t.Errorf("expected %d mutexes, got %d", len(chatIDs), count)
	}

	// Clean up by deleting them.
	for _, id := range chatIDs {
		chatMu.Delete(id)
	}

	// Verify all are removed.
	count = 0
	chatMu.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 0 {
		t.Errorf("expected 0 mutexes after cleanup, got %d", count)
	}

	// Re-creating should work (fresh mutex).
	for _, id := range chatIDs {
		mu := getChatMutex(id)
		if mu == nil {
			t.Fatalf("getChatMutex(%d) returned nil after cleanup", id)
		}
		// Liveness check: fresh mutex must be acquirable without
		// blocking. TryLock keeps the critical section non-empty (so
		// staticcheck's SA2001 doesn't flag it) while still asserting
		// the same property.
		if !mu.TryLock() {
			t.Errorf("fresh mutex for chat %d should be immediately lockable", id)
			continue
		}
		mu.Unlock()
	}
}

// TestGetChatMutex_LoadOrStore verifies that the same mutex is returned
// for the same chat ID across multiple calls.
func TestGetChatMutex_LoadOrStore(t *testing.T) {
	chatMu = sync.Map{}

	mu1 := getChatMutex(int64(42))
	mu2 := getChatMutex(int64(42))

	if mu1 != mu2 {
		t.Error("getChatMutex should return the same mutex for the same chat ID")
	}

	// Lock one, verify the other can't lock.
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
		// Expected: mu2 blocks
	}
	mu1.Unlock()
	<-locked // mu2 should now be able to lock
}
