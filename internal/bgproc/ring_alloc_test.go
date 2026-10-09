package bgproc

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRED_Bgproc_FullRingWriteDoesNotAllocate(t *testing.T) {
	r := &outputRing{limit: 1024}
	chunk := bytes.Repeat([]byte("a"), 700)
	for i := 0; i < 10; i++ {
		_, _ = r.Write(chunk)
	}
	allocs := testing.AllocsPerRun(200, func() { _, _ = r.Write(chunk) })
	if allocs > 0 {
		t.Fatalf("steady-state ring write allocates %.1f times per call", allocs)
	}
}

// refRing is the original append-and-reslice behaviour, kept as an oracle.
type refRing struct {
	buf     []byte
	limit   int
	dropped int64
}

func (r *refRing) write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		cut := len(r.buf) - r.limit
		for i := 0; i < 3 && cut > 0 && !runeStart(r.buf[cut]); i++ {
			cut--
		}
		r.dropped += int64(cut)
		r.buf = r.buf[cut:]
	}
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }

func TestOutputRing_MatchesReferenceUnderRandomWrites(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []byte("ab\xc3\xa9\xe2\x82\xac\x80\n")
	r := &outputRing{limit: 257}
	ref := &refRing{limit: 257}
	for i := 0; i < 2000; i++ {
		p := make([]byte, rng.Intn(600))
		for j := range p {
			p[j] = alphabet[rng.Intn(len(alphabet))]
		}
		_, _ = r.Write(p)
		ref.write(p)
		if !bytes.Equal(r.buf, ref.buf) || r.dropped != ref.dropped {
			t.Fatalf("diverged at write %d: dropped %d vs %d, len %d vs %d", i, r.dropped, ref.dropped, len(r.buf), len(ref.buf))
		}
		if r.size() != ref.dropped+int64(len(ref.buf)) {
			t.Fatalf("size mismatch at %d", i)
		}
	}
}
