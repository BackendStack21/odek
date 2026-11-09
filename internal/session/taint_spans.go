package session

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
func MergeTaintState(newer, older TaintState) TaintState {
	out := TaintState{Overflow: UnionTaintOverflow(newer.Overflow, older.Overflow)}
	if len(newer.Spans) == 0 && len(older.Spans) == 0 {
		return out
	}
	// The key is the span without its source; a struct key costs no
	// allocation per span (every per-step save merges a few thousand).
	type spanKey struct{ kind, value, hash string }
	seen := make(map[spanKey]struct{}, len(newer.Spans)+len(older.Spans))
	out.Spans = make([]TaintSpan, 0, min(len(newer.Spans)+len(older.Spans), MaxTaintSpans))
	for _, list := range [][]TaintSpan{newer.Spans, older.Spans} {
		for _, sp := range list {
			if sp.Kind == "" {
				continue
			}
			key := spanKey{sp.Kind, sp.Value, sp.Hash}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if len(out.Spans) == MaxTaintSpans || (sp.Value == "") == (sp.Hash == "") || len(sp.Value) > MaxTaintSpanValueBytes || len(sp.Hash) > 64 {
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
