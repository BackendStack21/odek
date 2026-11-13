package loop

import (
	"slices"
	"strings"
	"unsafe"

	"github.com/BackendStack21/odek/internal/session"
)

// Scan tracks: what an observe derives from each message, kept per caller.
//
// The gate and every per-step save hand the store the whole history. For
// each message the scan needs the call it answers, its digest, and facts it
// collects from every message on every observe (the principal's text, the
// background jobs started). Deriving them reads every byte of the history,
// so a long session would pay for its whole length on every step. A track
// keeps, per message of the history its caller handed last, the fields that
// were read and what was derived from them. A message whose fields equal the
// kept ones, after the same prefix or answering the same call, reuses what
// was derived; anything else is derived again. A history that only grew
// therefore costs its new messages; a trimmed, compacted or edited one costs
// what changed after the first difference.
//
// The kept fields are the handed history's own strings, never copies of
// them. A history built from the same messages (the loop's context, a
// snapshot of the transcript) shares their bytes, and comparing a string
// with one that shares its bytes reads none of them. A history reloaded from
// the store holds equal values in fresh bytes: its first comparison reads
// them, and every entry that compared equal is then pointed at the handed
// strings (adopt), so later comparisons read nothing again and the track
// keeps no superseded copy of the history alive, only the strings of the
// history its caller handed last.
//
// Nothing derived here depends on the environment or on the store: what
// does (whether a call crosses the trust boundary, the scan decision) is
// judged again by every observe that needs it.

// taintSeen is what an observe derived from one message.
type taintSeen struct {
	// The fields the scan and its digest read: the strings of the message
	// last handed (calls is the track's own slice holding them).
	role, toolCallID, name, content string
	calls                           []session.ToolCall
	// pair is the assistant call a tool result answers, when paired is set.
	pair   session.ToolCall
	paired bool
	d      taintDigest
	// principal is the principal's own text in the message (wrappers
	// stripped), when isPrincipal is set.
	principal   string
	isPrincipal bool
	// bgJob is the job a bg_start result reports.
	bgJob string
}

// taintCompare accumulates what comparing kept fields with handed ones
// cost: the bytes read, and whether an equal field lies in other bytes than
// the kept one (moved), so the entry should take the handed strings.
type taintCompare struct {
	moved bool
	read  int64
}

// eq reports whether the kept field a equals the handed field b. Equal
// strings that share their bytes are told apart without reading them.
func (c *taintCompare) eq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	if unsafe.StringData(a) == unsafe.StringData(b) {
		return true
	}
	c.read += int64(len(a))
	if a != b {
		return false
	}
	c.moved = true
	return true
}

// same reports whether m carries exactly the fields e was derived from.
func (e *taintSeen) same(m *session.Message, c *taintCompare) bool {
	if len(e.calls) != len(m.ToolCalls) || !c.eq(e.role, m.Role) || !c.eq(e.toolCallID, m.ToolCallID) || !c.eq(e.name, m.Name) || !c.eq(e.content, m.Content) {
		return false
	}
	for j := range e.calls {
		a, b := &e.calls[j], &m.ToolCalls[j]
		if !c.eq(a.ID, b.ID) || !c.eq(a.Function.Name, b.Function.Name) || !c.eq(a.Function.Arguments, b.Function.Arguments) {
			return false
		}
	}
	return true
}

// answers reports whether e was derived with the pairing (call, paired).
func (e *taintSeen) answers(call session.ToolCall, paired bool, c *taintCompare) bool {
	if e.paired != paired {
		return false
	}
	p := &e.pair
	return c.eq(p.ID, call.ID) && c.eq(p.Type, call.Type) && c.eq(p.Function.Name, call.Function.Name) && c.eq(p.Function.Arguments, call.Function.Arguments)
}

// adopt points e at the strings of m, whose fields equal the kept ones, so
// e keeps no other copy of them alive and the next comparison reads none.
func (e *taintSeen) adopt(m *session.Message) {
	if e.isPrincipal && len(e.principal) == len(e.content) && unsafe.StringData(e.principal) == unsafe.StringData(e.content) {
		// No wrapper was stripped: the principal's text is the content.
		e.principal = m.Content
	}
	e.role, e.toolCallID, e.name, e.content = m.Role, m.ToolCallID, m.Name, m.Content
	copy(e.calls, m.ToolCalls)
}

// repair points the call e answers at the equal one issued holds (the
// history up to e is unchanged, so its pairing is) and records the calls e
// issues.
func (e *taintSeen) repair(issued map[string]session.ToolCall) {
	if e.paired {
		if call, ok := issued[e.toolCallID]; ok {
			e.pair = call
		}
	}
	e.issue(issued)
}

// call returns the call e answers, nil when the history holds none.
func (e *taintSeen) call() *session.ToolCall {
	if !e.paired {
		return nil
	}
	return &e.pair
}

// issue records the calls an assistant message issued, for pairing.
func (e *taintSeen) issue(issued map[string]session.ToolCall) {
	if e.role != "assistant" {
		return
	}
	for j := range e.calls {
		issued[e.calls[j].ID] = e.calls[j]
	}
}

