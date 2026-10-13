package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/session"
)

type episodeStubLLM struct{ reply string }

func (e episodeStubLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	return e.reply, nil
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
	mm := memory.NewMemoryManager(memDir, episodeStubLLM{
		reply: "The session fixed a parser bug in lexer.go, added a test, and released v1.2.3.",
	}, cfg)

	telegramEpisodeOnReset(mm, store, chatID)

	data, err := os.ReadFile(filepath.Join(memDir, "tg-4242.md"))
	if err != nil {
		t.Fatalf("no episode written for the archived Telegram conversation: %v", err)
	}
	if !strings.Contains(string(data), "parser bug") {
		t.Errorf("episode does not summarise the conversation: %q", data)
	}
}
