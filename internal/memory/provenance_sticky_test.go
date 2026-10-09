package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// stickyFixture runs from a fresh workspace with a session store beside it.
func stickyFixture(t *testing.T) (*session.Store, string) {
	t.Helper()
	ws := t.TempDir()
	t.Chdir(ws)
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store, ws
}

func readCall(id, path string) []session.Message {
	tc := session.ToolCall{ID: id, Type: "function"}
	tc.Function.Name = "read_file"
	tc.Function.Arguments = `{"path":"` + path + `"}`
	return []session.Message{
		{Role: "assistant", ToolCalls: []session.ToolCall{tc}},
		{Role: "tool", ToolCallID: id, Name: "read_file", Content: "contents"},
	}
}

// dropToolGroups removes every assistant tool-call message and its results,
// as context trimming, compaction or write-time size trimming can.
func dropToolGroups(msgs []session.Message) []session.Message {
	var out []session.Message
	for _, m := range msgs {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// A session that read a file outside the workspace must still produce an
// untrusted episode after the read_file call was trimmed out of its history:
// otherwise the episode is stored trusted and auto-recalled later.
func TestRED_EpisodeProvenanceSurvivesTrimmedOutsideRead(t *testing.T) {
	store, _ := stickyFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "look"}}
	msgs = append(msgs, readCall("c1", outside)...)
	msgs = append(msgs, session.Message{Role: "assistant", Content: "done"})
	sess, err := store.Create(msgs, "m", "look")
	if err != nil {
		t.Fatal(err)
	}
	sess.Messages = dropToolGroups(sess.Messages)
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p := DeriveSessionProvenance(loaded); !p.Untrusted {
		t.Fatalf("episode trusted after the outside read was trimmed: %+v", p)
	}
	if p := DeriveSessionProvenance(sess); !p.Untrusted {
		t.Fatalf("in-memory session lost the episode taint after save: %+v", p)
	}
}

// Write-time size trimming drops the oldest groups inside the same save that
// first sees them; the taint must be derived before the drop.
func TestRED_EpisodeProvenanceSurvivesWriteTimeTrim(t *testing.T) {
	store, _ := stickyFixture(t)
	old := session.MaxSessionFileBytes
	session.MaxSessionFileBytes = 8192
	t.Cleanup(func() { session.MaxSessionFileBytes = old })
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "look"}}
	msgs = append(msgs, readCall("c1", "/etc/hosts")...)
	for i := 0; i < 8; i++ {
		msgs = append(msgs, session.Message{Role: "assistant", Content: strings.Repeat("x", 2000)})
	}
	sess, err := store.Create(msgs, "m", "look")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range loaded.Messages {
		if len(m.ToolCalls) > 0 {
			t.Fatal("fixture did not trim the tool call")
		}
	}
	if p := DeriveSessionProvenance(loaded); !p.Untrusted {
		t.Fatalf("episode trusted after write-time trim dropped the outside read: %+v", p)
	}
}

func TestDeriveSessionProvenanceSources(t *testing.T) {
	if p := DeriveSessionProvenance(nil); !p.Untrusted || len(p.Sources) != 1 || p.Sources[0] != "unknown_provenance" {
		t.Fatalf("nil session: %+v", p)
	}
	sticky := &session.Session{EpisodeUntrusted: true, Messages: []session.Message{{Role: "user", Content: "hi"}}}
	if p := DeriveSessionProvenance(sticky); !p.Untrusted || len(p.Sources) != 1 || p.Sources[0] != "trimmed_history" {
		t.Fatalf("sticky session: %+v", p)
	}
	live := &session.Session{EpisodeUntrusted: true, Messages: readCall("c1", "/etc/hosts")}
	if p := DeriveSessionProvenance(live); !p.Untrusted || len(p.Sources) != 1 || p.Sources[0] != "read_file" {
		t.Fatalf("history-derived taint should name its source: %+v", p)
	}
	if p := DeriveSessionProvenance(&session.Session{}); p.Untrusted {
		t.Fatalf("clean session: %+v", p)
	}
}

// Workspace-only reads keep the session recallable even after trimming: the
// sticky flag must not fall back to the stricter delegation taint.
func TestEpisodeProvenanceWorkspaceReadsStayTrustedAfterTrim(t *testing.T) {
	store, ws := stickyFixture(t)
	inside := filepath.Join(ws, "main.go")
	if err := os.WriteFile(inside, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "look"}}
	msgs = append(msgs, readCall("c1", inside)...)
	msgs[len(msgs)-1].Content = wrapped("tool:read_file", "package main")
	msgs = append(msgs, session.Message{Role: "assistant", Content: "done"})
	sess, err := store.Create(msgs, "m", "look")
	if err != nil {
		t.Fatal(err)
	}
	sess.Messages = dropToolGroups(sess.Messages)
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UntrustedIngested {
		t.Fatal("fixture: workspace read should still set the delegation taint")
	}
	if p := DeriveSessionProvenance(loaded); p.Untrusted {
		t.Fatalf("workspace-only session became untrusted after trim: %+v", p)
	}
}
