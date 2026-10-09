package telegram

import "testing"

// A forwarded message crosses a trust boundary; its text must not execute
// as an operator command.
func TestRED_ForwardedCommandExecutes(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	h := NewHandler(testBot(t, ts))
	h.Config.AllowAllUsers = true
	called := false
	h.OnCommand = func(int64, int, string, string, int64) (string, error) { called = true; return "", nil }

	h.HandleUpdate(Update{ID: 1, Message: &Message{
		ID: 1, Chat: &Chat{ID: 1}, From: &User{ID: 2},
		Text:          "/restart",
		Entities:      []MessageEntity{{Type: "bot_command", Offset: 0, Length: 8}},
		ForwardOrigin: &ForwardOrigin{},
	}})
	if called {
		t.Fatal("forwarded text executed as an operator command")
	}
}

// Forwarded text still reaches the text handler, flagged as forwarded.
func TestForwardedCommandReachesTextHandlerFlagged(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	h := NewHandler(testBot(t, ts))
	h.Config.AllowAllUsers = true
	var gotText string
	var gotFwd bool
	h.OnTextMessage = func(_ int64, _ int, text string, fwd bool, _ int64) (string, error) {
		gotText, gotFwd = text, fwd
		return "", nil
	}
	h.HandleUpdate(Update{ID: 1, Message: &Message{
		ID: 1, Chat: &Chat{ID: 1}, From: &User{ID: 2},
		Text:        "/new",
		ForwardDate: 1700000000,
	}})
	if gotText != "/new" || !gotFwd {
		t.Fatalf("forwarded text = %q forwarded=%v", gotText, gotFwd)
	}
}
