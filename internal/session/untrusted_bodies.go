package session

import "strings"

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
// wrapper. Regions are found by one pass over s with the fences
// (untrustedRegions), so a wrapper tag inside a fence body is part of that
// body, not a wrapper.
func UntrustedBodies(s string) []UntrustedBody {
	return regionsOfKind(s, false)
}

// UntrustedInputBodies returns every <untrusted_input_<hex nonce>> fence in
// s, in order. A fence ends only at the close tag carrying the same nonce.
// Its body is untrusted in the child, never the principal's words. Like
// UntrustedBodies, it reports the regions of one pass over s, so a fence tag
// inside a wrapper body is part of that body, not a fence.
func UntrustedInputBodies(s string) []UntrustedBody {
	return regionsOfKind(s, true)
}

// untrustedRegion is one region found by untrustedRegions, of either kind.
type untrustedRegion struct {
	UntrustedBody
	fence bool
}

func regionsOfKind(s string, fence bool) []UntrustedBody {
	var out []UntrustedBody
	for _, r := range untrustedRegions(s) {
		if r.fence == fence {
			out = append(out, r.UntrustedBody)
		}
	}
	return out
}

// untrustedRegions returns every untrusted region of s, wrappers and fences,
// in one pass: the earliest valid opener of either kind starts a region,
// which runs to its own nonce-paired close tag, or to the end of s when that
// is missing; the scan resumes after the region. An opener inside a region's
// body is never parsed as a region, so an unterminated opener nested in a
// region of the other kind cannot hide a real region that follows it.
// Regions never overlap.
func untrustedRegions(s string) []untrustedRegion {
	var out []untrustedRegion
	nextWrap := indexFrom(s, UntrustedWrapperPrefix, 0)
	nextFence := indexFrom(s, UntrustedInputPrefix, 0)
	off := 0
	for {
		if nextWrap >= 0 && nextWrap < off {
			nextWrap = indexFrom(s, UntrustedWrapperPrefix, off)
		}
		if nextFence >= 0 && nextFence < off {
			nextFence = indexFrom(s, UntrustedInputPrefix, off)
		}
		var (
			r  untrustedRegion
			ok bool
		)
		switch {
		case nextWrap < 0 && nextFence < 0:
			return out
		case nextFence < 0 || (nextWrap >= 0 && nextWrap < nextFence):
			if r, ok = wrapperAt(s, nextWrap); !ok {
				nextWrap = indexFrom(s, UntrustedWrapperPrefix, nextWrap+len(UntrustedWrapperPrefix))
				continue
			}
		default:
			if r, ok = fenceAt(s, nextFence); !ok {
				nextFence = indexFrom(s, UntrustedInputPrefix, nextFence+len(UntrustedInputPrefix))
				continue
			}
		}
		out = append(out, r)
		off = r.End
		if off >= len(s) {
			return out
		}
	}
}

// indexFrom returns the index of sub in s at or after from, or -1.
func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	if i := strings.Index(s[from:], sub); i >= 0 {
		return from + i
	}
	return -1
}

// wrapperAt parses the untrusted-content wrapper whose opener starts at
// start, reporting false when the text there is not a wrapper opener.
func wrapperAt(s string, start int) (untrustedRegion, bool) {
	const attr = ` source="`
	rest := s[start+len(UntrustedWrapperPrefix):]
	n := 0
	for n < len(rest) && isLowerHex(rest[n]) {
		n++
	}
	if n == 0 || !strings.HasPrefix(rest[n:], attr) {
		return untrustedRegion{}, false
	}
	nonce := rest[:n]
	label := rest[n+len(attr):]
	q := strings.Index(label, `">`)
	if q < 0 {
		return untrustedRegion{UntrustedBody: UntrustedBody{Body: label, Start: start, End: len(s)}}, true
	}
	bodyStart := start + len(UntrustedWrapperPrefix) + n + len(attr) + q + 2
	closeTag := "</untrusted_content_" + nonce + ">"
	end, body := len(s), s[bodyStart:]
	if c := strings.Index(s[bodyStart:], closeTag); c >= 0 {
		body = s[bodyStart : bodyStart+c]
		end = bodyStart + c + len(closeTag)
	}
	return untrustedRegion{UntrustedBody: UntrustedBody{Source: label[:q], Body: body, Start: start, End: end}}, true
}

// fenceAt parses the untrusted-input fence whose opener starts at start,
// reporting false when the text there is not a fence opener.
func fenceAt(s string, start int) (untrustedRegion, bool) {
	rest := s[start+len(UntrustedInputPrefix):]
	n := 0
	for n < len(rest) && isLowerHex(rest[n]) {
		n++
	}
	if n == 0 || n >= len(rest) || rest[n] != '>' {
		return untrustedRegion{}, false
	}
	bodyStart := start + len(UntrustedInputPrefix) + n + 1
	closeTag := "</untrusted_input_" + rest[:n] + ">"
	end, body := len(s), s[bodyStart:]
	if c := strings.Index(s[bodyStart:], closeTag); c >= 0 {
		body = s[bodyStart : bodyStart+c]
		end = bodyStart + c + len(closeTag)
	}
	return untrustedRegion{UntrustedBody: UntrustedBody{Body: body, Start: start, End: end}, fence: true}, true
}

// StripUntrustedBodies removes every untrusted-content wrapper and
// untrusted-input fence from s, leaving the text the principal wrote. Each
// removed region becomes a newline, so the text on either side cannot join
// into a new tag; an unterminated region removes everything to the end.
func StripUntrustedBodies(s string) string {
	return replaceUntrustedBodies(s, false)
}

// UnwrapUntrustedBodies replaces every untrusted-content wrapper and
// untrusted-input fence in s with its body, removing the tags: the
// principal's text together with the content in it. Each body sits on its
// own lines. Regions nested inside a body are kept as they appear there.
// The result carries untrusted text; use it only where untrusted text may
// steer the outcome (for example a local similarity search).
func UnwrapUntrustedBodies(s string) string {
	return replaceUntrustedBodies(s, true)
}

func replaceUntrustedBodies(s string, keepBody bool) string {
	regions := untrustedRegions(s)
	if len(regions) == 0 {
		return s
	}
	var b strings.Builder
	prev := 0
	for _, w := range regions {
		b.WriteString(s[prev:w.Start])
		b.WriteByte('\n')
		if keepBody {
			b.WriteString(w.Body)
			b.WriteByte('\n')
		}
		prev = w.End
	}
	b.WriteString(s[prev:])
	return b.String()
}
