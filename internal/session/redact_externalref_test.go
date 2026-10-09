package session

import (
	"os"
	"strings"
	"testing"
)

func TestRED_ExternalRefURIRedactedAtPersistence(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "20260101-bbbbbb", Messages: []Message{{Role: "system", Content: "s"}}}
	if _, err := sess.AddExternalRefs(ExternalRef{Kind: "ci", URI: "https://ci.example/run?token=" + redhuntSecret, CreatedBy: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(store.Path(sess.ID))
	if strings.Contains(string(raw), redhuntSecret) {
		t.Fatalf("secret persisted in external ref uri")
	}
}
