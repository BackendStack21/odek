package session

import (
	"github.com/BackendStack21/odek/internal/redact"
	"strings"
	"testing"
)

func TestDecisionReceiptsAreBoundedRedactedAndOutsideMessages(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{{Role: "user", Content: "Task"}}, "test", "Task")
	if err != nil {
		t.Fatal(err)
	}
	secret := "test-decision-secret-unique"
	redact.RegisterSecret(secret)
	for range 130 {
		sess.Decisions = append(sess.Decisions, Decision{ID: "decision", Kind: "approval", Command: secret + strings.Repeat("x", 5000), State: "accepted"})
	}
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Decisions) != 128 || strings.Contains(loaded.Decisions[0].Command, secret) || len(loaded.Decisions[0].Command) > 4100 {
		t.Fatal("receipts were not bounded/redacted")
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "Task" {
		t.Fatal("receipts entered model transcript")
	}
}
