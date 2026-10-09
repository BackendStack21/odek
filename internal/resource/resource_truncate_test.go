package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The 50KB truncation must not split a multi-byte rune.
func TestRED_LoadTruncationKeepsUTF8(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("a", 50*1024-1) + strings.Repeat("é", 100)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := NewFileResolver(root).Load(context.Background(), "big.txt")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("truncated content is invalid UTF-8 (cut inside a rune); tail bytes % x", []byte(out[len(out)-8:]))
	}
}

// ASCII content is cut at exactly the 50KB limit, with no rune backtracking.
func TestLoadTruncationASCIIIsExact(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("a", 60*1024)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := NewFileResolver(root).Load(context.Background(), "big.txt")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := strings.Repeat("a", 50*1024) + "\n... [truncated at 50KB]"
	if out != want {
		t.Fatalf("truncated ASCII content length %d, want %d", len(out), len(want))
	}
}