// newTaintSeen derives the entry of m answering (call, paired), and the
// number of message bytes its digest read.
func newTaintSeen(m *session.Message, call session.ToolCall, paired bool) (taintSeen, int64) {
	e := taintSeen{role: m.Role, toolCallID: m.ToolCallID, name: m.Name, content: m.Content, pair: call, paired: paired}
	if len(m.ToolCalls) > 0 {
		// A copy: the caller may reuse the backing array of a live history.
		e.calls = slices.Clone(m.ToolCalls)
	}
	if paired && call.Function.Name == "bg_start" {
		// A copy: the id is a slice of the content, which it must not keep
		// alive once the entry takes another message's strings.
		e.bgJob = strings.Clone(bgStartedJob(m.Content))
	}
	if m.Role == "user" && !session.IsSyntheticUserName(m.Name) {
		e.principal, e.isPrincipal = stripUntrustedBodies(m.Content), true
	}
	pc := e.call()
	e.d = taintMessageDigest(m, pc)
	return e, taintMessageSize(m, pc)
}

// taintScanTrack is what one caller's last observe derived from the history
// it handed (seen) and the assistant calls that history issued, by id, at
// its end.
type taintScanTrack struct {
	seen   []taintSeen
	issued map[string]session.ToolCall
	// gen is the scan generation (scanGen) under which the observe that
	// stored the track decided every message in it, bgGen the generation of
	// the background facts (taintSpanStore.bgGen).
	gen, bgGen uint64
}

// taintSeenFor derives the entries of messages from prev, the track of the
// caller's last observe. lead is the number of leading messages equal to
// prev's, which that observe decided; digested counts the message bytes
// digested, compared the bytes read comparing kept fields with handed ones.
// prev must not be in use by any other observe: its storage is reused, and
// its entries are pointed at the strings of messages.
func taintSeenFor(messages []session.Message, prev taintScanTrack) (seen []taintSeen, issued map[string]session.ToolCall, lead int, digested, compared int64) {
	var c taintCompare
	moved := false
	for lead < len(messages) && lead < len(prev.seen) {
		e := &prev.seen[lead]
		c.moved = false
		if !e.same(&messages[lead], &c) {
			break
		}
		if c.moved {
			e.adopt(&messages[lead])
			moved = true
		}
		lead++
	}
	if lead == len(prev.seen) && prev.issued != nil {
		// The history extends the one prev holds: only its new messages
		// are read.
		seen, issued = prev.seen, prev.issued
		if moved {
			// The calls issued so far, and those results answer, take the
			// handed strings too.
			clear(issued)
			for i := range seen {
				seen[i].repair(issued)
			}
		}
	} else {
		seen = make([]taintSeen, 0, len(messages))
		issued = make(map[string]session.ToolCall)
	}
	for i := len(seen); i < len(messages); i++ {
		m := &messages[i]
		// Each tool result answers the nearest preceding call with its id
		// that an assistant message issued (providers may reuse ids across
		// turns). A call on any other message answers nothing: the engine
		// runs only what an assistant issued, and pairing with another
		// message's call would let a system message, which the coverage does
		// not count, change how a covered result is judged.
		var call session.ToolCall
		var paired bool
		if m.Role == "tool" {
			call, paired = issued[m.ToolCallID]
		}
		c.moved = false
		switch {
		case i < lead:
			seen = append(seen, prev.seen[i])
		case i < len(prev.seen) && prev.seen[i].same(m, &c) && prev.seen[i].answers(call, paired, &c):
			seen = append(seen, prev.seen[i])
			if c.moved {
				seen[i].adopt(m)
			}
		default:
			e, n := newTaintSeen(m, call, paired)
			seen = append(seen, e)
			digested += n
		}
		if paired {
			// The call is equal to the one the entry was derived with.
			seen[i].pair = call
		}
		seen[i].issue(issued)
	}
	return seen, issued, lead, digested, c.read
}

// taintBgJob is a background job a history started, and whether the call
// that started it crosses the trust boundary.
type taintBgJob struct {
	id       string
	tainting bool
}

// taintHistoryFacts collects what the scan reads from the whole history on
// every observe: the principal's messages, newest first so the latest
// instructions always fit taintPrincipalMaxBytes however long an early paste
// was; whether any tainting bg_start call is in view (on any message: fail
// closed); and the jobs the history saw started, in order. Whether a call
// taints is judged now, never kept.
func taintHistoryFacts(seen []taintSeen) (principal string, bgTainted bool, jobs []taintBgJob) {
	var b strings.Builder
	for i := len(seen) - 1; i >= 0 && b.Len() < taintPrincipalMaxBytes; i-- {
		e := &seen[i]
		if !e.isPrincipal {
			continue
		}
		text := e.principal
		if room := taintPrincipalMaxBytes - b.Len(); len(text) > room {
			text = text[:room]
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	for i := range seen {
		e := &seen[i]
		for j := range e.calls {
			tc := &e.calls[j]
			if tc.Function.Name == "bg_start" && !bgTainted && session.ToolCallTaintsEpisode(tc.Function.Name, tc.Function.Arguments) {
				bgTainted = true
			}
		}
		if e.bgJob != "" {
			jobs = append(jobs, taintBgJob{e.bgJob, session.ToolCallTaintsEpisode(e.pair.Function.Name, e.pair.Function.Arguments)})
		}
	}
	return b.String(), bgTainted, jobs
}
