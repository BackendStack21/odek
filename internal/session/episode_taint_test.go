package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func episodeReadCall(id, path string) Message {
	tc := ToolCall{ID: id, Type: "function"}
	tc.Function.Name = "read_file"
	tc.Function.Arguments = `{"path":"` + path + `"}`
	return Message{Role: "assistant", ToolCalls: []ToolCall{tc}}
}

func TestEpisodeUntrustedIsDerivedAndSticky(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clean, err := store.Create([]Message{{Role: "user", Content: "hi"}}, "m", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if clean.EpisodeUntrusted || !clean.EpisodeTaintTracked {
		t.Fatalf("clean session: untrusted=%v tracked=%v", clean.EpisodeUntrusted, clean.EpisodeTaintTracked)
	}

	sess, err := store.Create([]Message{
		{Role: "user", Content: "read it"},
		episodeReadCall("c1", "/etc/hosts"),
		{Role: "tool", ToolCallID: "c1", Content: "127.0.0.1 localhost"},
	}, "m", "read it")
	if err != nil {
		t.Fatal(err)
	}
	if !sess.EpisodeUntrusted {
		t.Fatal("save did not derive the episode taint from an outside read")
	}

	// A snapshot that lost both the call and the flag must not clear it.
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.EpisodeUntrusted = false
	loaded.Messages = []Message{{Role: "user", Content: "read it"}, {Role: "assistant", Content: "done"}}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if stamp := store.revStamps[loaded.ID]; !stamp.episode {
		t.Fatal("revision stamp lost the episode taint")
	}

	// The cached-revision path (no re-read of the file) carries the taint.
	time.Sleep(revStampSettle + 10*time.Millisecond)
	loaded.EpisodeUntrusted = false
	loads := store.revisionLoads
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if store.revisionLoads != loads {
		t.Fatal("fixture: save did not take the cached-revision path")
	}
	if !loaded.EpisodeUntrusted {
		t.Fatal("cached-revision save cleared the episode taint")
	}
	again, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.EpisodeUntrusted {
		t.Fatal("trimmed save cleared the persisted episode taint")
	}
}

// A snapshot built without Load (not tracked) is scanned whole, including
// messages below the redaction boundary.
func TestEpisodeUntrackedSnapshotIsScannedWhole(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}, "m", "a")
	if err != nil {
		t.Fatal(err)
	}
	if sess.RedactBoundary != 3 {
		t.Fatalf("fixture: boundary %d", sess.RedactBoundary)
	}
	sess.Messages[0] = episodeReadCall("c1", "/etc/hosts")
	sess.EpisodeTaintTracked = false
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if !sess.EpisodeUntrusted {
		t.Fatal("untracked snapshot was scanned from the redaction boundary only")
	}
}

func TestLoadDerivesEpisodeUntrustedForLegacyFiles(t *testing.T) {
	cases := []struct {
		name string
		msgs []map[string]any
		want bool
	}{
		{"outside read", []map[string]any{
			{"role": "assistant", "tool_calls": []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"/etc/hosts"}`}}}},
			{"role": "tool", "tool_call_id": "c1", "content": "x"},
		}, true},
		{"workspace read", []map[string]any{
			{"role": "assistant", "tool_calls": []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"main.go"}`}}}},
			{"role": "tool", "tool_call_id": "c1", "content": "<untrusted_content_ab source=\"tool:read_file\">\npackage main\n</untrusted_content_ab>"},
		}, false},
		{"no tools", []map[string]any{{"role": "user", "content": "hi"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewStoreWithDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := store.Create([]Message{{Role: "user", Content: "hi"}}, "m", "hi")
			if err != nil {
				t.Fatal(err)
			}
			// Rewrite the file as a pre-flag version would have.
			path := filepath.Join(dir, sess.ID+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			delete(raw, "episode_untrusted")
			delete(raw, "episode_taint_tracked")
			raw["messages"] = tc.msgs
			data, _ = json.Marshal(raw)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.EpisodeUntrusted != tc.want || !loaded.EpisodeTaintTracked {
				t.Fatalf("legacy load: untrusted=%v tracked=%v, want untrusted=%v", loaded.EpisodeUntrusted, loaded.EpisodeTaintTracked, tc.want)
			}
		})
	}
}

// A save over a symlink alias cannot see the previous revision's episode
// taint, so it assumes it.
func TestAliasSaveKeepsEpisodeTaintFailClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{episodeReadCall("c1", "/etc/hosts")}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sess.ID+".json")
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	snap := &Session{ID: sess.ID, Messages: []Message{{Role: "user", Content: "clean"}}}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.EpisodeUntrusted {
		t.Fatal("save over a symlink alias cleared the episode taint")
	}
}

func TestEpisodeTaintSourcesRules(t *testing.T) {
	mcp := ToolCall{ID: "m"}
	mcp.Function.Name = "srv__tool"
	curl := ToolCall{ID: "s"}
	curl.Function.Name = "shell"
	curl.Function.Arguments = `{"command":"curl https://example.com"}`
	local := ToolCall{ID: "l"}
	local.Function.Name = "shell"
	local.Function.Arguments = `{"command":"go test ./..."}`
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{mcp, curl, local}},
		{Role: "user", Content: "<untrusted_content_ab source=\"attachment:x\">\nhi\n</untrusted_content_ab>"},
		{Role: "tool", Content: "<untrusted_content_ab source=\"attachment:y\">\nhi\n</untrusted_content_ab>"},
	}
	got := EpisodeTaintSources(msgs)
	want := []string{"srv__tool", "shell", "attachment:x"}
	if len(got) != len(want) {
		t.Fatalf("sources %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sources %v, want %v", got, want)
		}
	}
	if !messagesTaintEpisode(msgs[1:2]) || messagesTaintEpisode(msgs[2:]) {
		t.Fatal("messagesTaintEpisode disagrees with EpisodeTaintSources")
	}
	if messagesTaintEpisode([]Message{{Role: "assistant", ToolCalls: []ToolCall{local}}}) {
		t.Fatal("local shell command tainted the episode")
	}
}
