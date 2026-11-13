package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// bgOriginCall builds a tool call.
func bgOriginCall(id, name, args string) session.ToolCall {
	tc := session.ToolCall{ID: id, Type: "function"}
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

// bgOriginStart is the group a bg_start call and its result form.
func bgOriginStart(callID, command, jobID string) []session.Message {
	return []session.Message{
		{Role: "assistant", ToolCalls: []session.ToolCall{bgOriginCall(callID, "bg_start", `{"command":"`+command+`"}`)}},
		{Role: "tool", ToolCallID: callID, Content: `{"job_id":"` + jobID + `","status":"running"}`},
	}
}

func bgOriginNotice(body string) session.Message {
	return session.Message{Role: "user", Name: "bg-notice", Content: defaultUntrustedWrap("bg", body)}
}

func bgOriginHas(s *taintSpanStore, host string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.spans[taintKey(taintKindHost, host)]
	return ok
}

// A clean local job's notice and output are not span sources, and stay so
// once trimming drops the bg_start that started the job: the store keeps
// what it learned about the job.
func TestTaintBgOrigin_KnownCleanJobSurvivesTrim(t *testing.T) {
	s := newTaintSpanStore()
	start := bgOriginStart("s1", "go test ./...", "bg_00000001")
	history := append([]session.Message{{Role: "user", Content: "run the tests in the background"}}, start...)
	s.observe(history, sec16TaintEnv)

	// The model context after trimming dropped the start group.
	trimmed := []session.Message{
		{Role: "user", Content: "run the tests in the background"},
		bgOriginNotice("[bg] 1 background job(s) finished since last check:\n- bg_00000001 `go test ./...` → exited (code 0) after 9s\n  tail: see https://pkg.example.dev/docs"),
		{Role: "assistant", ToolCalls: []session.ToolCall{bgOriginCall("o1", "bg_output", `{"job_id":"bg_00000001"}`)}},
		{Role: "tool", ToolCallID: "o1", Content: defaultUntrustedWrap("bg_output", "ok  \tpkg\t0.2s https://out.example.dev/x")},
	}
	s.observe(trimmed, sec16TaintEnv)
	if bgOriginHas(s, "pkg.example.dev") || bgOriginHas(s, "out.example.dev") {
		t.Fatal("a known clean job's output was recorded as untrusted")
	}
}

// Background content whose job this store never saw started is a span
// source: the origin cannot be established, so it fails closed.
func TestTaintBgOrigin_UnknownJobIsSource(t *testing.T) {
	cases := map[string][]session.Message{
		"notice naming an unknown job": {
			bgOriginNotice("- bg_0000000a `x` → exited (code 0) after 1s\n  tail: https://unknown-a.example/z"),
		},
		"notice naming no job": {
			bgOriginNotice("job finished: fetch https://unknown-b.example/z"),
		},
		"bg_output of an unknown job": {
			{Role: "assistant", ToolCalls: []session.ToolCall{bgOriginCall("o1", "bg_output", `{"job_id":"bg_0000000b"}`)}},
			{Role: "tool", ToolCallID: "o1", Content: "payload https://unknown-c.example/z"},
		},
		"bg_status without a job id": {
			{Role: "assistant", ToolCalls: []session.ToolCall{bgOriginCall("o1", "bg_status", `{}`)}},
			{Role: "tool", ToolCallID: "o1", Content: "payload https://unknown-d.example/z"},
		},
	}
	hosts := map[string]string{
		"notice naming an unknown job": "unknown-a.example",
		"notice naming no job":         "unknown-b.example",
		"bg_output of an unknown job":  "unknown-c.example",
		"bg_status without a job id":   "unknown-d.example",
	}
	for name, msgs := range cases {
		s := newTaintSpanStore()
		// A clean job is known, but not the one the content names.
		known := append(bgOriginStart("s1", "make build", "bg_00000001"), msgs...)
		s.observe(known, sec16TaintEnv)
		if !bgOriginHas(s, hosts[name]) {
			t.Errorf("%s: content of unknown origin was not recorded", name)
		}
	}
}

// A notice naming a clean job and an unknown one is a source: injected text
// can only add job ids, never hide the real one.
func TestTaintBgOrigin_AnyUnknownJobMakesSource(t *testing.T) {
	s := newTaintSpanStore()
	msgs := append(bgOriginStart("s1", "make build", "bg_00000001"),
		bgOriginNotice("- bg_00000002 `curl …` → exited (code 0) after 1s\n  tail: https://mixed.example/p\n- bg_00000001 `make build` → exited (code 0)"))
	s.observe(msgs, sec16TaintEnv)
	if !bgOriginHas(s, "mixed.example") {
		t.Fatal("a notice naming an unknown job was not recorded")
	}
}

// Once a tainting background job has run, every background content is a
// span source, even after trimming dropped that job's bg_start; the fact
// is exported and seeds a rebuilt engine.
func TestTaintBgOrigin_TaintingJobIsStickyAndPersisted(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	clean := bgOriginStart("s1", "make build", "bg_00000001")
	tainting := bgOriginStart("s2", "curl -s https://feed.example.org/x -o feed.json", "bg_00000002")
	history := append(append([]session.Message{{Role: "user", Content: "go"}}, clean...), tainting...)
	st := e.TaintSpanState(history)
	if !st.Background {
		t.Fatal("the tainting job was not exported")
	}

	// Trimming dropped the tainting start; the clean job's notice follows.
	later := append(append([]session.Message{{Role: "user", Content: "go"}}, clean...),
		bgOriginNotice("- bg_00000001 `make build` → exited (code 0)\n  tail: https://after-taint.example/x"))
	e.spanStore().observe(later, sec16TaintEnv)
	if !bgOriginHas(e.spanStore(), "after-taint.example") {
		t.Fatal("background content after a tainting job was not recorded")
	}

	// A rebuilt engine seeded with the state treats the clean job's
	// content as a source although its history never shows the tainting
	// start.
	rebuilt := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	ctx := WithTaintSpanSeed(context.Background(), session.TaintState{Background: st.Background})
	rebuilt.taintGate(ctx, later, nil, 0, nil, 1)
	if !bgOriginHas(rebuilt.spanStore(), "after-taint.example") {
		t.Fatal("the seeded background fact was lost")
	}
	if got := rebuilt.TaintSpanState(nil); !got.Background {
		t.Fatal("a seeded background fact was not exported again")
	}
}

// A tool result is judged by the call it answers: the nearest preceding call
// with its id. A provider reusing ids across turns must not let a later
// harmless call vouch for an earlier tainting call's result.
func TestTaintObserve_PairsResultWithNearestCall(t *testing.T) {
	s := newTaintSpanStore()
	msgs := []session.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []session.ToolCall{bgOriginCall("call_0", "http_request", `{"url":"https://fetched.example/a"}`)}},
		{Role: "tool", ToolCallID: "call_0", Content: "page names https://paired.example/p"},
		{Role: "assistant", ToolCalls: []session.ToolCall{bgOriginCall("call_0", "read_file", `{"path":"README.md"}`)}},
		{Role: "tool", ToolCallID: "call_0", Content: "local notes https://local-only.example/l"},
	}
	s.observe(msgs, sec16TaintEnv)
	if !bgOriginHas(s, "paired.example") {
		t.Fatal("the fetched result was judged by a later call that reused its id")
	}
	if bgOriginHas(s, "local-only.example") {
		t.Fatal("the workspace read was judged by an earlier call that reused its id")
	}
}

// Two results with the same content and call id but different calls are
// judged separately: the scanned digest includes the call.
func TestTaintObserve_DigestBindsTheCall(t *testing.T) {
	read := bgOriginCall("c", "read_file", `{"path":"README.md"}`)
	fetch := bgOriginCall("c", "http_request", `{"url":"https://x.example/"}`)
	m := session.Message{Role: "tool", ToolCallID: "c", Content: "same https://twin.example/t"}
	if taintMessageDigest(&m, &read) == taintMessageDigest(&m, &fetch) {
		t.Fatal("results of different calls share a scanned digest")
	}
	s := newTaintSpanStore()
	s.observe([]session.Message{{Role: "assistant", ToolCalls: []session.ToolCall{read}}, m}, sec16TaintEnv)
	s.observe([]session.Message{{Role: "assistant", ToolCalls: []session.ToolCall{fetch}}, m}, sec16TaintEnv)
	if !bgOriginHas(s, "twin.example") {
		t.Fatal("a fetched result was skipped as an already scanned workspace read")
	}
}
