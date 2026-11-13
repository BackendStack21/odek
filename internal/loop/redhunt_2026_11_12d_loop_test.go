package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// loopR4History is a history of about n bytes of 64 KiB pieces: workspace
// reads (results the scan only digests) and file writes (the bytes are the
// call's arguments, which the result's pairing reads too).
func loopR4History(n int) []session.Message {
	h := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "update the docs"}}
	const page = 64 << 10
	for i := 0; i*page < n; i++ {
		body := fmt.Sprintf("section %d ", i) + strings.Repeat("lorem ipsum dolor sit amet ", page/27)
		if i%2 == 0 {
			h = append(h, covGroup(covCall(fmt.Sprintf("r%d", i), "read_file", map[string]string{"path": "README.md"}),
				defaultUntrustedWrap("tool:read_file", body))...)
		} else {
			h = append(h, covGroup(covCall(fmt.Sprintf("w%d", i), "write_file", map[string]string{"path": fmt.Sprintf("docs/s%d.md", i), "content": body}),
				"wrote "+fmt.Sprint(len(body))+" bytes")...)
		}
	}
	return h
}

// loopR4Reload returns h as a store reload hands it back: equal values in
// fresh bytes.
func loopR4Reload(t *testing.T, h []session.Message) []session.Message {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var out []session.Message
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !slicesEqualMessages(out, h) {
		t.Fatal("precondition: the reload changed the history")
	}
	return out
}

func slicesEqualMessages(a, b []session.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !trackMessageEqual(&a[i], &b[i]) {
			return false
		}
	}
	return true
}

// loopR4Bytes is the number of content and argument bytes of h.
func loopR4Bytes(h []session.Message) int64 {
	var n int64
	for i := range h {
		n += int64(len(h[i].Content))
		for _, tc := range h[i].ToolCalls {
			n += int64(len(tc.Function.Arguments))
		}
	}
	return n
}

// loopR4Unshared is the number of bytes the next comparison of h with track
// reads: content and arguments a kept entry holds equal to h's, but in
// other bytes (comparing strings that share their bytes reads none).
func loopR4Unshared(track taintScanTrack, h []session.Message) int64 {
	var n int64
	unshared := func(a, b string) {
		if len(a) > 0 && a == b && unsafe.StringData(a) != unsafe.StringData(b) {
			n += int64(len(a))
		}
	}
	for i := range track.seen {
		if i >= len(h) {
			break
		}
		e := &track.seen[i]
		unshared(e.content, h[i].Content)
		for j := range e.calls {
			if j < len(h[i].ToolCalls) {
				unshared(e.calls[j].Function.Arguments, h[i].ToolCalls[j].Function.Arguments)
			}
		}
	}
	return n
}

func loopR4Heap() uint64 {
	var ms runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// A REPL or serve turn hands the long-lived engine the session reloaded from
// the store: equal values in fresh bytes. What the engine's span store keeps
// of the history it observed must not hold the previous turn's copy alive:
// once the gate and the save have observed the reloaded history, dropping
// the store frees no more than a fraction of the history.
func TestRED_Loop_TaintTrackKeepsNoSupersededHistory(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	ctx := context.Background()
	h := loopR4History(6 << 20)
	hb := loopR4Bytes(h)
	e.taintGate(ctx, h, nil, 0, nil, 1)
	e.TaintSpanState(session.CloneMessages(h))
	next := append(loopR4Reload(t, h), session.Message{Role: "user", Content: "next turn"})
	h = nil
	e.taintGate(ctx, next, nil, 0, nil, 1)
	e.TaintSpanState(session.CloneMessages(next))
	withStore := loopR4Heap()
	e.ResetTaintSpans()
	without := loopR4Heap()
	runtime.KeepAlive(next)
	runtime.KeepAlive(e)
	retained := int64(withStore) - int64(without)
	t.Logf("history %d KiB: the span store retains %d KiB besides the live history", hb>>10, retained>>10)
	if retained > hb/4 {
		t.Fatalf("the span store keeps %d KiB alive besides a live history of %d KiB: a superseded copy of the history", retained>>10, hb>>10)
	}
}

// A span keeps its own copy of the label it was recorded under: a wrapper's
// label is a slice of its message, and the spans outlive the history they
// came from (a reload replaces it every turn).
func TestRED_Loop_TaintSpanLabelKeepsNoMessage(t *testing.T) {
	s := newTaintSpanStore()
	h := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "fetch the docs"}}
	h = append(h, covGroup(covCall("f1", "http_request", map[string]string{"url": "https://label.r4.example/"}),
		defaultUntrustedWrap("tool:http_request", "docs at https://label.r4.example/docs and label-cdn.r4.example:443/ "+strings.Repeat("filler ", 500)))...)
	s.observe(h, sec16TaintEnv)
	content := h[len(h)-1].Content
	lo := uintptr(unsafe.Pointer(unsafe.StringData(content)))
	hi := lo + uintptr(len(content))
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.spans) == 0 {
		t.Fatal("precondition: no span recorded")
	}
	for k, sp := range s.spans {
		if p := uintptr(unsafe.Pointer(unsafe.StringData(sp.source))); len(sp.source) > 0 && p >= lo && p < hi {
			t.Fatalf("span %q keeps its label %q in the bytes of the message it came from", k, sp.source)
		}
	}
}

// An unchanged history reloaded from the store is compared byte by byte at
// most once per caller (gate, saves): every later observe of it reads none
// of its bytes again, and none is digested again.
func TestRED_Loop_TaintTrackComparesReloadOnce(t *testing.T) {
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, 0)
	ctx := context.Background()
	h := loopR4History(1 << 20)
	e.taintGate(ctx, h, nil, 0, nil, 1)
	e.TaintSpanState(session.CloneMessages(h))
	store := e.spanStore()
	reloaded := loopR4Reload(t, h)
	digested := store.digestWork.Load()
	for step := 1; step <= 3; step++ {
		gate, save := loopR4Unshared(store.gateTrack, reloaded), loopR4Unshared(store.saveTrack, reloaded)
		if step > 1 && (gate > 0 || save > 0) {
			t.Fatalf("observe %d of an unchanged reloaded history compares %d bytes (gate) and %d bytes (save) again", step, gate, save)
		}
		e.taintGate(ctx, reloaded, nil, 0, nil, 1)
		e.TaintSpanState(session.CloneMessages(reloaded))
	}
	if d := store.digestWork.Load() - digested; d != 0 {
		t.Fatalf("an unchanged reloaded history was digested again: %d bytes", d)
	}
}
