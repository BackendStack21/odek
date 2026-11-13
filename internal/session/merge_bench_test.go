package session

import (
	"fmt"
	"strings"
	"testing"
)

// fullTaintState returns a state with MaxTaintSpans spans (a third hosts, a
// third URLs, a third hashed tokens) and a non-empty overflow filter. Every
// string is freshly allocated, as after a session file load.
func fullTaintState() TaintState {
	var st TaintState
	for i := 0; len(st.Spans) < MaxTaintSpans; i++ {
		src := strings.Clone(fmt.Sprintf("https://blog.example.net/post/%d", i/40))
		st.Spans = append(st.Spans,
			TaintSpan{Kind: "host", Value: fmt.Sprintf("h%d.example.org", i), Source: src},
			TaintSpan{Kind: "url", Value: fmt.Sprintf("https://h%d.example.org/p/%d", i, i), Source: src},
			TaintSpan{Kind: "token", Hash: TaintSpanHash(fmt.Sprint("tok", i)), Source: src})
	}
	st.Spans = st.Spans[:MaxTaintSpans]
	for i := 0; i < 20000; i++ {
		st.Overflow.AddValue("host", fmt.Sprintf("old%d.example", i))
	}
	return st
}

// cloneDeep copies st with every string reallocated.
func cloneDeep(st TaintState) TaintState {
	out := TaintState{Overflow: append(TaintOverflow(nil), st.Overflow...)}
	for _, sp := range st.Spans {
		out.Spans = append(out.Spans, TaintSpan{Kind: strings.Clone(sp.Kind), Value: strings.Clone(sp.Value), Hash: strings.Clone(sp.Hash), Source: strings.Clone(sp.Source)})
	}
	return out
}

// BenchmarkMergeTaintState_Steady is one per-step merge in steady state: the
// engine's export against the session's state, the same spans and filter.
func BenchmarkMergeTaintState_Steady(b *testing.B) {
	newer := fullTaintState()
	older := cloneDeep(newer)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MergeTaintState(newer, older)
	}
}

// stepStates returns the states one per-step save merges: the engine's
// export (three spans newer than the rest), the session's state from the
// previous step, and the same state as loaded from the session file.
func stepStates() (export, prev, disk TaintState) {
	prev = fullTaintState()
	disk = cloneDeep(prev)
	export = cloneDeep(prev)
	fresh := []TaintSpan{
		{Kind: "host", Value: "new1.example", Source: "web"},
		{Kind: "url", Value: "https://new1.example/x", Source: "web"},
		{Kind: "token", Hash: TaintSpanHash("new-token"), Source: "web"},
	}
	export.Spans = append(fresh, export.Spans[:MaxTaintSpans-len(fresh)]...)
	return export, prev, disk
}

// BenchmarkTaintMergeStep_CLI is the merging one CLI/REPL/serve step does:
// markRunTaint merges the export into the session, then the save merges the
// result onto the file's state.
func BenchmarkTaintMergeStep_CLI(b *testing.B) {
	export, prev, disk := stepStates()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		merged := MergeTaintState(export, prev)
		_ = MergeTaintState(merged, disk)
	}
}

// BenchmarkTaintMergeStep_Telegram is the merging one Telegram step does:
// MarkTaintSpans merges the export into the chat's carry, the chat save
// merges the carry onto the stored session, and the store save merges that
// onto the file's state.
func BenchmarkTaintMergeStep_Telegram(b *testing.B) {
	export, prev, disk := stepStates()
	carry := cloneDeep(prev)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		marked := MergeTaintState(export, carry)
		stored := MergeTaintState(marked, prev)
		_ = MergeTaintState(stored, disk)
	}
}

// BenchmarkMergeTaintState_OneSideEmpty merges a full state with nothing.
func BenchmarkMergeTaintState_OneSideEmpty(b *testing.B) {
	newer := fullTaintState()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MergeTaintState(newer, TaintState{})
	}
}

// BenchmarkSaveNoIndex_FullTaintState saves a session carrying a full span
// list and a filter over its previous revision, as every per-step persist
// does (the merge included).
func BenchmarkSaveNoIndex_FullTaintState(b *testing.B) {
	s, err := NewStoreWithDir(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	sess := &Session{ID: "20261110-bench", Task: "t", Messages: []Message{{Role: "user", Content: "hi"}}}
	sess.SetTaintState(fullTaintState())
	if err := s.SaveNoIndex(sess); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: fmt.Sprint("step ", i)})
		if err := s.SaveNoIndex(sess); err != nil {
			b.Fatal(err)
		}
	}
}
