package session

import (
	"encoding/binary"
	"hash/maphash"
)

// taintMergeSeed keys the span hashes MergeTaintState deduplicates by.
var taintMergeSeed = maphash.MakeSeed()

// TaintSpan is one span the agent loop recorded from untrusted content (see
// internal/loop/taint_spans.go): a URL, host, path or opaque token whose reuse
// in a later tool call is escalated to the principal. Value holds the span;
// a span that could carry a secret (every token, anything the redactor would
// change) is persisted as Hash, the hex SHA-256 of the value, instead, so the
// session file never stores it while a resumed run still recognises it.
type TaintSpan struct {
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Source string `json:"source,omitempty"`
}

const (
	// MaxTaintSpans bounds the spans a session keeps (the loop's own cap).
	// Spans past it are recorded in the overflow filter (TaintOverflow).
	MaxTaintSpans = 4096
	// MaxTaintSpanValueBytes bounds one persisted value; a longer span is
	// not kept in the list (a cut span would never match) but recorded in
	// the overflow filter.
	MaxTaintSpanValueBytes = 512
	// maxTaintSpanSourceBytes bounds the recorded source label.
	maxTaintSpanSourceBytes = 200
)

// MergeTaintState returns newer's spans followed by the spans of older it
// does not already hold, deduplicated, with source labels bounded and at most
// MaxTaintSpans entries — the newest kept — together with the union of both
// overflow filters. A span the list does not keep (past the cap, an over-long
// value, an entry with both or neither of value and hash) is added to the
// overflow filter instead of being forgotten; only an entry without a kind,
// which no lookup can name, is dropped. Neither input is modified.
//
// Every per-step save merges two nearly equal states, so the merge avoids
// work that cannot change the result: the leading entries of older that
// repeat newer entry for entry are known duplicates and are not looked up,
// and the result shares an input's filter when that filter already holds
// every bit of the other one. A merged filter may therefore share its
// backing array with an input: never modify one in place (AddValue,
// AddHash); merge or replace it instead.
func MergeTaintState(newer, older TaintState) TaintState {
	var out TaintState
	var owned bool
	out.Overflow, owned = mergeTaintOverflow(newer.Overflow, older.Overflow)
	if len(newer.Spans) == 0 && len(older.Spans) == 0 {
		return out
	}
	rest := older.Spans[repeatedTaintSpans(newer.Spans, older.Spans):]
	// seen maps a 64-bit hash of each span's key (kind, value and hash; the
	// source does not count) to the position of the first span with that
	// hash (newer, then rest). A hash match is confirmed against that span,
	// so a collision keeps both spans rather than dropping one. It costs no
	// allocation per span, and a third of the memory of a string-keyed map.
	at := func(i int32) *TaintSpan {
		if int(i) < len(newer.Spans) {
			return &newer.Spans[i]
		}
		return &rest[int(i)-len(newer.Spans)]
	}
	seen := make(map[uint64]int32, len(newer.Spans)+len(rest))
	var h maphash.Hash
	h.SetSeed(taintMergeSeed)
	out.Spans = make([]TaintSpan, 0, min(len(newer.Spans)+len(rest), MaxTaintSpans))
	pos := int32(-1)
	for _, list := range [][]TaintSpan{newer.Spans, rest} {
		for _, sp := range list {
			pos++
			if sp.Kind == "" {
				continue
			}
			h.Reset()
			h.WriteString(sp.Kind)
			h.WriteByte(0)
			h.WriteString(sp.Value)
			h.WriteByte(0)
			h.WriteString(sp.Hash)
			key := h.Sum64()
			if i, dup := seen[key]; !dup {
				seen[key] = pos
			} else if first := at(i); first.Kind == sp.Kind && first.Value == sp.Value && first.Hash == sp.Hash {
				continue
			}
			if len(out.Spans) == MaxTaintSpans || (sp.Value == "") == (sp.Hash == "") || len(sp.Value) > MaxTaintSpanValueBytes || len(sp.Hash) > 64 {
				if !owned {
					if (sp.Value == "" || out.Overflow.HasValue(sp.Kind, sp.Value)) && (sp.Hash == "" || out.Overflow.HasHash(sp.Kind, sp.Hash)) {
						continue // the shared filter already records it
					}
					out.Overflow, owned = append(TaintOverflow(nil), out.Overflow...), true
				}
				if sp.Value != "" {
					out.Overflow.AddValue(sp.Kind, sp.Value)
				}
				if sp.Hash != "" {
					out.Overflow.AddHash(sp.Kind, sp.Hash)
				}
				continue
			}
			if len(sp.Source) > maxTaintSpanSourceBytes {
				sp.Source = truncateUTF8(sp.Source, maxTaintSpanSourceBytes)
			}
			out.Spans = append(out.Spans, sp)
		}
	}
	if len(out.Spans) == 0 {
		out.Spans = nil
	}
	return out
}

// repeatedTaintSpanWindow bounds where in newer the first span of older is
// looked for: the spans recorded since older was saved come first in newer.
const repeatedTaintSpanWindow = 64

// repeatedTaintSpans returns how many leading entries of older repeat newer
// entry for entry (same kind, value and hash) from some position among the
// first repeatedTaintSpanWindow entries of newer. Each such entry is a
// duplicate of a newer entry, so a merge can skip it.
func repeatedTaintSpans(newer, older []TaintSpan) int {
	if len(older) == 0 {
		return 0
	}
	same := func(a, b TaintSpan) bool { return a.Kind == b.Kind && a.Value == b.Value && a.Hash == b.Hash }
	for d := 0; d < len(newer) && d < repeatedTaintSpanWindow; d++ {
		if !same(newer[d], older[0]) {
			continue
		}
		n := 1
		for n < len(older) && d+n < len(newer) && same(newer[d+n], older[n]) {
			n++
		}
		return n
	}
	return 0
}

// mergeTaintOverflow returns the union of a and b and whether the result is
// a new filter. When one input already holds every bit of the other (the
// same filter, an empty one, or a subset) that input is returned as is.
// Like UnionTaintOverflow, a wrong-size input gives a saturated filter and
// a union without any bit set is nil.
func mergeTaintOverflow(a, b TaintOverflow) (TaintOverflow, bool) {
	if len(a) != 0 && len(a) != TaintOverflowBytes || len(b) != 0 && len(b) != TaintOverflowBytes {
		return saturatedTaintOverflow(), true
	}
	if len(a) == 0 {
		a, b = b, a
	}
	if len(a) == 0 {
		return nil, false
	}
	if len(b) == 0 || &a[0] == &b[0] || taintOverflowCovers(a, b) {
		if !taintOverflowAny(a) {
			return nil, false
		}
		return a, false
	}
	if taintOverflowCovers(b, a) {
		return b, false
	}
	return UnionTaintOverflow(a, b), true
}

// taintOverflowCovers reports whether every bit set in sub is set in super;
// both have the right size.
func taintOverflowCovers(super, sub TaintOverflow) bool {
	for i := 0; i < TaintOverflowBytes; i += 8 {
		if binary.LittleEndian.Uint64(sub[i:])&^binary.LittleEndian.Uint64(super[i:]) != 0 {
			return false
		}
	}
	return true
}

// taintOverflowAny reports whether f, of the right size, has any bit set.
func taintOverflowAny(f TaintOverflow) bool {
	for i := 0; i < TaintOverflowBytes; i += 8 {
		if binary.LittleEndian.Uint64(f[i:]) != 0 {
			return true
		}
	}
	return false
}

// truncateUTF8 cuts s to at most n bytes on a rune boundary.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
