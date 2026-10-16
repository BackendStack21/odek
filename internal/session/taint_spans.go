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
	MaxTaintSpans = 4096
	// MaxTaintSpanValueBytes bounds one persisted value; longer spans are
	// dropped rather than cut, since a cut span would never match.
	MaxTaintSpanValueBytes = 512
	// maxTaintSpanSourceBytes bounds the recorded source label.
	maxTaintSpanSourceBytes = 200
)

// MergeTaintSpans returns newer followed by the spans of older it does not
// already hold, deduplicated, with over-long values dropped, source labels
// bounded and at most MaxTaintSpans entries — the newest kept.
func MergeTaintSpans(newer, older []TaintSpan) []TaintSpan {
	if len(newer) == 0 && len(older) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(newer)+len(older))
	out := make([]TaintSpan, 0, min(len(newer)+len(older), MaxTaintSpans))
	for _, list := range [][]TaintSpan{newer, older} {
		for _, sp := range list {
			if len(out) == MaxTaintSpans {
				return out
			}
			if sp.Kind == "" || (sp.Value == "") == (sp.Hash == "") || len(sp.Value) > MaxTaintSpanValueBytes || len(sp.Hash) > 64 {
				continue
			}
			key := sp.Kind + "\x00" + sp.Value + "\x00" + sp.Hash
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(sp.Source) > maxTaintSpanSourceBytes {
				sp.Source = truncateUTF8(sp.Source, maxTaintSpanSourceBytes)
			}
			out = append(out, sp)
		}
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
