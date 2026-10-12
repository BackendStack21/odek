package session

import (
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/redact"
)

const memHunt12bSecret = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"

// TestRED_InPlaceSystemRecordRefreshPersistsSecretUnredacted: MergeCheckpoint
// refreshes an already-persisted system record (the rolling compaction
// digest) IN PLACE. The save path only redacts messages at or beyond
// RedactBoundary, and the boundary anchor fingerprints only the last covered
// message — which the in-place refresh does not touch. A digest rewritten
// with a secret (the model summary of dropped turns quoting a pasted token)
// therefore reaches disk unredacted.
func TestRED_InPlaceSystemRecordRefreshPersistsSecretUnredacted(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	durable := []Message{
		{ID: "m-sys", Role: "system", Content: "you are odek"},
		{ID: "m-user", Role: "user", Content: "deploy the app"},
		{ID: "m-digest", Role: "system", Content: "[Compacted earlier context: sketch]"},
		{ID: "m-a1", Role: "assistant", Content: "working on it"},
	}
	sess, err := store.Create(durable, "m", "deploy the app")
	if err != nil {
		t.Fatal(err)
	}
	// Later checkpoint: the model digest replaces the sketch in place; a new
	// assistant step is appended.
	snapshot := CloneMessages(sess.Messages)
	snapshot[2].Content = "[Compacted earlier context: user pushed with token " + memHunt12bSecret + "]"
	snapshot = append(snapshot, Message{ID: "m-a2", Role: "assistant", Content: "done"})
	sess.Messages = MergeCheckpoint(sess.Messages, snapshot)
	if !strings.Contains(sess.Messages[2].Content, memHunt12bSecret) {
		t.Fatalf("precondition: merge should refresh the system record in place, got %q", sess.Messages[2].Content)
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), memHunt12bSecret) {
		t.Fatalf("session file persists a secret from an in-place refreshed system record (digest) unredacted")
	}
}

// TestRED_ReadingOdekUntrustedStoresViaFileToolsLaundersEpisodeTaint: the
// episode trust rule treats every path under ~/.odek as inside the trust zone.
// But ~/.odek holds exactly the content the memory gates refuse to recall:
// other sessions' transcripts (session_search over the same data is an
// always-tainting EpisodeExternalTool), the extended-memory quarantine
// (list_quarantine now taints) and tainted, unpromoted episode files (the
// memory `view` action refuses them). Reading the same bytes with read_file
// leaves the episode TRUSTED and auto-recalled.
func TestRED_ReadingOdekUntrustedStoresViaFileToolsLaundersEpisodeTaint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"/.odek/sessions", "/.odek/memory/extended", "/.odek/memory/episodes"} {
		if err := os.MkdirAll(home+d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{
		home + "/.odek/sessions/20261001-other.json",
		home + "/.odek/memory/extended/quarantine.json",
		home + "/.odek/memory/episodes/20261001-tainted.md",
	} {
		args := `{"path":"` + p + `"}`
		if !ToolCallTaintsEpisode("read_file", args) {
			t.Errorf("read_file %s (untrusted store, gated on every dedicated path) does not taint the episode", p)
		}
	}
}

// TestRED_StructuralToolArgRedactionLosesKeyContext: redactToolArguments
// redacts a JSON argument object value-by-value, each string in isolation.
// Every key-context rule of the redactor ("password": "...", X-Api-Key: ...,
// "Authorization": "token ...") needs the key next to the value, so a secret
// passed as a credential-named argument or header — http_request headers, an
// MCP login tool's password argument — is persisted verbatim, although the
// same text is redacted when scanned whole.
func TestRED_StructuralToolArgRedactionLosesKeyContext(t *testing.T) {
	cases := []struct{ args, secret string }{
		{`{"url":"https://api.example.com/v1/me","headers":{"X-Api-Key":"9f8e7d6c5b4a39281706f5e4d3c2b1a0"}}`, "9f8e7d6c5b4a39281706f5e4d3c2b1a0"},
		{`{"username":"bob","password":"Tr0ub4dor-horse-battery"}`, "Tr0ub4dor-horse-battery"},
	}
	for _, c := range cases {
		if strings.Contains(redact.RedactSecrets(c.args), c.secret) {
			t.Logf("redactor does not cover %q even as whole text; skipping case", c.args)
			continue
		}
		if got := redactToolArguments(c.args); strings.Contains(got, c.secret) {
			t.Errorf("tool-call arguments persisted with a secret the whole-text redactor removes:\n  in:  %s\n  out: %s", c.args, got)
		}
	}
}

func TestRED_ToolArgRedactionPrecision(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"command":"export API_KEY=abcdef1234567890abcdef && make"}`, `{"command":"export [REDACTED] && make"}`},
		{`{"token_count":"42","max_tokens":"100"}`, `{"max_tokens":"100","token_count":"42"}`},
		{`{"q":"a&b<c>"}`, `{"q":"a&b<c>"}`},
	}
	for _, c := range cases {
		got := redactToolArguments(c.in)
		if got != c.want {
			t.Errorf("in %s\n got  %s\n want %s", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, secret string }{
		{`{"credentials":{"user":"bob","pw":"hunter2hunter2"}}`, "hunter2hunter2"},
		{`{"args":["--password","hunter2hunter2"]}`, "hunter2hunter2"},
	} {
		if got := redactToolArguments(c.in); strings.Contains(got, c.secret) {
			t.Errorf("secret survived: %s", got)
		}
	}
}
