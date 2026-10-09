package session

import (
	"os"
	"strings"
	"testing"
)

const redhuntSecret = "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghij"

func TestRED_ToolCallArgumentsRedactedAtPersistence(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var tc ToolCall
	tc.ID, tc.Type = "c1", "function"
	tc.Function.Name = "shell"
	tc.Function.Arguments = `{"command":"curl -H 'Authorization: Bearer ` + redhuntSecret + `' https://x"}`
	sess := &Session{ID: "20260101-aaaaaa", Messages: []Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{tc}},
	}}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(store.Path(sess.ID))
	if strings.Contains(string(raw), redhuntSecret) {
		t.Fatalf("secret persisted in tool call arguments")
	}
}
