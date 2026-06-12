package fsatomic

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFile_WritesContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	if err := WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteFile_Overwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	if err := WriteFile(path, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
}

// TestWriteFile_LeavesNoTempOnSuccess verifies the unique temp file is renamed
// away (no litter), which also confirms the temp naming doesn't collide with
// the target.
func TestWriteFile_LeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	if err := WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "data" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only [data], got %v", names)
	}
}

// TestWriteFile_ConcurrentSameTarget verifies concurrent writers to the same
// path don't clobber each other's temp file (the old fixed-".tmp" pattern
// could) — the final content must be a complete one of the writes, never torn.
func TestWriteFile_ConcurrentSameTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	payloads := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccc",
	}
	var wg sync.WaitGroup
	for _, p := range payloads {
		wg.Add(1)
		go func(data string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := WriteFile(path, []byte(data), 0644); err != nil {
					t.Errorf("WriteFile: %v", err)
					return
				}
			}
		}(p)
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, p := range payloads {
		if string(got) == p {
			ok = true
		}
	}
	if !ok {
		t.Errorf("final content %q is torn — not a complete write", got)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only the target file, got %d entries", len(entries))
	}
}
