package bgproc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputPaginationMakesProgressWithSmallUnicodeLimits(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 4, 5} {
		r := &outputRing{limit: 1024}
		want := "a😀é中z"
		_, _ = r.Write([]byte(want))
		var cursor int64
		var got strings.Builder
		for cursor < int64(len(want)) {
			chunk, next := r.readFrom(cursor, limit)
			if next <= cursor || chunk == "" {
				t.Fatalf("limit=%d stalled at cursor=%d with unread output", limit, cursor)
			}
			if !utf8.ValidString(chunk) {
				t.Fatalf("limit=%d returned invalid UTF-8: %q", limit, chunk)
			}
			got.WriteString(chunk)
			cursor = next
		}
		if got.String() != want {
			t.Fatalf("limit=%d output=%q, want %q", limit, got.String(), want)
		}
	}
}

func TestOutputPaginationMakesProgressWithBinaryOutput(t *testing.T) {
	r := &outputRing{limit: 1024}
	want := string([]byte{0x80, 0x81, 0x82, 0xff, 'a'})
	_, _ = r.Write([]byte(want))
	var cursor int64
	var got strings.Builder
	for cursor < int64(len(want)) {
		chunk, next := r.readFrom(cursor, 1)
		if next <= cursor {
			t.Fatalf("binary pagination stalled at %d", cursor)
		}
		got.WriteString(chunk)
		cursor = next
	}
	if got.String() != want {
		t.Fatalf("output=%q, want %q", got.String(), want)
	}
}
