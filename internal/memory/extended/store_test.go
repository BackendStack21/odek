package extended

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAtomStoreRefresh(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	if err := s.Refresh(); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
}

func TestAtomSizeMissing(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	_, err := s.AtomSize("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6")
	if err == nil {
		t.Error("expected error for missing atom size")
	}
}

func TestAtomStoreAddRejectsInvalidID(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	if err := s.Add(MemoryAtom{ID: "../escape", Text: "x"}, 100); err == nil {
		t.Error("expected error for invalid atom id")
	}
}

func TestAtomStoreAddRejectsEmptyText(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	if err := s.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: ""}, 100); err == nil {
		t.Error("expected error for empty text")
	}
}

func TestAtomStoreGetMissing(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	_, err := s.Get("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6")
	if err == nil {
		t.Error("expected error getting missing atom")
	}
}

func TestAtomStoreRemoveMissing(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	if err := s.Remove("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6"); err == nil {
		t.Error("expected error removing missing atom")
	}
}

func TestAtomStorePinRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewAtomStore(dir)
	id := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6"
	if err := s.Add(MemoryAtom{ID: id, Text: "pin me"}, 100); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := s.Pin(id, true); err != nil {
		t.Fatalf("Pin failed: %v", err)
	}
	atom, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !atom.Pin {
		t.Error("expected atom to be pinned")
	}
	if err := s.Pin(id, false); err != nil {
		t.Fatalf("Unpin failed: %v", err)
	}
	atom, err = s.Get(id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if atom.Pin {
		t.Error("expected atom to be unpinned")
	}
}

// TestAtomStoreTruncatesAtRuneBoundary verifies that the maxChars truncation
// backs off to a rune boundary instead of splitting a multi-byte UTF-8
// character.
func TestAtomStoreTruncatesAtRuneBoundary(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	id := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6"
	// "€" is 3 bytes; maxChars=8 lands mid-rune without boundary back-off.
	if err := s.Add(MemoryAtom{ID: id, Text: strings.Repeat("€", 10)}, 8); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !utf8.ValidString(got.Text) {
		t.Errorf("expected valid UTF-8 after truncation, got %q", got.Text)
	}
	if got.Text != "€€" {
		t.Errorf("expected truncation to back off to 2 runes, got %q", got.Text)
	}
}
