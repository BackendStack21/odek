package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

func saveDeepSession(t *testing.T, store *session.Store, id string, age time.Duration, msgs ...string) {
	t.Helper()
	s := &session.Session{
		ID:        id,
		Task:      "unrelated task " + id,
		CreatedAt: time.Now().Add(-age - time.Hour),
		UpdatedAt: time.Now().Add(-age),
		Model:     "m",
		Turns:     1,
	}
	for _, m := range msgs {
		s.Messages = append(s.Messages, session.Message{Role: "user", Content: m})
	}
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
}

func deepSearchIDs(t *testing.T, tool *sessionSearchTool, query string, limit int) []string {
	t.Helper()
	out, err := tool.Call(fmt.Sprintf(`{"action":"search","query":%q,"limit":%d}`, query, limit))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ids := make([]string, len(r.Sessions))
	for i, s := range r.Sessions {
		ids[i] = s.ID
	}
	return ids
}

func TestRED_SessionSearch_DeepSearchStopsOnceLimitIsOutranked(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const total = 40
	for i := 0; i < total; i++ {
		msg := "nothing relevant here"
		if i < 10 {
			msg = "alpha and beta appear here"
		}
		saveDeepSession(t, store, fmt.Sprintf("s%02d", i), time.Duration(i+1)*time.Minute, msg)
	}
	tool := newSessionSearchTool(store)
	loads := 0
	tool.loadSession = func(id string) (*session.Session, error) { loads++; return store.Load(id) }

	got := deepSearchIDs(t, tool, "alpha beta", 3)
	want := []string{"s00", "s01", "s02"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if loads > 6 {
		t.Fatalf("deep search loaded %d of %d sessions for limit 3; want it to stop early", loads, total)
	}
}

func TestSessionSearch_DeepSearchStillFindsHigherScoringOlderSession(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Recent sessions match two of three tokens; the oldest matches all three
	// and must outrank them even though it is scanned last.
	for i := 0; i < 8; i++ {
		saveDeepSession(t, store, fmt.Sprintf("two%d", i), time.Duration(i+1)*time.Minute, "alpha and beta only")
	}
	saveDeepSession(t, store, "old-all", 500*time.Minute, "alpha", "then beta", "and gamma")
	tool := newSessionSearchTool(store)
	got := deepSearchIDs(t, tool, "alpha beta gamma", 3)
	if len(got) == 0 || got[0] != "old-all" {
		t.Fatalf("highest-scoring older session must lead the results, got %v", got)
	}
}

func TestSessionSearch_DeepSearchMatchesFullScanForSmallLimit(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		msgs := []string{"filler"}
		switch i % 3 {
		case 0:
			msgs = append(msgs, "alpha beta")
		case 1:
			msgs = append(msgs, "alpha", "beta", "gamma")
		}
		saveDeepSession(t, store, fmt.Sprintf("m%02d", i), time.Duration(i+1)*time.Minute, msgs...)
	}
	tool := newSessionSearchTool(store)
	for _, q := range []string{"alpha beta", "alpha beta gamma", "alpha", "gamma beta alpha filler"} {
		full := deepSearchIDs(t, tool, q, 100)
		for _, limit := range []int{1, 3, 7} {
			got := deepSearchIDs(t, tool, q, limit)
			n := limit
			if n > len(full) {
				n = len(full)
			}
			if fmt.Sprint(got) != fmt.Sprint(full[:n]) {
				t.Fatalf("query %q limit %d: %v, want prefix %v of full result", q, limit, got, full[:n])
			}
		}
	}
}
