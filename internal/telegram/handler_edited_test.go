package telegram

import (
	"testing"
)

// TestHandleUpdate_EditedMessage verifies that edited messages are routed
// to the same handler as regular messages.
func TestHandleUpdate_EditedMessage(t *testing.T) {
	var (
		capturedChatID int64
		capturedText   string
		capturedEdit   bool
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnTextMessage = func(chatID int64, messageID int, text string) (string, error) {
		capturedChatID = chatID
		capturedText = text
		// messageID should be set for edited messages too
		capturedEdit = (messageID > 0)
		return "ok", nil
	}

	// Simulate an edited_message update from Telegram.
	upd := Update{
		ID: 1,
		EditedMessage: &Message{
			ID:   42,
			Chat: &Chat{ID: 123},
			From: &User{ID: 456},
			Text: "edited content",
		},
	}

	h.HandleUpdate(upd)

	if capturedChatID != 123 {
		t.Errorf("chatID = %d, want 123", capturedChatID)
	}
	if capturedText != "edited content" {
		t.Errorf("text = %q, want %q", capturedText, "edited content")
	}
	if !capturedEdit {
		t.Error("edited message should be routed to OnTextMessage")
	}
}

// TestHandleUpdate_EditedMessageWithCommand verifies that edited commands
// are also routed through the command handler.
func TestHandleUpdate_EditedMessageWithCommand(t *testing.T) {
	var (
		capturedCmd  string
		capturedArgs string
	)
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)
	h := NewHandler(bot)
	h.OnCommand = func(chatID int64, messageID int, cmd string, args string) (string, error) {
		capturedCmd = cmd
		capturedArgs = args
		return "ok", nil
	}

	upd := Update{
		ID: 2,
		EditedMessage: &Message{
			ID:   99,
			Chat: &Chat{ID: 111},
			From: &User{ID: 222},
			Text: "/start arg1",
			Entities: []MessageEntity{
				{Type: "bot_command", Offset: 0, Length: 6},
			},
		},
	}

	h.HandleUpdate(upd)

	if capturedCmd != "start" {
		t.Errorf("cmd = %q, want %q", capturedCmd, "start")
	}
	if capturedArgs != "arg1" {
		t.Errorf("args = %q, want %q", capturedArgs, "arg1")
	}
}
