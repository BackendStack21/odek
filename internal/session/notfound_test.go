package session

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A missing session loads as a NotFoundError: a plain message without the
// store path, still matching os.ErrNotExist for callers that test for it.
// Exists answers the same question without loading.
func TestLoadMissingSessionIsNotFound(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("20260101-missing")
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.ID != "20260101-missing" {
		t.Fatalf("load missing: %v (%T)", err, err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("NotFoundError does not unwrap to os.ErrNotExist")
	}
	if strings.Contains(err.Error(), string(os.PathSeparator)) {
		t.Fatalf("message leaks a path: %q", err)
	}
	if store.Exists("20260101-missing") || store.Exists("../etc/passwd") {
		t.Fatal("Exists reports a missing or invalid id as present")
	}
	sess := &Session{ID: "20260101-present"}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if !store.Exists("20260101-present") {
		t.Fatal("Exists reports a saved session as absent")
	}
	// Something that is not a regular file at the session path is absent.
	if err := os.MkdirAll(store.path("20260101-adir"), 0700); err != nil {
		t.Fatal(err)
	}
	if store.Exists("20260101-adir") {
		t.Fatal("Exists reports a directory as a session")
	}
}
