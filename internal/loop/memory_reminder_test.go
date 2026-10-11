package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// TestRED_MemoryBlock_EndsWithAuthorityReminder: the memory block is a
// system message that follows the base prompt and its security pillar. It
// ends with a fixed reminder placed after the untrusted wrapper — inside
// the wrapper it would read as data.
func TestRED_MemoryBlock_EndsWithAuthorityReminder(t *testing.T) {
	var providerMessages []session.Message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []session.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		providerMessages = body.Messages
		fmt.Fprint(w, budgetFinalResponse("done", 1, 1))
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "system", nil, 0)
	engine.SetMemoryPromptFunc(func() string { return "MEM" })
	if _, _, err := engine.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	var memory string
	for _, m := range providerMessages {
		if m.Role == "system" && strings.Contains(m.Content, "\nMEM\n") {
			memory = m.Content
		}
	}
	if !strings.HasSuffix(memory, memoryAuthorityReminder) {
		t.Fatalf("memory block does not end with the authority reminder: %q", memory)
	}
	closeTag := strings.LastIndex(memory, "</untrusted_content_")
	if closeTag < 0 || closeTag > strings.Index(memory, memoryAuthorityReminder) {
		t.Fatalf("authority reminder is inside the untrusted wrapper: %q", memory)
	}
}

// TestMemoryBlock_ReminderSurvivesFedBackTurns (regression guard): a fed-back history
// carries last turn's memory block. The reminder must not stop the
// sanitizer from recognising it as engine-minted, or the block would be
// re-wrapped as persisted context and a second memory slot inserted.
func TestMemoryBlock_ReminderSurvivesFedBackTurns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	engine.SetMemoryPromptFunc(func() string { return "MEM" })

	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "turn 1"}}
	for turn := 1; turn <= 3; turn++ {
		if turn > 1 {
			msgs = append(msgs, session.Message{Role: "user", Content: fmt.Sprintf("turn %d", turn)})
		}
		var err error
		if _, msgs, err = engine.RunWithMessages(context.Background(), msgs); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}
	reminders := 0
	for _, m := range msgs {
		if m.Role != "system" {
			continue
		}
		if strings.Contains(m.Content, `source="persisted_system"`) {
			t.Fatalf("memory block re-wrapped as persisted context: %q", m.Content)
		}
		reminders += strings.Count(m.Content, memoryAuthorityReminder)
	}
	if reminders != 1 {
		t.Fatalf("authority reminders after 3 fed-back turns = %d, want 1 (history: %v)", reminders, summarize(msgs))
	}
}

// TestRED_MemoryBlock_ResumeInNewProcessUnwrapsCleanly: `odek continue`
// starts a new process whose minted registry is empty, so last run's memory
// block is foreign and gets re-wrapped as persisted context. The trailing
// reminder must not stop the foreign wrapper from being unwrapped first —
// otherwise the history gains a nested, neutralised tag and a second copy
// of the reminder sitting inside a data wrapper.
func TestRED_MemoryBlock_ResumeInNewProcessUnwrapsCleanly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	first := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	first.SetMemoryPromptFunc(func() string { return "MEM" })
	_, msgs, err := first.RunWithMessages(context.Background(),
		[]session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "turn 1"}})
	if err != nil {
		t.Fatal(err)
	}

	// A new process: nothing this history carries was minted here.
	mintedBoundaries.Lock()
	saved := mintedBoundaries.m
	mintedBoundaries.m = make(map[[32]byte]uint64)
	mintedBoundaries.Unlock()
	t.Cleanup(func() {
		mintedBoundaries.Lock()
		mintedBoundaries.m = saved
		mintedBoundaries.Unlock()
	})

	second := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 10, "sys", nil, 0)
	second.SetMemoryPromptFunc(func() string { return "MEM" })
	msgs = append(msgs, session.Message{Role: "user", Content: "turn 2"})
	if _, msgs, err = second.RunWithMessages(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role != "system" || !strings.Contains(m.Content, `source="persisted_system"`) {
			continue
		}
		if strings.Contains(m.Content, "untrusted·content") {
			t.Errorf("persisted memory block kept its foreign wrapper nested inside: %q", m.Content)
		}
		if strings.Contains(m.Content, memoryAuthorityReminder) {
			t.Errorf("authority reminder re-wrapped as data: %q", m.Content)
		}
	}
}

// TestRED_PersistedReminderOnly_NotEmptied: a persisted system message that
// is nothing but the reminder must not be trimmed to an empty system
// message, which some providers reject on resume.
func TestRED_PersistedReminderOnly_NotEmptied(t *testing.T) {
	engine := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	msgs := engine.sanitizePersistedSystemMessages(context.Background(), []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "system", Content: "\n\n" + memoryAuthorityReminder},
	})
	if strings.TrimSpace(msgs[1].Content) == "" {
		t.Fatal("persisted reminder-only system message was emptied")
	}
}
