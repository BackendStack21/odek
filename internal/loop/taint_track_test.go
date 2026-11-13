package loop

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// trackEntryEqual compares what was derived from a message (the cached
// fields aside, which equal the message whenever the entry is current).
func trackEntryEqual(a, b *taintSeen) bool {
	return a.d == b.d && a.paired == b.paired && a.pair == b.pair && a.principal == b.principal &&
		a.isPrincipal == b.isPrincipal && a.bgJob == b.bgJob && a.same(&session.Message{
		Role: b.role, ToolCallID: b.toolCallID, Name: b.name, Content: b.content, ToolCalls: b.calls})
}

// trackMutate changes h the ways a caller's history changes between
// observes: it grows, trimming drops a group or rewrites a result, an
// engine message is edited or inserted, a call's arguments change in place
// in a shared backing array, a reused id re-pairs a result.
func trackMutate(r *rand.Rand, c covDiffCorpus, h []session.Message) []session.Message {
	switch r.IntN(8) {
	case 0, 1: // grows
		return append(h, c.history(r)[2:]...)
	case 2: // trimming drops the oldest group after the head
		for i := 2; i < len(h); i++ {
			if h[i].Role == "assistant" && len(h[i].ToolCalls) > 0 {
				j := i + 1
				for j < len(h) && h[j].Role == "tool" {
					j++
				}
				return append(h[:i:i], h[j:]...)
			}
		}
	case 3: // trimming rewrites a large result in place
		for i := range h {
			if h[i].Role == "tool" && r.IntN(2) == 0 {
				h[i].Content = fmt.Sprintf(session.TrimmedUntrustedMarker, len(h[i].Content))
				return h
			}
		}
	case 4: // an engine message in the middle is edited
		for i := len(h) / 2; i < len(h); i++ {
			if h[i].Role == "system" {
				h[i].Content += " refreshed"
				return h
			}
		}
		return slices.Insert(h, len(h)/2, session.Message{Role: "system", Content: "evidence"})
	case 5: // a call's arguments change in the shared backing array
		for i := range h {
			if h[i].Role == "assistant" && len(h[i].ToolCalls) > 0 {
				h[i].ToolCalls[0].Function.Arguments = `{"url":"https://changed.example/"}`
				return h
			}
		}
	case 6: // a reused id: a later assistant call takes over a result's id
		for i := range h {
			if h[i].Role == "tool" {
				call := covCall(h[i].ToolCallID, "read_file", map[string]string{"path": "README.md"})
				return slices.Insert(h, i, session.Message{Role: "assistant", ToolCalls: []session.ToolCall{call}})
			}
		}
	default: // a system message carrying a call with a result's id
		for i := range h {
			if h[i].Role == "tool" {
				return slices.Insert(h, i, c.systemCall(r, h[i].ToolCallID))
			}
		}
	}
	return append(h, session.Message{Role: "user", Content: "more " + c.text(r)})
}

// What a track reuses equals what deriving from nothing yields, whatever
// happened to the history between observes, and its leading run is exactly
// the prefix the two histories share.
func TestTaintTrack_ReuseEqualsFreshDerivation(t *testing.T) {
	r := rand.New(rand.NewPCG(20261113, 11))
	var jobSeq int
	for it := 0; it < 60; it++ {
		c := newCovDiffCorpus(r, &jobSeq)
		h := c.history(r)
		var track taintScanTrack
		var prevHistory []session.Message
		for step := 0; step < 12; step++ {
			seen, issued, lead, _ := taintSeenFor(h, track)
			fresh, _, _, _ := taintSeenFor(h, taintScanTrack{})
			if len(seen) != len(h) || len(fresh) != len(h) {
				t.Fatalf("iteration %d step %d: %d entries for %d messages", it, step, len(seen), len(h))
			}
			for i := range fresh {
				if !trackEntryEqual(&seen[i], &fresh[i]) {
					t.Fatalf("iteration %d step %d: message %d (%s) reused a stale derivation", it, step, i, h[i].Role)
				}
			}
			want := 0
			for want < len(h) && want < len(prevHistory) && trackMessageEqual(&h[want], &prevHistory[want]) {
				want++
			}
			if lead != want {
				t.Fatalf("iteration %d step %d: lead %d, the histories share %d leading messages", it, step, lead, want)
			}
			track = taintScanTrack{seen: seen, issued: issued}
			// The caller keeps its history and changes it in place.
			prevHistory = session.CloneMessages(h)
			for i := range prevHistory {
				prevHistory[i].ToolCalls = slices.Clone(prevHistory[i].ToolCalls)
			}
			h = trackMutate(r, c, h)
		}
	}
}

func trackMessageEqual(a, b *session.Message) bool {
	return a.Role == b.Role && a.ToolCallID == b.ToolCallID && a.Name == b.Name && a.Content == b.Content &&
		slices.EqualFunc(a.ToolCalls, b.ToolCalls, func(x, y session.ToolCall) bool {
			return x.ID == y.ID && x.Function.Name == y.Function.Name && x.Function.Arguments == y.Function.Arguments
		})
}

// A call whose arguments change in the caller's backing array after an
// observe is judged by its new arguments: the track holds a copy.
func TestTaintTrack_InPlaceCallChangeIsSeen(t *testing.T) {
	s := newTaintSpanStore()
	call := covCall("c1", "read_file", map[string]string{"path": "README.md"})
	h := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "read it"},
		{Role: "assistant", ToolCalls: []session.ToolCall{call}},
		{Role: "tool", ToolCallID: "c1", Content: "names https://inplace-evil.example/x"}}
	s.observe(h, sec16TaintEnv)
	if bgOriginHas(s, "inplace-evil.example") {
		t.Fatal("precondition: a workspace read was recorded")
	}
	// The same backing array now holds a network call.
	h[2].ToolCalls[0].Function.Name = "http_request"
	h[2].ToolCalls[0].Function.Arguments = `{"url":"https://inplace.example/"}`
	s.observe(h, sec16TaintEnv)
	if !bgOriginHas(s, "inplace-evil.example") {
		t.Fatal("a result whose call changed in place was judged by the call as first seen")
	}
}

// The gate's context and the saves' history are tracked apart: a trimmed
// context does not cost the saves their reuse, and the reverse.
func TestTaintTrack_GateAndSaveTracksIndependent(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	h := loopR3History(60)
	e.TaintSpanState(session.CloneMessages(h))
	store := e.spanStore()
	trimmed := append(h[:2:2], h[len(h)-6:]...)
	e.spanStore().observe(trimmed, currentTaintEnv())
	before := store.digestWork.Load()
	grown := append(session.CloneMessages(h), session.Message{Role: "user", Content: "next"})
	e.TaintSpanState(grown)
	if d := store.digestWork.Load() - before; d > int64(len("next"))+64 {
		t.Fatalf("a save after a trimmed gate digested %d bytes for one short message", d)
	}
	if got := len(store.gateTrack.seen); got != len(trimmed) {
		t.Fatalf("gate track holds %d messages, want %d", got, len(trimmed))
	}
}
