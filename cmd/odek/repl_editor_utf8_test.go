package main

import (
	"strings"
	"testing"
)

// The raw-mode line editor reads the terminal byte-by-byte and used to
// insert each byte as rune(b): typed multibyte UTF-8 (é, 日, emoji) was
// stored as separate Latin-1-ish runes — "café" became "cafÃ©" in the
// submitted prompt. handleByte must accumulate bytes until a complete
// UTF-8 rune arrives.
func TestReplEditor_MultibyteInputPreserved(t *testing.T) {
	ed := newReplEditor("> ", nil)

	for _, b := range []byte("café") {
		if _, err := ed.handleByte(b); err != nil {
			t.Fatalf("handleByte(0x%02x): %v", b, err)
		}
	}
	if got := string(ed.line); got != "café" {
		t.Fatalf("line = %q, want %q (multibyte input corrupted)", got, "café")
	}

	for _, b := range []byte("日本語") {
		if _, err := ed.handleByte(b); err != nil {
			t.Fatalf("handleByte(0x%02x): %v", b, err)
		}
	}
	if got := string(ed.line); got != "café日本語" {
		t.Fatalf("line = %q, want %q", got, "café日本語")
	}
	if !strings.HasPrefix(string(ed.line), "caf") {
		t.Fatal("sanity")
	}
}
