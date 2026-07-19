package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReplHistory_Persist_CreatesRestricted verifies that the REPL history
// file is created with 0600 permissions.
func TestReplHistory_Persist_CreatesRestricted(t *testing.T) {
	origHome := os.Getenv("HOME")
	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", origHome)

	h := newReplHistory()
	path := filepath.Join(home, ".odek", historyFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	h.Load(path)
	h.Add("secret-api-key")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat history file: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("history file mode = %o, want 0600", fi.Mode().Perm())
	}
}

// TestReplHistory_Persist_HardensExisting verifies that an existing
// world-readable history file is chmod'd to 0600 on the next persist.
func TestReplHistory_Persist_HardensExisting(t *testing.T) {
	origHome := os.Getenv("HOME")
	home := t.TempDir()
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", origHome)

	path := filepath.Join(home, ".odek", historyFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := newReplHistory()
	h.Load(path)
	h.Add("new-secret")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat history file: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("history file mode = %o, want 0600", fi.Mode().Perm())
	}
}
