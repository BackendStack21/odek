package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

// ── GET /api/sessions pagination correctness ────────────────────────────

// decodePage decodes the paged envelope.
func decodePage(t *testing.T, w *httptest.ResponseRecorder) struct {
	Sessions []session.Session `json:"sessions"`
	Count    int               `json:"count"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
} {
	t.Helper()
	var page struct {
		Sessions []session.Session `json:"sessions"`
		Count    int               `json:"count"`
		Limit    int               `json:"limit"`
		Offset   int               `json:"offset"`
	}
	if err := json.NewDecoder(w.Body).Decode(&page); err != nil {
		t.Fatalf("decode envelope: %v (body: %s)", err, w.Body.String())
	}
	return page
}

// TestHandleSessionListPaged_SearchLimitClamped: the paged envelope
// advertises a limit — the returned page must respect it. The search path
// loaded the whole store (List(0)) but never clamped the page to limit
// after the offset slice, so ?q=x&limit=N returned every match.
func TestHandleSessionListPaged_SearchLimitClamped(t *testing.T) {
	store := newTestSessionStore(t)
	for i := 0; i < 5; i++ {
		if _, err := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "fixme task"); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	handleSessionListPaged(store)(w, httptest.NewRequest(http.MethodGet, "/api/sessions?q=fixme&limit=2&offset=0", nil))
	page := decodePage(t, w)
	if len(page.Sessions) != 2 || page.Count != 2 {
		t.Fatalf("q=fixme&limit=2 returned %d sessions (count %d), want at most 2 — limit must clamp the page", len(page.Sessions), page.Count)
	}
}

// TestHandleSessionListPaged_PinnedFloatsOnFullList: pinned sessions float
// to the top of the listing. For paged requests the float must happen on
// the FULL list before the window is cut. Floating inside a pre-windowed
// slice (List(limit+offset)) structurally hides a pinned session whose
// recency rank is beyond the window from EVERY page — it floats to the
// window front and is then cut off by the offset slice — and shifts
// boundary entries between pages (duplicates across pages).
func TestHandleSessionListPaged_PinnedFloatsOnFullList(t *testing.T) {
	store := newTestSessionStore(t)
	// Save() refreshes UpdatedAt, so build recency order by pinning FIRST
	// and creating newer sessions after: the pinned session ends up at the
	// oldest recency rank (position 5 of 6).
	pinned, err := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "oldest task")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Pinned = true
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		sess, err := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")
		if err != nil {
			t.Fatal(err)
		}
		_ = sess
		time.Sleep(3 * time.Millisecond) // strict recency ordering between saves
	}

	handler := handleSessionListPaged(store)
	seen := map[string]int{}
	var pinnedOnPageOne bool
	for _, offset := range []int{0, 2, 4} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/api/sessions?limit=2&offset="+strconv.Itoa(offset), nil))
		page := decodePage(t, w)
		if len(page.Sessions) > 2 {
			t.Fatalf("offset %d: page returned %d sessions, want <= limit 2", offset, len(page.Sessions))
		}
		for _, s := range page.Sessions {
			seen[s.ID]++
			if s.ID == pinned.ID && offset == 0 {
				pinnedOnPageOne = true
			}
		}
	}
	if len(seen) != 6 {
		t.Fatalf("page union covers %d unique sessions, want all 6 (missing/duplicated across pages): %v", len(seen), seen)
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("session %s appeared on %d pages — pagination windows overlap", id, n)
		}
	}
	if !pinnedOnPageOne {
		t.Errorf("pinned session %s must float to page 1, got distribution %v", pinned.ID, seen)
	}
}
