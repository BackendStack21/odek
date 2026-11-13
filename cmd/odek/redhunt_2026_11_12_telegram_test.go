package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/telegram"
)

// tgh1112Env isolates HOME and the per-chat globals for one chat turn and
// returns a session manager over the fixture store.
func tgh1112Env(t *testing.T, chatID int64) (*telegram.SessionManager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	deleteChatMutex(chatID)
	chatCancels.Delete(chatID)
	chatRunInfos.Delete(chatID)
	t.Cleanup(func() {
		deleteChatMutex(chatID)
		chatCancels.Delete(chatID)
		chatRunInfos.Delete(chatID)
		telegramTurnUsers.Delete(chatID)
		dropBGRuntimeForChat(chatID)
	})
	// Post-turn background work can still write under ~/.odek when the test
	// returns; retry the removal before t.TempDir's own cleanup runs.
	t.Cleanup(func() {
		odekDir := filepath.Join(home, ".odek")
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if err := os.RemoveAll(odekDir); err == nil || time.Now().After(deadline) {
				return
			}
		}
	})
	store, err := session.NewStoreWithDir(filepath.Join(home, ".odek", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return telegram.NewSessionManager(store, time.Hour), home
}

// tgh1112Bot is a Bot API stub that accepts every call.
func tgh1112Bot(t *testing.T) *telegram.Bot {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"chat":{"id":1}}}`))
	}))
	t.Cleanup(ts.Close)
	bot := telegram.NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.FileBaseURL = ts.URL + "/file"
	return bot
}

// The daily token budget bills each chat turn with
// runInfo.InputTokens + runInfo.OutputTokens (cmd/odek/telegram.go,
// billTelegramChatTurn call; the scheduled-run path in runTaskHeadless does
// the same). Since go-llm-sdk v1.1.0 the input count is UNCACHED input only:
// every provider's cached prompt tokens are moved to CacheReadTokens (and
// Anthropic reports cache reads and writes separately anyway). With prompt
// caching - the normal case for a long chat - most of each request is cache
// reads, so the budget is charged a small fraction of what the turn consumed
// and daily_token_budget stops bounding spend. docs/TELEGRAM.md: "actual
// token usage (input + output) is deducted from the daily budget".
func TestRED_TelegramDailyBudgetBillsCachedInput(t *testing.T) {
	const chatID = int64(88111201)
	sm, home := tgh1112Env(t, chatID)
	bot := tgh1112Bot(t)
	bot.SetDailyTokenBudget(1_000_000)

	// One request: 10,000 prompt tokens of which 9,000 were served from the
	// provider's prompt cache, plus 50 completion tokens.
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10000,"completion_tokens":50,"total_tokens":10050,` +
			`"prompt_tokens_details":{"cached_tokens":9000}}}`))
	}))
	defer llm.Close()

	resolved := config.ResolvedConfig{
		Model:           "test-model",
		BaseURL:         llm.URL,
		APIKey:          "sk-test",
		MaxIter:         4,
		ToolProgress:    "off",
		InteractionMode: "off",
	}
	resolved.Telegram.DailyTokenBudget = 1_000_000
	handleChatMessage(chatID, 5, 42, "hello", bot, telegram.NewHandler(bot), sm,
		resolved, "sys", telegram.NewNopLogger())

	usage := filepath.Join(home, ".odek", "telegram_token_usage_"+time.Now().UTC().Format("2006-01-02"))
	data, err := os.ReadFile(usage)
	if err != nil {
		t.Fatalf("turn was not billed at all: %v", err)
	}
	billed, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		t.Fatalf("usage counter %q: %v", data, err)
	}
	if billed < 10_050 {
		t.Fatalf("daily budget billed %d tokens for a turn that consumed 10050 (10000 input, 9000 of them cache reads, + 50 output): cached input is not counted", billed)
	}
}

func TestDailyBudgetTokensCountsCacheTraffic(t *testing.T) {
	got := dailyBudgetTokens(loop.IterationInfo{InputTokens: 1000, CacheReadTokens: 9000, CacheCreationTokens: 200, OutputTokens: 50})
	if got != 10250 {
		t.Fatalf("dailyBudgetTokens = %d, want 10250", got)
	}
}
