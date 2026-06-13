package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/telegram"
)

// newRecordingTestBot creates a telegram.Bot wired to a test server that
// records all sendMessage requests via a channel.
func newRecordingTestBot(t *testing.T) (*telegram.Bot, <-chan string) {
	t.Helper()
	recv := make(chan string, 10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sendMessage") {
			bodyBytes, _ := io.ReadAll(r.Body)
			r.Body.Close()
			var m map[string]interface{}
			if json.Unmarshal(bodyBytes, &m) == nil {
				if txt, ok := m["text"].(string); ok {
					select {
					case recv <- txt:
					default:
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(ts.Close)
	bot := telegram.NewBot("test:token")
	bot.BaseURL = ts.URL
	bot.FileBaseURL = ts.URL + "/file"
	return bot, recv
}

// TestHandleChatMessage_RecoversFromPanic verifies that handleChatMessage
// catches panics, releases the per-chat mutex, and sends an error message
// to the user.
func TestHandleChatMessage_RecoversFromPanic(t *testing.T) {
	chatID := int64(88001)
	messageID := 42

	// Ensure clean chat state.
	chatMu.Delete(chatID)
	chatCancels.Delete(chatID)
	chatRunInfos.Delete(chatID)

	// Recording bot that captures sendMessage calls.
	bot, msgCh := newRecordingTestBot(t)
	handler := telegram.NewHandler(bot)
	handler.Config = telegram.HandlerConfig{}

	resolved := config.ResolvedConfig{
		Model: "test-model",
		Telegram: telegram.TelegramConfig{
			DailyTokenBudget: 0, // disable budget check
		},
	}

	systemMessage := "You are a test assistant."

	// Trigger a nil-pointer panic by passing nil sessionManager.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handleChatMessage(
			chatID, messageID, 0, "test input",
			bot, handler, nil, // nil → panic at sessionManager.GetOrCreate
			resolved, systemMessage, telegram.NewNopLogger(),
		)
	}()
	wg.Wait()

	// Mutex must be released.
	mu := getChatMutex(chatID)
	if !mu.TryLock() {
		t.Fatal("per-chat mutex NOT released after panic — chat deadlocked")
	}
	mu.Unlock()

	// Error message must be sent.
	select {
	case msg := <-msgCh:
		if !strings.Contains(msg, "Internal error") {
			t.Errorf("error message should contain 'Internal error', got: %q", msg)
		}
		if !strings.Contains(msg, "/new") {
			t.Errorf("error message should mention /new, got: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error message")
	}

	chatMu.Delete(chatID)
	chatCancels.Delete(chatID)
	chatRunInfos.Delete(chatID)
}

// TestHandleChatMessage_RecoversFromPanic_MidRun verifies panic recovery
// deeper in the function (after session manager is valid).
func TestHandleChatMessage_RecoversFromPanic_MidRun(t *testing.T) {
	chatID := int64(88002)
	messageID := 99

	chatMu.Delete(chatID)
	chatCancels.Delete(chatID)
	chatRunInfos.Delete(chatID)

	bot, msgCh := newRecordingTestBot(t)
	handler := telegram.NewHandler(bot)
	handler.Config = telegram.HandlerConfig{}

	store := newTestSessionStore(t)
	sm := telegram.NewSessionManager(store, 1*time.Hour)

	resolved := config.ResolvedConfig{
		Model: "test-model",
		Telegram: telegram.TelegramConfig{
			DailyTokenBudget: 0,
		},
		Dangerous: danger.DangerousConfig{
			Approver: &mockApprover{},
		},
	}

	systemMessage := "You are a test assistant."

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handleChatMessage(
			chatID, messageID, 0, "test input",
			bot, handler, sm,
			resolved, systemMessage, telegram.NewNopLogger(),
		)
	}()
	wg.Wait()

	// Mutex must be released.
	mu := getChatMutex(chatID)
	if !mu.TryLock() {
		t.Fatal("per-chat mutex NOT released after panic (mid-run)")
	}
	mu.Unlock()

	// Check for error message (may or may not fire depending on panic point).
	select {
	case msg := <-msgCh:
		t.Logf("error message: %q", msg)
	case <-time.After(2 * time.Second):
		t.Log("no error message (acceptable for this panic path)")
	}

	chatMu.Delete(chatID)
	chatCancels.Delete(chatID)
	chatRunInfos.Delete(chatID)
}

// mockApprover implements danger.Approver for testing.
type mockApprover struct{}

func (m *mockApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	return nil // auto-approve
}

func (m *mockApprover) PromptOperation(op danger.ToolOperation) error {
	return nil // auto-approve
}
