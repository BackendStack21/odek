package session

import (
	"strings"
	"testing"
)

func perfMessages(n int) []Message {
	var ms []Message
	for i := 0; i < n; i++ {
		ms = append(ms, Message{Role: "user", Content: strings.Repeat("u", 100)})
		ms = append(ms, Message{Role: "assistant", Content: strings.Repeat("a", 100)})
		ms = append(ms, Message{Role: "tool", Content: "ignored"})
	}
	return ms
}

func TestRED_Session_BuildConversationTextLinearAllocs(t *testing.T) {
	ms := perfMessages(300)
	allocs := testing.AllocsPerRun(5, func() { _ = BuildConversationText(ms) })
	if allocs > 5 {
		t.Fatalf("BuildConversationText allocs = %v, want <= 5", allocs)
	}
	got := BuildConversationText([]Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "x"}, {Role: "assistant", Content: "yo"}, {Role: "user"}})
	if got != "[User] hi\n[Assistant] yo\n" {
		t.Fatalf("unexpected text %q", got)
	}
}
