package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func serveClipTestRun() *serveRun {
	run := &serveRun{ID: "run-clip", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	return run
}

func TestServeClipRunEventStringMultibyteBoundary(t *testing.T) {
	// 3-byte runes so the cuts land inside a rune.
	s := strings.Repeat("€", serveRunEventFieldMax)
	got := clipRunEventString(s)
	if !utf8.ValidString(got) {
		t.Fatal("clipped string is not valid UTF-8")
	}
	if !strings.Contains(got, "bytes omitted") || len(got) > serveRunEventFieldMax+64 {
		t.Fatalf("unexpected clip result (len %d)", len(got))
	}
}

func TestServeClipRunEventStringInvalidByteKeepsHead(t *testing.T) {
	s := "\xff" + strings.Repeat("a", 20000)
	got := clipRunEventString(s)
	if !strings.HasPrefix(got, "\xff"+strings.Repeat("a", serveRunEventFieldMax/2-1)) {
		t.Fatalf("invalid byte dropped the head: %.20q", got)
	}
	s = strings.Repeat("a", 20000) + "\xff"
	if got := clipRunEventString(s); !strings.HasSuffix(got, strings.Repeat("a", serveRunEventFieldMax/2-1)+"\xff") {
		t.Fatal("invalid trailing byte dropped the tail")
	}
}

func TestServeRunRecordClipsNestedAndUnmarshalableValues(t *testing.T) {
	run := serveClipTestRun()
	big := make([]string, 0, 4000)
	for i := 0; i < 4000; i++ {
		big = append(big, "item-item-item")
	}
	_ = run.record(map[string]any{"type": "tool_call", "args": big})
	_ = run.record(map[string]any{"type": "tool_call", "args": make(chan int)})
	_ = run.record(map[string]any{"type": "tool_call", "args": []string{"small"}})
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.events) != 3 {
		t.Fatalf("events = %d", len(run.events))
	}
	if s, _ := run.events[0]["args"].(string); !strings.Contains(s, "bytes omitted from run event tail") {
		t.Fatalf("nested slice not replaced by marker: %v", run.events[0]["args"])
	}
	if s, _ := run.events[1]["args"].(string); s != "[value omitted from run event tail]" {
		t.Fatalf("unmarshalable value marker = %v", run.events[1]["args"])
	}
	if _, ok := run.events[2]["args"].([]string); !ok {
		t.Fatal("small nested value should be kept as is")
	}
}

func TestServeSchedulePreviewNeverMatchingAndValid(t *testing.T) {
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handleSchedulePreview(rec, httptest.NewRequest(http.MethodGet, "/api/schedules/preview?"+q, nil))
		return rec
	}
	if rec := get("cron=0+0+30+2+*&timezone=UTC"); rec.Code != http.StatusBadRequest {
		t.Fatalf("never-matching cron: %d", rec.Code)
	}
	if rec := get("cron=0+9+*+*+1-5&timezone=UTC"); rec.Code != http.StatusOK {
		t.Fatalf("valid cron: %d", rec.Code)
	}
}
