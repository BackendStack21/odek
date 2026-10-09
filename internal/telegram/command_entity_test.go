package telegram

import "testing"

// A normal sentence that mentions a slash path/command gets a bot_command
// entity from Telegram at a non-zero offset. The message must still reach
// the text handler instead of being silently dropped.
func TestRED_MidTextCommandEntityDropsMessage(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	h := NewHandler(testBot(t, ts))
	h.Config.AllowAllUsers = true
	got := false
	h.OnTextMessage = func(int64, int, string, bool, int64) (string, error) { got = true; return "", nil }
	h.OnCommand = func(int64, int, string, string, int64) (string, error) { return "", nil }

	h.HandleUpdate(Update{ID: 1, Message: &Message{
		ID: 1, Chat: &Chat{ID: 1}, From: &User{ID: 2},
		Text:     "can you explain /etc/passwd please",
		Entities: []MessageEntity{{Type: "bot_command", Offset: 16, Length: 4}},
	}})
	if !got {
		t.Fatal("message with a mid-text bot_command entity was silently dropped")
	}
}

func TestIsCommand_Variants(t *testing.T) {
	cases := []struct {
		name string
		m    *Message
		want bool
	}{
		{"nil", nil, false},
		{"entity at zero", &Message{Text: "/new", Entities: []MessageEntity{{Type: "bot_command", Offset: 0, Length: 4}}}, true},
		{"entity mid text", &Message{Text: "see /etc now", Entities: []MessageEntity{{Type: "bot_command", Offset: 4, Length: 4}}}, false},
		{"prefix without entity", &Message{Text: "  /new"}, true},
		{"plain", &Message{Text: "hello"}, false},
	}
	for _, c := range cases {
		if got := c.m.IsCommand(); got != c.want {
			t.Errorf("%s: IsCommand = %v, want %v", c.name, got, c.want)
		}
	}
}
