package session

import (
	"os"
	"strings"
	"testing"
)

func TestToolCallArgumentsRedactionDoesNotMutateCallerSlice(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var tc ToolCall
	tc.ID, tc.Type = "c1", "function"
	tc.Function.Name = "shell"
	tc.Function.Arguments = `{"command":"echo ` + redhuntSecret + `"}`
	shared := []ToolCall{tc}
	sess := &Session{ID: "20260101-dddddd", Messages: []Message{
		{Role: "system", Content: "s"},
		{Role: "assistant", ToolCalls: shared},
	}}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shared[0].Function.Arguments, redhuntSecret) {
		t.Fatalf("caller's tool call slice was mutated by persistence")
	}
	raw, _ := os.ReadFile(store.Path(sess.ID))
	if strings.Contains(string(raw), redhuntSecret) {
		t.Fatalf("secret persisted")
	}
}
