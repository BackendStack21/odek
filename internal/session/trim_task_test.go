package session

import (
	"fmt"
	"strings"
	"testing"
)

func TestRED_WriteTimeTrimKeepsOriginalTask(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "ORIGINAL-TASK"}}
	old := MaxSessionFileBytes
	MaxSessionFileBytes = 64 * 1024
	t.Cleanup(func() { MaxSessionFileBytes = old })
	big := strings.Repeat("x", 4096)
	for i := 0; i < 40; i++ {
		msgs = append(msgs, Message{Role: "assistant", Content: big}, Message{Role: "user", Content: fmt.Sprintf("follow-up %d", i)})
	}
	sess := &Session{ID: "20260101-cccccc", Messages: msgs}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range loaded.Messages {
		if m.Content == "ORIGINAL-TASK" {
			found = true
		}
	}
	if !found {
		t.Fatalf("original task message dropped by write-time trim (head protection)")
	}
}
