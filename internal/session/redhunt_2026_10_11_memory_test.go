package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRED_AuditPersistsSecretsUnredacted: the per-session audit log lives in
// the session directory next to the (redacted) session files, but
// RecordTurn/RecordIngest write the raw user prompt, ingest source and
// resource list verbatim. A user pasting a token into the prompt, or a
// fetched URL carrying one in its query string, leaves the secret in
// ~/.odek/sessions/audit/<id>.json even though the session file redacts it.
func TestRED_AuditPersistsSecretsUnredacted(t *testing.T) {
	dir := t.TempDir()
	a := NewAuditStore(dir)
	const id = "20261011-aaaaaaaa"
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"

	if err := a.RecordIngest(id, 1, "https://api.example.com/x?token="+secret, "body mentions https://api.example.com/y?key="+secret); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordTurn(id, AuditTurn{
		Turn:               1,
		UserMessage:        "use my token " + secret + " to push",
		NovelResources:     []string{"https://evil.example/?k=" + secret},
		UntrustedResources: []string{"https://evil.example/?k=" + secret},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("audit log persisted the secret verbatim:\n%s", data)
	}
}

// TestRED_CorruptIndexCollapsesListing: when index.json fails to parse,
// loadIndex returns an empty map and the next save writes an index holding
// only the saved session. Every other session file stays on disk but
// vanishes from List (and from index-driven Cleanup) permanently, because a
// non-empty index disables the directory-scan fallback.
func TestRED_CorruptIndexCollapsesListing(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"one", "two"} {
		if _, err := st.Create([]Message{{Role: "user", Content: task}}, "m", task); err != nil {
			t.Fatal(err)
		}
	}
	// Torn/garbled index (e.g. external edit, disk error, older writer).
	if err := os.WriteFile(filepath.Join(dir, indexFile), []byte("[{\"id\":"), 0600); err != nil {
		t.Fatal(err)
	}
	// Reads over the corrupt index already see every session.
	if list, err := st.List(0); err != nil || len(list) != 2 {
		t.Fatalf("List over a corrupt index = %d sessions (err %v), want 2", len(list), err)
	}
	if _, err := st.Create([]Message{{Role: "user", Content: "three"}}, "m", "three"); err != nil {
		t.Fatal(err)
	}
	list, err := st.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("List after saving over a corrupt index = %d sessions, want 3 (two sessions hidden)", len(list))
	}
	// The rewritten index is valid and complete on its own.
	fresh, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fresh.loadIndex()); n != 3 {
		t.Fatalf("rewritten index holds %d entries, want 3", n)
	}
}

// TestRED_EpisodeTaintWorkspaceSymlinkEscape: pathOutsideRoots returns
// "inside" as soon as ANY candidate (the lexical path or its resolved
// target) sits under a trusted root. A symlink inside the workspace that
// points outside it (a cloned repo shipping `docs -> /`, or `cfg -> ~/.aws`)
// therefore reads files outside the trust zone while the episode stays
// trusted and auto-recallable — exactly the escape the symlink resolution
// was meant to catch.
func TestRED_EpisodeTaintWorkspaceSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s3cr3t"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "plain.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)

	// Control: the same file named directly is (correctly) an escape.
	if !ToolCallTaintsEpisode("read_file", `{"path":"`+filepath.Join(outside, "secret.txt")+`"}`) {
		t.Fatal("setup: direct read outside the workspace should taint")
	}
	if !ToolCallTaintsEpisode("read_file", `{"path":"link/secret.txt"}`) {
		t.Fatal("read through a workspace symlink pointing outside the workspace did not taint the episode")
	}
	// A not-yet-existing file behind the link is judged by its resolved parent.
	if !ToolCallTaintsEpisode("read_file", `{"path":"link/missing/deeper.txt"}`) {
		t.Fatal("missing path behind an escaping symlink did not taint the episode")
	}
	// Ordinary workspace reads, existing or not, stay trusted.
	for _, p := range []string{"plain.txt", "not/there.txt", filepath.Join(ws, "plain.txt")} {
		if ToolCallTaintsEpisode("read_file", `{"path":"`+p+`"}`) {
			t.Fatalf("workspace read %q tainted the episode", p)
		}
	}
}

// TestRED_IndexNullEntryDoesNotPanic: a null element in index.json is
// skipped rather than dereferenced.
func TestRED_IndexNullEntryDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, indexFile), []byte("[null]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.List(0); err != nil {
		t.Fatal(err)
	}
}
