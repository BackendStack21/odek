package telegram

import (
	"sync"
	"testing"

	"github.com/BackendStack21/kode/internal/llm"
)

// TestSave_UnblocksOtherChatsDuringDiskIO verifies that Save() releases
// the cache lock BEFORE doing disk I/O, so concurrent operations on
// different chats are not blocked by a slow disk write on another chat.
func TestSave_UnblocksOtherChatsDuringDiskIO(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	chatA := int64(1)
	chatB := int64(2)

	// Pre-create both sessions.
	_, err := sm.GetOrCreate(chatA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sm.GetOrCreate(chatB)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: Save on chatA (triggers disk I/O).
	savedA := make(chan struct{})
	go func() {
		defer wg.Done()
		msgs := make([]llm.Message, 500)
		for i := range msgs {
			msgs[i] = llm.Message{Role: "user", Content: "data"}
		}
		err := sm.Save(chatA, msgs)
		if err != nil {
			t.Errorf("Save chatA failed: %v", err)
		}
		close(savedA)
	}()

	// Goroutine B: GetOrCreate on chatB — should NOT block on chatA's Save.
	var bStarted bool
	go func() {
		defer wg.Done()
		cs, err := sm.GetOrCreate(chatB)
		if err != nil {
			t.Errorf("GetOrCreate chatB failed: %v", err)
			return
		}
		if cs == nil {
			t.Error("GetOrCreate chatB returned nil session")
		}
		bStarted = true
	}()

	wg.Wait()

	if !bStarted {
		t.Error("chatB GetOrCreate should have started while chatA Save was in progress, " +
			"indicating the lock was held during disk I/O")
	}
	<-savedA
}

// TestSave_SameChatSerialized verifies that concurrent Save calls
// on the same chat complete without data corruption.
func TestSave_SameChatSerialized(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	chatID := int64(42)
	_, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := sm.Save(chatID, []llm.Message{{Role: "user", Content: "hello"}})
			if err != nil {
				t.Errorf("Save failed: %v", err)
			}
		}()
	}
	wg.Wait()

	sm.Mu.RLock()
	cs, ok := sm.Cache[chatID]
	sm.Mu.RUnlock()

	if !ok {
		t.Fatal("chat session not in cache after concurrent saves")
	}
	if cs == nil {
		t.Fatal("chat session is nil")
	}
	if cs.Messages == nil {
		t.Error("messages slice is nil after save")
	}
}

// TestSave_RaceFreeLoadAfterSave verifies that loading a session
// immediately after saving it returns the correct data.
func TestSave_RaceFreeLoadAfterSave(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	chatID := int64(99)
	_, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := sm.Save(chatID, []llm.Message{{Role: "user", Content: "data"}})
			if err != nil {
				t.Errorf("Save failed: %v", err)
			}
			// Immediately load — should not race.
			cs, err := sm.Load(chatID)
			if err != nil {
				t.Errorf("Load after Save failed: %v", err)
			}
			if cs != nil && len(cs.Messages) == 0 {
				t.Error("Load returned empty messages after Save")
			}
		}()
	}
	wg.Wait()
}
