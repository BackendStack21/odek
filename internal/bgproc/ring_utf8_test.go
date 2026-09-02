package bgproc

// Regression tests for batch-3 finding B3-TOOLS-1: the output ring's
// rune-boundary walk used to collapse to cut=0 on invalid UTF-8 (binary)
// output, defeating the byte cap entirely and growing the "bounded" ring
// without limit. The cap must be unconditional; valid multibyte output
// must still be cut on a rune boundary.

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

func TestOutputRing_InvalidUTF8StillBounded(t *testing.T) {
	var ring outputRing
	ring.limit = 1000
	// 5000 bytes of pure continuation bytes — no valid rune start anywhere,
	// so the old walk ran all the way back to cut=0 and never trimmed.
	payload := bytes.Repeat([]byte{0x80}, 5000)
	if n, err := ring.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if len(ring.buf) > ring.limit+utf8.UTFMax {
		t.Fatalf("BUG B3-TOOLS-1: ring buf = %d bytes after one write, want <= %d — the byte cap must be unconditional on invalid UTF-8", len(ring.buf), ring.limit+utf8.UTFMax)
	}
	if got := ring.dropped + int64(len(ring.buf)); got != int64(len(payload)) {
		t.Fatalf("accounting drift: dropped(%d)+buf(%d) = %d, want %d", ring.dropped, len(ring.buf), got, len(payload))
	}
}

func TestOutputRing_ValidUTF8CutOnRuneBoundary(t *testing.T) {
	var ring outputRing
	ring.limit = 1000
	// 500 "日" runes (3 bytes each) — valid UTF-8, over the cap.
	payload := bytes.Repeat([]byte("日"), 500)
	if _, err := ring.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if len(ring.buf) > ring.limit+utf8.UTFMax {
		t.Fatalf("ring buf = %d bytes, want <= %d", len(ring.buf), ring.limit+utf8.UTFMax)
	}
	if !utf8.Valid(ring.buf) {
		t.Fatal("retained window is not valid UTF-8 — a multibyte character was split")
	}
	if got := ring.dropped + int64(len(ring.buf)); got != int64(len(payload)) {
		t.Fatalf("accounting drift: dropped(%d)+buf(%d) = %d, want %d", ring.dropped, len(ring.buf), got, len(payload))
	}
}
