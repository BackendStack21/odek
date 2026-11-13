package loop

import (
	"slices"
	"strings"

	"github.com/BackendStack21/odek/internal/session"
)

// Scan tracks: what an observe derives from each message, kept per caller.
//
// The gate and every per-step save hand the store the whole history. For
// each message the scan needs the call it answers, its digest, and facts it
// collects from every message on every observe (the principal's text, the
// background jobs started). Deriving them reads every byte of the history,
// so a long session would pay for its whole length on every step. A track
// keeps, per message of the history its caller handed last, a copy of the
// fields that were read and what was derived from them. A message whose
// fields equal the kept ones, after the same prefix or answering the same
// call, reuses what was derived; anything else is derived again. Strings are
// immutable: a field equal to the kept one holds the same bytes, and a
// history built from the same messages (the loop's context, a snapshot of
// the transcript) shares their bytes, so the comparison does not read them.
// A history that only grew therefore costs its new messages; a trimmed,
// compacted or edited one costs what changed after the first difference.
//
// Nothing derived here depends on the environment or on the store: what
// does (whether a call crosses the trust boundary, the scan decision) is
// judged again by every observe that needs it.

// taintSeen is what an observe derived from one message.
type taintSeen struct {
	// The fields the scan and its digest read, as last handed.
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

// same reports whether m carries exactly the fields e was derived from.
func (e *taintSeen) same(m *session.Message) bool {
	if e.role != m.Role || e.toolCallID != m.ToolCallID || e.name != m.Name || len(e.calls) != len(m.ToolCalls) || e.content != m.Content {
		return false
	}
	for j := range e.calls {
		a, b := &e.calls[j], &m.ToolCalls[j]
		if a.ID != b.ID || a.Function.Name != b.Function.Name || a.Function.Arguments != b.Function.Arguments {
			return false
		}
	}
	return true
}

// answers reports whether e was derived with the pairing (call, paired).
func (e *taintSeen) answers(call session.ToolCall, paired bool) bool {
	return e.paired == paired && e.pair == call
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
		e.bgJob = bgStartedJob(m.Content)
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
	// stored the track decided every message in it.
	gen uint64
}

// taintSeenFor derives the entries of messages from prev, the track of the
// caller's last observe. lead is the number of leading messages equal to
// prev's, which that observe decided; digested counts the message bytes
// digested. prev must not be in use by any other observe: its storage is
// reused.
func taintSeenFor(messages []session.Message, prev taintScanTrack) (seen []taintSeen, issued map[string]session.ToolCall, lead int, digested int64) {
	for lead < len(messages) && lead < len(prev.seen) && prev.seen[lead].same(&messages[lead]) {
		lead++
	}
	if lead == len(prev.seen) && prev.issued != nil {
		// The history extends the one prev holds: only its new messages
		// are read.
		seen, issued = prev.seen, prev.issued
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
		if i < lead || i < len(prev.seen) && prev.seen[i].same(m) && prev.seen[i].answers(call, paired) {
			seen = append(seen, prev.seen[i])
		} else {
			e, n := newTaintSeen(m, call, paired)
			seen = append(seen, e)
			digested += n
		}
		seen[i].issue(issued)
	}
	return seen, issued, lead, digested
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
