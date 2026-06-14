package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/llm"
)

// writeVectorTestSession writes a minimal session JSON for vector-index tests.
func writeVectorTestSession(t *testing.T, dir, id string, msgs []llm.Message) {
	t.Helper()
	data, err := json.Marshal(struct {
		Messages []llm.Message `json:"messages"`
	}{Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// httpCfgForTests returns an HTTP embedding config backed by the shared mock
// server so semantic assertions are deterministic.
func httpCfgForTests(t *testing.T) *embedding.Config {
	t.Helper()
	srv, _ := mockEmbedServer(t)
	return httpEmbedConfig(srv)
}

// TestVectorIndexRebuildSkipsSymlink verifies that a session file that is a
// symlink to an arbitrary file outside the sessions directory is not indexed.
func TestVectorIndexRebuildSkipsSymlink(t *testing.T) {
	dir := t.TempDir()

	felineID := "20260518-abc12345678901234567890123456789"
	dbID := "20260518-def45678901234567890123456789012"
	writeVectorTestSession(t, dir, felineID, []llm.Message{
		{Role: "user", Content: "investigated the feline behavior module"},
	})
	writeVectorTestSession(t, dir, dbID, []llm.Message{
		{Role: "user", Content: "tuned postgres sql indexes"},
	})

	// Create a file outside the sessions dir with the same database-bucket
	// content a real session uses.
	outside := filepath.Join(t.TempDir(), "outside.json")
	secret := []byte(`{"messages":[{"role":"user","content":"tuned postgres sql indexes"}]}`)
	if err := os.WriteFile(outside, secret, 0600); err != nil {
		t.Fatal(err)
	}

	// Plant a symlink named like a session file.
	linkName := "20260518-symlink1234567890123456789012345.json"
	linkPath := filepath.Join(dir, linkName)
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	vi := new(VectorIndex)
	if err := vi.InitWithConfig(dir, httpCfgForTests(t)); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	if !vi.Ready() {
		t.Fatal("index should be ready")
	}

	linkID := idFromPath(linkName)

	// The real database session is returned; the symlinked copy is not.
	results, err := vi.Search("database tuning", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	dbFound := false
	for _, r := range results {
		if r.SessionID == linkID {
			t.Fatalf("symlinked session %q must not be indexed", linkName)
		}
		if r.SessionID == dbID {
			dbFound = true
		}
	}
	if !dbFound {
		t.Fatalf("real database session %q missing from results: %+v", dbID, results)
	}

	// The legitimate feline session is still reachable under its own query.
	results, err = vi.Search("kitten care", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	felineFound := false
	for _, r := range results {
		if r.SessionID == felineID {
			felineFound = true
			break
		}
	}
	if !felineFound {
		t.Fatalf("feline session %q missing from results: %+v", felineID, results)
	}
}

// TestVectorIndexRebuildSkipsInvalidName verifies that files with names that
// look like session JSON but do not contain a valid session ID are ignored.
func TestVectorIndexRebuildSkipsInvalidName(t *testing.T) {
	dir := t.TempDir()

	validID := "20260518-abc12345678901234567890123456789"
	writeVectorTestSession(t, dir, validID, []llm.Message{
		{Role: "user", Content: "investigated the feline behavior module"},
	})

	// Invalid names: empty ID after stripping .json, and traversal pattern.
	// Give them database-bucket content so that, if indexed, they would rank
	// highly for a database query.
	invalidNames := []string{".json", "foo..bar.json"}
	for _, name := range invalidNames {
		content := []byte(`{"messages":[{"role":"user","content":"tuned postgres sql indexes"}]}`)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}

	vi := new(VectorIndex)
	if err := vi.InitWithConfig(dir, httpCfgForTests(t)); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	if !vi.Ready() {
		t.Fatal("index should be ready")
	}

	// Invalid files are not indexed, so a database query does not return them.
	results, err := vi.Search("database tuning", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.SessionID == "" || r.SessionID == "foo..bar" {
			t.Fatalf("invalid session id %q must not be indexed", r.SessionID)
		}
	}

	// The legitimate session is still indexed.
	results, err = vi.Search("kitten care", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.SessionID == validID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("valid session %q missing from results: %+v", validID, results)
	}
}
