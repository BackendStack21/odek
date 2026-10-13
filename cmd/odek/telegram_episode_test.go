package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
	"time"
)

// episodeStubLLM returns a DIFFERENT summary per call: two identical
// summaries would be collapsed by episode dedup (near-duplicate threshold),
// which would mask whether the second /new overwrote the first episode.
type episodeStubLLM struct {
	mu    sync.Mutex
	calls int
}

func (e *episodeStubLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	switch e.calls {
	case 1:
		return "The session fixed a parser bug in lexer.go, added a regression test, and released v1.2.3.", nil
	default:
		return "The session added telemetry in metrics.go and shipped it after verifying the counters.", nil
	}
}

// TestRED_TelegramResetProducesEpisode: a Telegram chat reuses one permanent
// session id ("tg-<chat>") and never calls OnSessionEnd, so Telegram
// conversations never produced an episode — they were invisible to recall on
// every other surface. /new archives the conversation, which IS its end, so
// that is where the episode must be extracted.
func TestRED_TelegramResetProducesEpisode(t *testing.T) {
	home := setupTestHome(t)
	dir := filepath.Join(home, ".odek", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	const chatID = int64(4242)
	sess := &session.Session{ID: "tg-4242", Turns: 4}
	sess.Messages = []session.Message{
		{Role: "user", Content: "fix the parser bug"},
		{Role: "assistant", Content: "patched lexer.go and added a test"},
		{Role: "user", Content: "ship it"},
		{Role: "assistant", Content: "tagged v1.2.3"},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	memDir := filepath.Join(home, ".odek", "memory")
	cfg := memory.DefaultMemoryConfig()
	cfg.Enabled = memory.BoolPtr(true)
	cfg.LLMExtract = memory.BoolPtr(true)
	cfg.ExtractOnEnd = memory.BoolPtr(true)
	cfg.ConsolidateOnEnd = memory.BoolPtr(false)
	mm := memory.NewMemoryManager(memDir, &episodeStubLLM{}, cfg)

	// The conversation is archived under a timestamped id; the episode must
	// be keyed on THAT id. Keying it on the reused "tg-<chat>" id would name
	// a session that no longer exists and would be overwritten by the next
	// /new, leaving a chat with at most one episode ever.
	sm := telegram.NewSessionManager(store, time.Hour)
	archiveID, err := sm.ArchiveAndDeleteID(chatID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	telegramEpisodeOnReset(mm, store, archiveID)

	data, err := os.ReadFile(filepath.Join(memDir, archiveID+".md"))
	if err != nil {
		t.Fatalf("no episode written for the archived Telegram conversation %s: %v", archiveID, err)
	}
	if !strings.Contains(string(data), "parser bug") {
		t.Errorf("episode does not summarise the conversation: %q", data)
	}

	// A second conversation in the same chat must add a SECOND episode, not
	// replace the first.
	sess2 := &session.Session{ID: "tg-4242", Turns: 4}
	sess2.Messages = []session.Message{
		{Role: "user", Content: "now add telemetry"},
		{Role: "assistant", Content: "added metrics.go"},
		{Role: "user", Content: "done?"},
		{Role: "assistant", Content: "yes, shipped"},
	}
	if err := store.Save(sess2); err != nil {
		t.Fatal(err)
	}
	archiveID2, err := sm.ArchiveAndDeleteID(chatID)
	if err != nil {
		t.Fatalf("archive 2: %v", err)
	}
	if archiveID2 == archiveID {
		t.Fatalf("second archive reused the first id %q", archiveID)
	}
	telegramEpisodeOnReset(mm, store, archiveID2)
	if _, err := os.Stat(filepath.Join(memDir, archiveID+".md")); err != nil {
		t.Errorf("the first conversation's episode was destroyed by the second /new: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, archiveID2+".md")); err != nil {
		t.Errorf("no episode for the second conversation: %v", err)
	}
}
