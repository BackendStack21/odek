package session

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
)

// TaintOverflow is a fixed-size Bloom filter of the untrusted spans that a
// bounded span list or span store no longer holds (see
// internal/loop/taint_spans.go). Forgetting a span would fail open — its
// reuse would stop escalating — so every span a bound drops is added here
// instead, and a lookup that misses the spans still held consults it. A false
// positive only adds an escalation, which is the fail-closed direction; there
// are no false negatives.
//
// The filter never holds a raw value. Bit positions derive from the SHA-256
// of the span's hashed key, kind + NUL + TaintSpanHash(value): a span kept in
// the clear and the same span kept as its hash set the same bits, and the
// positions are stable across processes, so the filter can be persisted.
//
// A nil filter is empty. A filter whose length is neither 0 nor
// TaintOverflowBytes (a damaged or hand-edited session file) can no longer
// rule any span out, so it is treated as saturated: every lookup hits, and
// the next merge rewrites it as a full filter of the right size. The size is
// therefore part of the session file format; changing it would saturate
// every session written before.
//
// AddValue and AddHash modify the filter in place, so only its owner calls
// them: a filter it allocated itself or got from UnionTaintOverflow, which
// always returns a new one (the loop's span store keeps its filter that
// way). MergeTaintState and UnionTaintOverflow never modify their inputs,
// but a merged filter may share its backing array with an input, so it is
// only ever replaced, never modified in place.
type TaintOverflow []byte

const (
	// TaintOverflowBytes is the filter size: 2^20 bits, 128 KiB (about
	// 175 KB of base64 in a session file, written only once a span has been
	// dropped). With taintOverflowProbes = 4 the false-positive rate of one
	// lookup is (1 - e^(-4n/2^20))^4 for n dropped spans: about 2e-6 at
	// n = 10,000, 9e-4 at 50,000 and 1e-2 at 100,000. A gated call looks up a
	// handful of keys (host suffixes, URL and path prefixes), so even a
	// session that has dropped 50,000 spans escalates well under one
	// unrelated command in a hundred. A quarter of the size (32 KiB) would put
	// the 50,000 figure at about 8% per lookup, several tens of percent per
	// command, which an unattended surface turns into denials.
	TaintOverflowBytes  = 128 << 10
	taintOverflowBits   = TaintOverflowBytes * 8
	taintOverflowProbes = 4

	// TaintOverflowNearlyFull is the Fill at which the filter counts as
	// nearly full. With 4 probes a lookup of a span that was never recorded
	// hits when all 4 of its bits are set, so at fill p the false-positive
	// rate of one lookup is p^4: 2.6% at 0.4, roughly one unrelated gated
	// command in ten. A filter reaches it after about 134,000 dropped spans
	// (1 - e^(-4n/2^20) = 0.4). Past it, a hit found only in the filter is
	// labelled as possibly false (see internal/loop/taint_spans.go).
	TaintOverflowNearlyFull = 0.4
)

// TaintSpanHash returns the hex SHA-256 of a span value: the form a span
// that could carry a secret is persisted in (TaintSpan.Hash), and the key the
// overflow filter derives its bits from.
func TaintSpanHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// taintOverflowProbe returns the bit positions of the span kind/hash.
func taintOverflowProbe(kind, hash string) [taintOverflowProbes]uint32 {
	var buf [96]byte
	key := append(append(append(buf[:0], kind...), 0), hash...)
	sum := sha256.Sum256(key)
	var pos [taintOverflowProbes]uint32
	for i := range pos {
		pos[i] = binary.BigEndian.Uint32(sum[4*i:]) % taintOverflowBits
	}
	return pos
}

// saturatedTaintOverflow returns a filter with every bit set.
func saturatedTaintOverflow() TaintOverflow {
	f := make(TaintOverflow, TaintOverflowBytes)
	for i := range f {
		f[i] = 0xff
	}
	return f
}

// AddValue records the span kind/value.
func (f *TaintOverflow) AddValue(kind, value string) { f.AddHash(kind, TaintSpanHash(value)) }

// AddHash records the span persisted as kind/hash.
func (f *TaintOverflow) AddHash(kind, hash string) {
	switch len(*f) {
	case TaintOverflowBytes:
	case 0:
		*f = make(TaintOverflow, TaintOverflowBytes)
	default:
		*f = saturatedTaintOverflow()
		return
	}
	for _, p := range taintOverflowProbe(kind, hash) {
		(*f)[p>>3] |= 1 << (p & 7)
	}
}

// HasValue reports whether the span kind/value may have been recorded.
func (f TaintOverflow) HasValue(kind, value string) bool {
	if len(f) == 0 {
		return false
	}
	return f.HasHash(kind, TaintSpanHash(value))
}

