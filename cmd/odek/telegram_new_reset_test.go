package main

import (
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
)

// TestResetChatForNew_KeepsMutex is the regression test for the "/new deletes
// the per-chat mutex while a run holds it" bug. The scenario:
//
//  1. An in-flight handleChatMessage goroutine holds the per-chat mutex (run A).
//  2. The user sends /new, which calls resetChatForNew.
//  3. The next message calls getChatMutex again (run B).
//
// If resetChatForNew removes the mutex from chatMu (the old behavior), run B's
// getChatMutex LoadOrStores a *fresh* mutex and TryLocks it successfully — two
// runs execute concurrently for the same chat. This test asserts the mutex is
// preserved across the reset so run B still blocks on run A.
func TestResetChatForNew_KeepsMutex(t *testing.T) {
	withTempHome(t) // session.NewStore writes under a sandbox HOME

	chatID := int64(770001)
	chatMu = sync.Map{} // isolate from other tests

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	sm := telegram.NewSessionManager(store, time.Hour)
	bot := telegram.NewBot("test:token")
	handler := telegram.NewHandler(bot)

	// Run A acquires the per-chat mutex and holds it for the duration.
	muA := getChatMutex(chatID)
	if !muA.TryLock() {
		t.Fatal("run A could not acquire a fresh mutex")
	}
	defer muA.Unlock()

	// User sends /new while run A is still in flight.
	resetChatForNew(chatID, sm, handler, telegram.NewNopLogger())

	// Run B fetches the per-chat mutex. It MUST be the same instance run A
	// holds, so B cannot lock it — serialization is preserved.
	muB := getChatMutex(chatID)
	if muB != muA {
		t.Fatal("resetChatForNew orphaned the per-chat mutex: run B got a different mutex and would run concurrently with run A")
	}
	if muB.TryLock() {
		muB.Unlock()
		t.Fatal("run B was able to lock the per-chat mutex while run A holds it — concurrent same-chat runs possible")
	}
}

// TestResetChatForNew_ResetsApproverTrust verifies the reset still clears the
// approver's trust state (the legitimate purpose of /new's reset), so removing
// the mutex delete did not drop the approver reset.
func TestResetChatForNew_ResetsApproverTrust(t *testing.T) {
	withTempHome(t)

	chatID := int64(770002)
	chatMu = sync.Map{}

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	sm := telegram.NewSessionManager(store, time.Hour)
	bot := telegram.NewBot("test:token")
	handler := telegram.NewHandler(bot)

	approver := telegram.NewTelegramApprover(bot, chatID)
	handler.SetApprover(chatID, approver)

	// Should not panic and should reach the approver reset path.
	resetChatForNew(chatID, sm, handler, telegram.NewNopLogger())

	if handler.GetApprover(chatID) != approver {
		t.Fatal("approver unexpectedly replaced by resetChatForNew")
	}
}
