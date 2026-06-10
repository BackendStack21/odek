package memory

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newHTTPEmbedderFactory returns an EpisodeStore embedder factory backed by
// the mock embeddings server.
func newHTTPEmbedderFactory(srv *httptest.Server) func() textEmbedder {
	cfg := &EmbeddingConfig{Provider: "http", BaseURL: srv.URL + "/v1", Model: "mock-embed"}
	return func() textEmbedder { return newTextEmbedder(cfg, episodeVectorDim) }
}

// TestEpisodeRecallHTTPSemantic: with an HTTP embedding backend, per-turn
// recall matches episodes by MEANING, not vocabulary — the exact gap the RP
// default cannot close (query and episode share zero tokens here).
func TestEpisodeRecallHTTPSemantic(t *testing.T) {
	resetEpIdxes()
	srv, _, _ := mockEmbedServer(t)
	dir := t.TempDir()

	store := NewEpisodeStore(dir, nil)
	store.setEmbedderFactory(newHTTPEmbedderFactory(srv))

	if err := store.Write("sess-cats", "investigated the feline behavior module", 5); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("sess-db", "tuned postgres sql indexes", 5); err != nil {
		t.Fatal(err)
	}

	// "kitten" appears in NO episode summary — only the mock model's semantic
	// space links it to "feline".
	got, err := store.recallByVector("kitten care", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-cats" {
		t.Fatalf("recall = %+v, want sess-cats (semantic match via embedding space)", got)
	}
}

// TestEpisodeIndexFingerprintInvalidation: an index persisted in one embedding
// space must not serve a different one — switching backends forces a rebuild
// in the new space, and the meta file records the new fingerprint.
func TestEpisodeIndexFingerprintInvalidation(t *testing.T) {
	resetEpIdxes()
	srv, _, _ := mockEmbedServer(t)
	dir := t.TempDir()

	// Build + persist the index with the default RP backend.
	rpStore := NewEpisodeStore(dir, nil)
	if err := rpStore.Write("sess-1", "worked on the login credential flow", 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := rpStore.recallByVector("login credential", 1); len(got) != 1 {
		t.Fatal("rp index should serve recall")
	}
	if _, err := os.Stat(filepath.Join(dir, episodeVectorFile)); err != nil {
		t.Fatalf("rp index not persisted: %v", err)
	}

	// Same dir, HTTP backend: persisted RP vectors must be rejected and the
	// index rebuilt — recall works in the new space.
	resetEpIdxes()
	httpStore := NewEpisodeStore(dir, nil)
	httpStore.setEmbedderFactory(newHTTPEmbedderFactory(srv))
	got, err := httpStore.recallByVector("auth problems", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-1" {
		t.Fatalf("recall after backend switch = %+v, want sess-1", got)
	}
	meta, err := os.ReadFile(filepath.Join(dir, episodeIndexMetaFile))
	if err != nil {
		t.Fatalf("meta file not written: %v", err)
	}
	if want := `"http/mock-embed/0"`; !strings.Contains(string(meta), want) {
		t.Errorf("meta = %s, want fingerprint %s", meta, want)
	}

	// And back to RP: the http-stamped meta must invalidate again.
	resetEpIdxes()
	rpStore2 := NewEpisodeStore(dir, nil)
	if got, _ := rpStore2.recallByVector("login credential", 1); len(got) != 1 {
		t.Fatal("rp recall after switching back should rebuild and work")
	}
}

// TestEpisodeIndexLegacyLayoutStillLoads: an index persisted by a pre-meta
// version (gobs present, no meta file) keeps working for the default RP
// backend without a rebuild.
func TestEpisodeIndexLegacyLayoutStillLoads(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()

	store := NewEpisodeStore(dir, nil)
	if err := store.Write("sess-1", "configured postgres database backups", 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.recallByVector("postgres backups", 1); len(got) != 1 {
		t.Fatal("initial recall failed")
	}
	// Simulate the pre-meta on-disk layout.
	if err := os.Remove(filepath.Join(dir, episodeIndexMetaFile)); err != nil {
		t.Fatal(err)
	}

	resetEpIdxes() // force cold start: must load the legacy gobs
	store2 := NewEpisodeStore(dir, nil)
	got, err := store2.recallByVector("postgres backups", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-1" {
		t.Fatalf("legacy layout recall = %+v, want sess-1", got)
	}
}

// TestEpisodeIndexRebuildBackoff: when the embedding backend is down, recall
// degrades to "no context" and the rebuild is NOT retried on every search
// until the cool-down elapses; a new write clears the cool-down.
func TestEpisodeIndexRebuildBackoff(t *testing.T) {
	resetEpIdxes()
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	store := NewEpisodeStore(dir, nil)
	store.setEmbedderFactory(newHTTPEmbedderFactory(srv))
	if err := store.Write("sess-1", "some episode summary text", 5); err != nil {
		t.Fatal(err) // writes succeed — only the vector index needs the backend
	}

	if got, _ := store.recallByVector("query", 1); len(got) != 0 {
		t.Fatalf("recall with backend down = %+v, want empty", got)
	}
	after := requests
	for range 5 {
		store.recallByVector("query", 1) //nolint:errcheck // exercising backoff
	}
	if requests != after {
		t.Errorf("rebuild retried %d times within cool-down, want 0", requests-after)
	}

	// A new write clears the cool-down so fresh data triggers a retry.
	if err := store.Write("sess-2", "another episode summary", 5); err != nil {
		t.Fatal(err)
	}
	store.recallByVector("query", 1) //nolint:errcheck // retry after markDirty
	if requests == after {
		t.Error("markDirty should clear the failure cool-down and retry the rebuild")
	}

	// The failed index must not have been marked clean.
	vi := sharedEpisodeIndex(dir, store.newEmbedder)
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	if vi.ready {
		t.Error("index must not be ready after failed rebuilds")
	}
	if vi.failedAt.IsZero() {
		t.Error("failedAt should be set after a failed rebuild")
	}
	if time.Since(vi.failedAt) > rebuildRetryInterval {
		t.Error("failedAt should be recent")
	}
}
