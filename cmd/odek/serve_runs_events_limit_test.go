package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
)

// An explicit limit=0 must reach the snapshot untruncated (documented
// "return everything"), and negatives are treated as 0 — previously both
// were silently rewritten to the default 100.
func TestHandleEvents_ExplicitZeroLimitReturnsAll(t *testing.T) {
	serveEvents.reset()
	t.Cleanup(serveEvents.reset)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		serveEvents.add(events.Event{Type: "e", RunID: fmt.Sprintf("r%d", i), Timestamp: now})
	}

	decode := func(req *http.Request) (count int) {
		w := httptest.NewRecorder()
		handleEvents()(w, req)
		var body struct {
			Count int `json:"count"`
		}
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Count
	}

	if got := decode(httptest.NewRequest(http.MethodGet, "/api/events?limit=0", nil)); got != 3 {
		t.Errorf("limit=0 returned %d events, want all 3", got)
	}
	if got := decode(httptest.NewRequest(http.MethodGet, "/api/events?limit=-5", nil)); got != 3 {
		t.Errorf("limit=-5 returned %d events, want all 3 (treated as 0)", got)
	}
	if got := decode(httptest.NewRequest(http.MethodGet, "/api/events?limit=2", nil)); got != 2 {
		t.Errorf("limit=2 returned %d events, want 2", got)
	}
	if got := decode(httptest.NewRequest(http.MethodGet, "/api/events", nil)); got != 3 {
		t.Errorf("absent limit returned %d events, want default (all 3 fit)", got)
	}
}
