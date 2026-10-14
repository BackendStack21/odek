package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

func TestRED_ResumeSessionReportsArchivedConversation(t *testing.T) {
	sm, st := setupTestSessionManager(t)
	const chat int64 = 4242

	// An older archived conversation to resume, and a live one to replace.
	old := &session.Session{
		ID: "tg-4242-20260101-000000", CreatedAt: time.Now().Add(-48 * time.Hour),
		UpdatedAt: time.Now().Add(-48 * time.Hour), Turns: 1,
		Messages: []session.Message{{Role: "user", Content: "old topic"}},
	}
	if err := st.Save(old); err != nil {
		t.Fatal(err)
	}
	if err := sm.Save(chat, []session.Message{{Role: "user", Content: "live topic"}}); err != nil {
		t.Fatal(err)
	}

	cs, archivedID, err := sm.ResumeSessionArchived(chat, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs == nil || archivedID == "" || archivedID == "tg-4242" || !strings.HasPrefix(archivedID, "tg-4242-") {
		t.Fatalf("replaced conversation not reported: cs=%v archivedID=%q", cs, archivedID)
	}
	got, err := st.Load(archivedID)
	if err != nil || got == nil || len(got.Messages) == 0 || got.Messages[0].Content != "live topic" {
		t.Fatalf("archive %q does not hold the replaced conversation: %v %v", archivedID, got, err)
	}
}

func TestRED_CheckChatMediaReadScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".odek", "media")
	if err := os.MkdirAll(filepath.Join(dir, "chat7"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "doc_chat8_a.pdf")
	own := filepath.Join(dir, "doc_chat7_a.pdf")
	sub := filepath.Join(dir, "chat7", "x.txt")
	for _, p := range []string{other, own, sub} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckChatMediaRead(other, 7); err == nil {
		t.Error("another chat's file allowed")
	}
	if err := CheckChatMediaRead(own, 7); err != nil {
		t.Errorf("own file refused: %v", err)
	}
	if err := CheckChatMediaRead(sub, 7); err != nil {
		t.Errorf("own subdirectory file refused: %v", err)
	}
	// A not-yet-existing file in the media dir is judged lexically.
	if err := CheckChatMediaRead(filepath.Join(dir, "doc_chat8_missing.pdf"), 7); err == nil {
		t.Error("missing other-chat file allowed")
	}
	if err := CheckChatMediaRead(filepath.Join(home, "notes.txt"), 7); err != nil {
		t.Errorf("non-media path refused: %v", err)
	}
	if err := CheckChatMediaRead(other, 0); err != nil {
		t.Errorf("no chat context must not restrict: %v", err)
	}
}

func TestRED_CheckChatMediaReadSpellings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".odek", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "voice_chat8_a.ogg")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "l.ogg")
	if err := os.Symlink(p, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	for _, path := range []string{"~/.odek/media/voice_chat8_a.ogg", filepath.Join(dir, "..", "media", "voice_chat8_a.ogg"), link} {
		if err := CheckChatMediaRead(path, 7); err == nil {
			t.Errorf("%q allowed for the wrong chat", path)
		}
		if err := CheckChatMediaRead(path, 8); err != nil {
			t.Errorf("%q refused for the owner: %v", path, err)
		}
	}
	if err := CheckChatMediaRead("", 7); err != nil {
		t.Errorf("empty path: %v", err)
	}
}
