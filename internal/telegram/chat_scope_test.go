package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

// Regression tests for M-2: Telegram session/plan commands must be scoped
// to the requesting chat. Cross-chat access must be rejected.

func TestResumeSession_CrossChatRejected(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const ownerChat int64 = 999
	const attackerChat int64 = 100

	if err := sm.Save(ownerChat, []llm.Message{{Role: "user", Content: "secret"}}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	_, err := sm.ResumeSession(attackerChat, "tg-999")
	if err == nil {
		t.Fatal("ResumeSession should reject a session belonging to a different chat")
	}
	if !strings.Contains(err.Error(), "different chat") {
		t.Errorf("error = %q, want 'different chat'", err)
	}
}

func TestListSessions_ChatScoped(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	for _, chatID := range []int64{111, 222, 333} {
		if err := sm.Save(chatID, []llm.Message{{Role: "user", Content: "msg"}}); err != nil {
			t.Fatalf("Save(%d) failed: %v", chatID, err)
		}
	}

	infos, err := sm.ListSessions(222, 0)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("ListSessions returned %d, want 1", len(infos))
	}
	if len(infos) == 1 && infos[0].ID != "tg-222" {
		t.Errorf("session ID = %q, want tg-222", infos[0].ID)
	}
}

func TestPruneSessions_ChatScoped(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	makeOldSession := func(chatID int64, id string) {
		sess := &session.Session{
			ID:        id,
			CreatedAt: time.Now().Add(-60 * 24 * time.Hour),
			UpdatedAt: time.Now().Add(-60 * 24 * time.Hour),
			Task:      id,
			Messages:  nil,
		}
		if err := st.Save(sess); err != nil {
			t.Fatalf("store.Save(%q) failed: %v", id, err)
		}
	}

	makeOldSession(1, "tg-1-old")
	makeOldSession(2, "tg-2-old")

	removed, err := sm.PruneSessions(1, 30)
	if err != nil {
		t.Fatalf("PruneSessions failed: %v", err)
	}
	if removed != 1 {
		t.Errorf("PruneSessions removed %d, want 1", removed)
	}

	// Chat 2's session must remain untouched.
	_, err = st.Load("tg-2-old")
	if err != nil {
		t.Errorf("chat 2 session should not have been pruned: %v", err)
	}
}

func TestReadPlan_ChatScoped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ownerDir := filepath.Join(tmp, ".odek", "plans", "chat111")
	if err := os.MkdirAll(ownerDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	os.WriteFile(filepath.Join(ownerDir, "secret.md"), []byte("secret plan"), 0644)

	_, _, err := ReadPlan(222, "secret")
	if err == nil {
		t.Fatal("ReadPlan should reject a plan belonging to a different chat")
	}
	if !strings.Contains(err.Error(), "no plan") && !strings.Contains(err.Error(), "no plans found") {
		t.Errorf("error = %q, want plan-not-found", err)
	}
}

func TestDeletePlan_ChatScoped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ownerDir := filepath.Join(tmp, ".odek", "plans", "chat111")
	if err := os.MkdirAll(ownerDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	os.WriteFile(filepath.Join(ownerDir, "secret.md"), []byte("secret plan"), 0644)

	_, err := DeletePlan(222, "secret")
	if err == nil {
		t.Fatal("DeletePlan should reject a plan belonging to a different chat")
	}
	if !strings.Contains(err.Error(), "no plan") && !strings.Contains(err.Error(), "no plans found") {
		t.Errorf("error = %q, want plan-not-found", err)
	}

	// Ensure the owner's file is still there.
	if _, err := os.Stat(filepath.Join(ownerDir, "secret.md")); os.IsNotExist(err) {
		t.Error("owner's plan file was deleted by cross-chat call")
	}
}
