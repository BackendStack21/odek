package bgproc

import (
	"bytes"
	"testing"
)

// A single write larger than the ring keeps only its tail, counts the rest as
// dropped, and allocates no more than the write plus one window of headroom.
func TestOutputRing_WriteLargerThanLimit(t *testing.T) {
	r := &outputRing{limit: 64}
	big := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1024 bytes
	if _, err := r.Write(big); err != nil {
		t.Fatal(err)
	}
	if got := string(r.buf); got != string(big[len(big)-64:]) {
		t.Fatalf("window = %q, want the last 64 bytes", got)
	}
	if r.dropped != int64(len(big)-64) {
		t.Fatalf("dropped = %d, want %d", r.dropped, len(big)-64)
	}
	if cap(r.store) > 4*len(big) {
		t.Fatalf("backing array cap %d is out of proportion to a %d-byte write", cap(r.store), len(big))
	}
}
