package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func getSubagentEntries(t *testing.T, store *session.Store, url string, hdr map[string]string) (int, []subagentEntry) {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handleSubagentRegistry(store, "inst-token").ServeHTTP(rec, req)
	var out struct {
		Entries []subagentEntry `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Entries
}

// An entry stamped with its owning session at spawn keeps belonging to that
// session after the connection that spawned it is gone (reload, reconnect,
// session_switch).
func TestRED_SubagentOwner_SurvivesConnectionLoss(t *testing.T) {
	resetSubagentRegistry()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessA := subagentTestSession(t, store, "A")
	sessB := subagentTestSession(t, store, "B")

	send := func(v any) error { return nil }
	relay := newSubagentTelemetryRelayOwned(send, "conn-gone", func() string { return sessA.ID })
	relay(0, "task-own", `{"type":"subagent_queued","goal":"g"}`)
	// No connection with id conn-gone is registered: live resolution finds nothing.

	if code, got := getSubagentEntries(t, store, "/api/subagents?session_id="+sessA.ID, map[string]string{"X-Session-Token": sessA.AuthToken}); code != 200 || len(got) != 1 {
		t.Fatalf("owner view = %d %+v, want the recorded task", code, got)
	}
	if _, got := getSubagentEntries(t, store, "/api/subagents?session_id="+sessB.ID, map[string]string{"X-Session-Token": sessB.AuthToken}); len(got) != 0 {
		t.Fatalf("foreign session sees %+v", got)
	}
	if !subagentTaskOwnedBySession("task-own", sessA.ID) || subagentTaskOwnedBySession("task-own", sessB.ID) {
		t.Fatal("cancel ownership must follow the recorded owner")
	}
}

// Entries without a recorded owner keep resolving through live connections.
func TestSubagentOwner_UnownedFallsBackToLiveConnection(t *testing.T) {
	resetSubagentRegistry()
	conn := &wsConnInfo{ID: "conn-fb"}
	conn.setLive("sess-fb", true)
	wsConnRegister(conn)
	defer wsConnUnregister(conn.ID)
	subagentRegistryRecord(&subagentEntry{TaskID: "t-fb", RunKey: "conn-fb"})
	if !subagentTaskOwnedBySession("t-fb", "sess-fb") {
		t.Fatal("legacy entry must fall back to live connection resolution")
	}
}

func TestSubagentRegistry_OperatorView(t *testing.T) {
	resetSubagentRegistry()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	subagentRegistryRecord(&subagentEntry{TaskID: "a", RunKey: "r1", OwnerSession: "s1"})
	subagentRegistryRecord(&subagentEntry{TaskID: "b", RunKey: "r2", OwnerSession: "s2"})

	if code, got := getSubagentEntries(t, store, "/api/subagents", map[string]string{wsTokenHeaderName: "inst-token"}); code != 200 || len(got) != 2 {
		t.Fatalf("operator view = %d %+v, want both", code, got)
	}
	if code, _ := getSubagentEntries(t, store, "/api/subagents", nil); code != 400 {
		t.Fatalf("no token = %d, want 400", code)
	}
	if code, _ := getSubagentEntries(t, store, "/api/subagents", map[string]string{wsTokenHeaderName: "wrong"}); code != 400 {
		t.Fatalf("wrong token = %d, want 400", code)
	}
	if code, _ := getSubagentEntries(t, store, "/api/subagents", map[string]string{wsTokenHeaderName: "inst-token", "Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("foreign origin = %d, want 403", code)
	}
}
