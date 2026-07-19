package telegram

import (
	"sync/atomic"
	"testing"
)

// TestHandleUpdate_RecoverFromPanic verifies that HandleUpdate catches panics
// in the handler callbacks and continues processing subsequent updates.
func TestHandleUpdate_RecoverFromPanic(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	// Set up a text handler that panics.
	var panicCaught atomic.Bool
	h.OnTextMessage = func(chatID int64, messageID int, text string, _ bool, _ int64) (string, error) {
		panic("simulated handler panic")
	}

	// Set up OnError to verify it fires.
	var errorFired atomic.Bool
	h.OnError = func(chatID int64, err error) {
		errorFired.Store(true)
	}

	// Wrap HandleUpdate to catch the panic (so the test doesn't crash).
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicCaught.Store(true)
				// This is what the test verifies — the panic should NOT
				// escape HandleUpdate. When the fix is applied, HandleUpdate
				// itself catches the panic, so this defer should never fire.
			}
		}()
		h.HandleUpdate(Update{
			ID: 1,
			Message: &Message{
				ID:   42,
				Chat: &Chat{ID: 123},
				From: &User{ID: 456},
				Text: "hello",
			},
		})
	}()

	if panicCaught.Load() {
		t.Error("HandleUpdate did not recover from panic — panic escaped to caller")
	}

	if !errorFired.Load() {
		t.Error("OnError was not called after panic recovery")
	}
}

// TestHandleUpdate_RecoverFromPanicCallback verifies panic recovery
// in callback query handlers.
func TestHandleUpdate_RecoverFromPanicCallback(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	h.OnCallbackQuery = func(chatID int64, data string, userID int64) (string, error) {
		panic("simulated callback panic")
	}

	var errorFired atomic.Bool
	h.OnError = func(chatID int64, err error) {
		errorFired.Store(true)
	}

	var panicEscaped atomic.Bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicEscaped.Store(true)
			}
		}()
		h.HandleUpdate(Update{
			ID: 2,
			CallbackQuery: &CallbackQuery{
				ID:   "cq_test",
				From: &User{ID: 456},
				Message: &Message{
					Chat: &Chat{ID: 789},
				},
				Data: "test_data",
			},
		})
	}()

	if panicEscaped.Load() {
		t.Error("HandleUpdate did not recover from callback handler panic")
	}

	if !errorFired.Load() {
		t.Error("OnError was not called after callback panic recovery")
	}
}

// TestHandleUpdate_RecoverFromPanicCommand verifies panic recovery
// in command handlers.
func TestHandleUpdate_RecoverFromPanicCommand(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.Config.AllowAllUsers = true // routing test

	h.OnCommand = func(chatID int64, messageID int, cmd string, args string, _ int64) (string, error) {
		panic("simulated command panic")
	}

	var errorFired atomic.Bool
	h.OnError = func(chatID int64, err error) {
		errorFired.Store(true)
	}

	var panicEscaped atomic.Bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicEscaped.Store(true)
			}
		}()
		h.HandleUpdate(Update{
			ID: 3,
			Message: &Message{
				ID:   99,
				Chat: &Chat{ID: 111},
				From: &User{ID: 222},
				Text: "/start",
				Entities: []MessageEntity{
					{Type: "bot_command", Offset: 0, Length: 6},
				},
			},
		})
	}()

	if panicEscaped.Load() {
		t.Error("HandleUpdate did not recover from command handler panic")
	}

	if !errorFired.Load() {
		t.Error("OnError was not called after command panic recovery")
	}
}
