package session

import (
	"strings"
	"testing"
)

func TestExternalRefCleanURIUnchangedAndOthersRedacted(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "20260101-eeeeee", Messages: []Message{{Role: "system", Content: "s"}}}
	clean := "https://ci.example/run/42"
	if _, err := sess.AddExternalRefs(
		ExternalRef{Kind: "ci", URI: clean, CreatedBy: "op"},
		ExternalRef{Kind: "ci", URI: "https://ci.example/x?key=" + redhuntSecret, CreatedBy: "op"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ExternalRefs) != 2 || loaded.ExternalRefs[0].URI != clean {
		t.Fatalf("clean ref altered: %+v", loaded.ExternalRefs)
	}
	if strings.Contains(loaded.ExternalRefs[1].URI, redhuntSecret) {
		t.Fatalf("secret survived: %q", loaded.ExternalRefs[1].URI)
	}
}
