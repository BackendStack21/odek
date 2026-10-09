package session

import (
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/redact"
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

func promptMessages(n int) []Message {
	var ms []Message
	for i := 0; i < n; i++ {
		p := "prompt " + strings.Repeat("x", i+1)
		ms = append(ms, Message{Role: "user", Content: "c", PrincipalPrompt: &p})
		ms = append(ms, Message{Role: "assistant", Content: "a"})
	}
	return ms
}

func TestRED_Session_PrincipalPromptsNotRedactedTwice(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(promptMessages(20), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	p := "newest prompt"
	sess.Messages = append(sess.Messages, Message{Role: "user", Content: "c", PrincipalPrompt: &p})
	before := store.promptRedactions
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	if got := store.promptRedactions - before; got > 1 {
		t.Fatalf("second save redacted %d prompts, want only the new one", got)
	}
}

func TestRED_Session_PrincipalPromptMutatedInPlaceStillRedacted(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(promptMessages(5), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	secret := "in-place-mutated-prompt-secret-value"
	redact.RegisterSecret(secret)
	// Mutate an already-persisted prompt through its pointer.
	*sess.Messages[2].PrincipalPrompt = "leak " + secret
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("mutated prompt below the boundary reached disk unredacted")
	}
	*sess.Messages[4].PrincipalPrompt = "leak2 " + secret
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "x"})
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(store.path(sess.ID))
	if strings.Contains(string(raw), secret) {
		t.Fatal("mutated prompt leaked after append")
	}
}