// HasHash reports whether the span persisted as kind/hash may have been
// recorded.
func (f TaintOverflow) HasHash(kind, hash string) bool {
	switch len(f) {
	case 0:
		return false
	case TaintOverflowBytes:
	default:
		return true // saturated: see TaintOverflow
	}
	for _, p := range taintOverflowProbe(kind, hash) {
		if f[p>>3]&(1<<(p&7)) == 0 {
			return false
		}
	}
	return true
}

// Fill returns the fraction of the filter's bits that are set: 0 for an
// empty filter, 1 for a filter of the wrong size (it is treated as
// saturated). It counts every bit, so callers that look it up often cache
// it until the filter changes.
func (f TaintOverflow) Fill() float64 {
	switch len(f) {
	case 0:
		return 0
	case TaintOverflowBytes:
	default:
		return 1
	}
	set := 0
	for i := 0; i < TaintOverflowBytes; i += 8 {
		set += bits.OnesCount64(binary.LittleEndian.Uint64(f[i:]))
	}
	return float64(set) / taintOverflowBits
}

// Covers reports whether f already records everything g does, so that
// merging g into f would change nothing: g is empty or has no bit set, f is
// saturated (wrong size), or every bit set in g is set in f. A wrong-size g
// is covered only by a saturated f.
func (f TaintOverflow) Covers(g TaintOverflow) bool {
	switch {
	case len(g) == 0, len(f) != 0 && len(f) != TaintOverflowBytes:
		return true
	case len(g) != TaintOverflowBytes:
		return false
	case len(f) == 0:
		return !taintOverflowAny(g)
	}
	return taintOverflowCovers(f, g)
}

// UnionTaintOverflow returns a new filter holding every span of a and b, nil
// when neither holds any, and a saturated filter when either has the wrong
// size.
func UnionTaintOverflow(a, b TaintOverflow) TaintOverflow {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	if len(a) != 0 && len(a) != TaintOverflowBytes || len(b) != 0 && len(b) != TaintOverflowBytes {
		return saturatedTaintOverflow()
	}
	if len(a) == 0 {
		a, b = b, a
	}
	out := make(TaintOverflow, TaintOverflowBytes)
	var nonzero uint64
	for i := 0; i < TaintOverflowBytes; i += 8 {
		w := binary.LittleEndian.Uint64(a[i:])
		if len(b) != 0 {
			w |= binary.LittleEndian.Uint64(b[i:])
		}
		binary.LittleEndian.PutUint64(out[i:], w)
		nonzero |= w
	}
	if nonzero == 0 {
		return nil
	}
	return out
}

// TaintState is a session's untrusted-span state: the spans it holds and the
// overflow filter of the spans it no longer holds. Persist both
// (Session.TaintSpans and Session.TaintOverflow); keeping only the spans
// forgets what the filter records.
type TaintState struct {
	Spans    []TaintSpan
	Overflow TaintOverflow
}

// Empty reports whether st holds no span and no overflow record.
func (st TaintState) Empty() bool { return len(st.Spans) == 0 && len(st.Overflow) == 0 }

// Covers reports whether st already records everything other does, so that
// merging other into st would add nothing: st's filter covers other's, and
// each of other's spans is in st's list (same kind, value and hash) or in
// st's filter. A filter false positive can only make a span count as
// recorded, and such a span would match st's filter anyway.
func (st TaintState) Covers(other TaintState) bool {
	if !st.Overflow.Covers(other.Overflow) {
		return false
	}
	if len(other.Spans) == 0 {
		return true
	}
	type spanKey struct{ kind, value, hash string }
	have := make(map[spanKey]struct{}, len(st.Spans))
	for _, sp := range st.Spans {
		have[spanKey{sp.Kind, sp.Value, sp.Hash}] = struct{}{}
	}
	for _, sp := range other.Spans {
		if sp.Kind == "" || sp.Value == "" && sp.Hash == "" {
			continue // nothing a lookup could name
		}
		if _, ok := have[spanKey{sp.Kind, sp.Value, sp.Hash}]; ok {
			continue
		}
		if (sp.Value == "" || st.Overflow.HasValue(sp.Kind, sp.Value)) && (sp.Hash == "" || st.Overflow.HasHash(sp.Kind, sp.Hash)) {
			continue
		}
		return false
	}
	return true
}

// TaintState returns the session's untrusted-span state.
func (s *Session) TaintState() TaintState {
	if s == nil {
		return TaintState{}
	}
	return TaintState{Spans: s.TaintSpans, Overflow: s.TaintOverflow}
}

// SetTaintState stores st as the session's untrusted-span state.
func (s *Session) SetTaintState(st TaintState) {
	s.TaintSpans, s.TaintOverflow = st.Spans, st.Overflow
}
