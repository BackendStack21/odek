package main

import (
	"testing"
	"time"
)

func chatWaiters(chatID int64) int {
	chatMetaMu.Lock()
	defer chatMetaMu.Unlock()
	if s := chatSlots[chatID]; s != nil {
		return s.waiters
	}
	return 0
}

// A busy chat must not leave a goroutine parked on its mutex after the
// probe times out: the only waiter left is the turn that holds the slot.
func TestRED_Telegram_ChatIsIdleLeavesNoParkedWaiter(t *testing.T) {
	resetChatMutexes()
	const chat = int64(4242)
	slot := pinChat(chat)
	slot.mu.Lock()
	if chatIsIdle(chat, 30*time.Millisecond) {
		t.Fatal("busy chat reported idle")
	}
	time.Sleep(20 * time.Millisecond)
	if w := chatWaiters(chat); w != 1 {
		t.Fatalf("waiters=%d after timed-out probe, want 1 (holder only)", w)
	}
	unpinChat(chat, slot)
	if chatMutexCount() != 0 {
		t.Fatal("slot leaked")
	}
}

func TestChatIsIdleWhenFreeAndReleasesSlot(t *testing.T) {
	resetChatMutexes()
	const chat = int64(4243)
	if !chatIsIdle(chat, 50*time.Millisecond) {
		t.Fatal("free chat reported busy")
	}
	if chatMutexCount() != 0 {
		t.Fatal("probe leaked the slot")
	}
}

func TestChatIsIdleBecomesTrueWhenTurnEndsWithinWait(t *testing.T) {
	resetChatMutexes()
	const chat = int64(4244)
	slot := pinChat(chat)
	slot.mu.Lock()
	go func() {
		time.Sleep(40 * time.Millisecond)
		unpinChat(chat, slot)
	}()
	if !chatIsIdle(chat, 2*time.Second) {
		t.Fatal("probe missed a turn that ended within the wait")
	}
}
