package session

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLoadRejectsSymlinkedSession(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(target, []byte(`{"id":"20260918-symlink-load","messages":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	id := "20260918-symlink-load"
	if err := os.Symlink(target, filepath.Join(dir, id+".json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("Load followed a symlink")
	}
}

func TestLoadRejectsFileThatExceedsCap(t *testing.T) {
	old := MaxSessionFileBytes
	MaxSessionFileBytes = 256
	t.Cleanup(func() { MaxSessionFileBytes = old })
	dir := t.TempDir()
	id := "20260918-load-cap"
	// The file is valid JSON but exceeds the configured load cap.
	data := `{"id":"` + id + `","messages":[{"role":"user","content":"` + strings.Repeat("x", 400) + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("Load accepted an oversized session")
	}
}

func TestLoadDoesNotBlockOnFIFO(t *testing.T) {
	dir := t.TempDir()
	id := "20260918-fifo-load"
	path := filepath.Join(dir, id+".json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := store.Load(id); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Load accepted FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Load blocked opening FIFO")
	}
}
