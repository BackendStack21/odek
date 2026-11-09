package session

import (
	"sort"
	"strings"
)

// UntrustedInputPrefix opens the fence a parent puts around a delegated task
// derived from untrusted content: <untrusted_input_<hex nonce>>.
const UntrustedInputPrefix = "<untrusted_input_"

// UntrustedBody is one untrusted region of a text: an untrusted-content
// wrapper or an untrusted-input fence. Start and End are the byte offsets of
// the whole region, tags included. A region whose closing tag is missing
// (truncated content) runs to the end of the text.
type UntrustedBody struct {
	// Source is the wrapper's source label; it is empty for a fence and for
	// a wrapper whose label is not closed.
	Source string
	Body   string
	Start  int
	End    int
}

// UntrustedBodies returns every real untrusted-content wrapper in s, in
// order. A wrapper is `<untrusted_content_<hex nonce> source="<label>">` and
// ends only at the close tag carrying the same nonce, so a forged or nested
// close tag inside the body does not end it. Prose naming the tag is not a
// wrapper.
func UntrustedBodies(s string) []UntrustedBody {
	var out []UntrustedBody
	const attr = ` source="`
	off := 0
	for {
		i := strings.Index(s[off:], UntrustedWrapperPrefix)
		if i < 0 {
			return out
		}
		start := off + i
		rest := s[start+len(UntrustedWrapperPrefix):]
		n := 0
		for n < len(rest) && isLowerHex(rest[n]) {
			n++
		}
		if n == 0 || !strings.HasPrefix(rest[n:], attr) {
			off = start + len(UntrustedWrapperPrefix)
			continue
		}
		nonce := rest[:n]
		label := rest[n+len(attr):]
		q := strings.Index(label, `">`)
		if q < 0 {
			return append(out, UntrustedBody{Body: label, Start: start, End: len(s)})
		}
		bodyStart := start + len(UntrustedWrapperPrefix) + n + len(attr) + q + 2
		closeTag := "</untrusted_content_" + nonce + ">"
		end, body := len(s), s[bodyStart:]
		if c := strings.Index(s[bodyStart:], closeTag); c >= 0 {
			body = s[bodyStart : bodyStart+c]
			end = bodyStart + c + len(closeTag)
		}
		out = append(out, UntrustedBody{Source: label[:q], Body: body, Start: start, End: end})
		off = end
		if off >= len(s) {
			return out
		}
	}
}

// UntrustedInputBodies returns every <untrusted_input_<hex nonce>> fence in
// s, in order. A fence ends only at the close tag carrying the same nonce.
// Its body is untrusted in the child, never the principal's words.
func UntrustedInputBodies(s string) []UntrustedBody {
	var out []UntrustedBody
	off := 0
	for {
		i := strings.Index(s[off:], UntrustedInputPrefix)
		if i < 0 {
			return out
		}
		start := off + i
		rest := s[start+len(UntrustedInputPrefix):]
		n := 0
		for n < len(rest) && isLowerHex(rest[n]) {
			n++
		}
		if n == 0 || n >= len(rest) || rest[n] != '>' {
			off = start + len(UntrustedInputPrefix)
			continue
		}
		bodyStart := start + len(UntrustedInputPrefix) + n + 1
		closeTag := "</untrusted_input_" + rest[:n] + ">"
		end, body := len(s), s[bodyStart:]
		if c := strings.Index(s[bodyStart:], closeTag); c >= 0 {
			body = s[bodyStart : bodyStart+c]
			end = bodyStart + c + len(closeTag)
		}
		out = append(out, UntrustedBody{Body: body, Start: start, End: end})
		off = end
		if off >= len(s) {
			return out
		}
	}
}

// StripUntrustedBodies removes every untrusted-content wrapper and
// untrusted-input fence from s, leaving the text the principal wrote. Each
// removed region becomes a newline, so the text on either side cannot join
// into a new tag; an unterminated region removes everything to the end.
func StripUntrustedBodies(s string) string {
	bodies := append(UntrustedBodies(s), UntrustedInputBodies(s)...)
	if len(bodies) == 0 {
		return s
	}
	sort.Slice(bodies, func(i, j int) bool { return bodies[i].Start < bodies[j].Start })
	var b strings.Builder
	prev := 0
	for _, w := range bodies {
		if w.Start < prev {
			continue // nested in a region already removed
		}
		b.WriteString(s[prev:w.Start])
		b.WriteByte('\n')
		prev = w.End
	}
	b.WriteString(s[prev:])
	return b.String()
}
