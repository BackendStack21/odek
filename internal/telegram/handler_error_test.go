package telegram

import (
	"fmt"
	"strings"
	"testing"
)

// TestHandleCommand_ErrorSentToUser verifies that when a command handler
// returns an error, the error message is sent to the user (not just logged).
func TestHandleCommand_ErrorSentToUser(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	h.OnCommand = func(chatID int64, messageID int, cmd string, args string) (string, error) {
		return "", fmt.Errorf("simulated command failure: %s", cmd)
	}

	upd := Update{
		ID: 1,
		Message: &Message{
			ID:   42,
			Chat: &Chat{ID: 123},
			From: &User{ID: 456},
			Text: "/start",
			Entities: []MessageEntity{
				{Type: "bot_command", Offset: 0, Length: 6},
			},
		},
	}

	h.HandleUpdate(upd)

	// Verify a sendMessage was sent with the error text.
	found := false
	for _, r := range rec.all() {
		if strings.Contains(r.Path, "sendMessage") {
			if strings.Contains(r.Body, "simulated command failure") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("error from command handler was not sent to user")
	}
}

// TestHandleCallback_ErrorSentToUser verifies that when a callback query
// handler returns an error, the error message is sent to the user.
func TestHandleCallback_ErrorSentToUser(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	h.OnCallbackQuery = func(chatID int64, data string) (string, error) {
		return "", fmt.Errorf("simulated callback failure: %s", data)
	}

	upd := Update{
		ID: 2,
		CallbackQuery: &CallbackQuery{
			ID:   "cq_test",
			From: &User{ID: 456},
			Message: &Message{
				Chat: &Chat{ID: 789},
			},
			Data: "test_data",
		},
	}

	h.HandleUpdate(upd)

	// Verify a sendMessage was sent with the error text.
	found := false
	for _, r := range rec.all() {
		if strings.Contains(r.Path, "sendMessage") {
			if strings.Contains(r.Body, "simulated callback failure") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("error from callback handler was not sent to user")
	}
}

// TestHandleCommand_ErrorNotSentOnSuccess verifies that successful commands
// still send only the response text (not double-sending).
func TestHandleCommand_ErrorNotSentOnSuccess(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	h.OnCommand = func(chatID int64, messageID int, cmd string, args string) (string, error) {
		return "ok response", nil
	}

	upd := Update{
		ID: 3,
		Message: &Message{
			ID:   99,
			Chat: &Chat{ID: 111},
			From: &User{ID: 222},
			Text: "/status",
			Entities: []MessageEntity{
				{Type: "bot_command", Offset: 0, Length: 7},
			},
		},
	}

	h.HandleUpdate(upd)

	// Count sendMessage calls — should be exactly 1 (for the response).
	sendCount := 0
	for _, r := range rec.all() {
		if strings.Contains(r.Path, "sendMessage") {
			sendCount++
		}
	}
	if sendCount != 1 {
		t.Errorf("expected 1 sendMessage, got %d (should not double-send on success)", sendCount)
	}
}
