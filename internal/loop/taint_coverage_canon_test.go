package loop

import (
	"fmt"
	"slices"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// covCanonDigest returns the coverage digest of messages under env.
func covCanonDigest(t *testing.T, messages []session.Message, env taintEnv) string {
	t.Helper()
	key, ok := taintCoverageKey(env)
	if !ok {
		t.Fatal("no coverage key")
	}
	h := newTaintCoverageHash(key)
	for i := range messages {
		if taintCountedMessage(&messages[i]) {
			h.add(&messages[i])
		}
	}
	return h.sum()
}

// The coverage digest of a history is the digest of the history the store
// writes and loads back whenever the arguments decode alike in both forms;
// arguments whose rewrite could decode differently (or that the store
// replaces) are digested raw, so a reload of them never reproduces the
// coverage and the history is scanned in full. Fails if the store's
// encoding and the digest's canonical form drift apart.
func TestTaintCoverage_DigestSurvivesStoreRewrite(t *testing.T) {
	survive := []string{
		`{"command":"ls -la"}`,
		`{"command": "curl -s https://feed.example/x -o x"}`,
		`{"path":"notes.md","content":"see https://feed.example/x"}`,
		"{\n  \"command\": \"curl -s https://feed.example/x -o x\"\n}",
		`{"command":"go build ./... && ./app > out.log"}`,
		`{"command":"curl -s https:\/\/feed.example\/x"}`,
		`{"files":[{"path":"b.go"},{"path":"a.go"}],"n":1.000e10}`,
		`{"q":"café  ","nested":{"z":[1,{"y":null}],"a":false}}`,
		``,
	}
	rescan := []string{
		`ls -la`,
		`{"path":"a","path":"b"}`,
		`{"path":"README.md","PATH":"/etc/x"}`,
		`{"files":[{"path":"a"}],"files":[{}]}`,
		`{"a":1}]`,
		"\xef\xbb\xbf{\"a\":1}",
	}
	env := currentTaintEnv()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, args := range append(slices.Clone(survive), rescan...) {
		call := session.ToolCall{ID: "c1", Type: "function"}
		call.Function.Name, call.Function.Arguments = "write_file", args
		history := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: []session.ToolCall{call}}, {Role: "tool", ToolCallID: "c1", Content: "ok"}}
		sess := &session.Session{ID: fmt.Sprintf("20261112-canon%02d", i), Task: "t", Messages: session.CloneMessages(history)}
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		before, after := covCanonDigest(t, history, env), covCanonDigest(t, loaded.Messages, env)
		// Saving the loaded form again (a later save of a resumed session,
		// whose head the store rewrites in full when its boundary moved)
		// keeps the digest: the canonical form is a fixed point.
		loaded.RedactBoundary, loaded.RedactBoundaryFP = 0, ""
		if err := store.Save(loaded); err != nil {
			t.Fatal(err)
		}
		reloaded, err := store.Load(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		again := covCanonDigest(t, reloaded.Messages, env)
		wantSame := i < len(survive)
		if (before == after) != wantSame || after != again {
			t.Errorf("%q (stored as %q): digest kept across the store = %v, want %v",
				args, loaded.Messages[2].ToolCalls[0].Function.Arguments, before == after, wantSame)
		}
		if !wantSame {
			continue
		}
		// The recording decisions read the same from both forms.
		orig, stored := &history[2].ToolCalls[0], &loaded.Messages[2].ToolCalls[0]
		for _, name := range []string{"read_file", "diff", "bg_output", "shell", "memory"} {
			orig.Function.Name, stored.Function.Name = name, name
			if taintGoSource(orig) != taintGoSource(stored) || !slices.Equal(bgReadJobs(orig), bgReadJobs(stored)) ||
				session.ToolCallTaintsEpisode(name, orig.Function.Arguments) != session.ToolCallTaintsEpisode(name, stored.Function.Arguments) {
				t.Errorf("%s %q: decided differently from its stored form %q", name, orig.Function.Arguments, stored.Function.Arguments)
			}
		}
	}

	// The raw and canonical forms are digested under different tags: an
	// argument text equal to another's canonical form never shares its
	// digest unless it is that canonical form.
	a := []session.Message{{Role: "assistant", ToolCalls: []session.ToolCall{covCall("c", "shell", map[string]string{"command": "x"})}}}
	b := session.CloneMessages(a)
	b[0].ToolCalls = slices.Clone(b[0].ToolCalls)
	b[0].ToolCalls[0].Function.Arguments = `{"command":"x"}x`
	if covCanonDigest(t, a, env) == covCanonDigest(t, b, env) {
		t.Fatal("a raw and a canonical argument text share a digest")
	}
}
