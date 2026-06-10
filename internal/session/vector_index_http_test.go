package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/llm"
)

// mockEmbedServer serves the OpenAI embeddings wire format with deterministic,
// keyword-bucketed vectors so semantically "related" texts (e.g. "feline" and
// "kitten") get identical vectors even with zero lexical overlap. requests
// tracks how many times the backend is hit (for backoff assertions).
func mockEmbedServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for i, txt := range req.Input {
			out.Data = append(out.Data, datum{Index: i, Embedding: mockVectorFor(txt)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// mockVectorFor maps a text onto an 8-dim vector by keyword buckets. Texts
// sharing a bucket are "semantically identical" to the mock model.
func mockVectorFor(text string) []float32 {
	v := make([]float32, 8)
	buckets := map[int][]string{
		0: {"cat", "feline", "kitten"},
		1: {"database", "postgres", "sql"},
		2: {"auth", "login", "credential"},
	}
	words := strings.Fields(strings.ToLower(text))
	for dim, bucket := range buckets {
		for _, kw := range bucket {
			for _, tok := range words {
				if strings.Trim(tok, ".,;:!?[]") == kw {
					v[dim] = 1
				}
			}
		}
	}
	v[7] = 0.1 // default direction so no vector is all-zero
	return v
}

func httpEmbedConfig(srv *httptest.Server) *embedding.Config {
	return &embedding.Config{Provider: "http", BaseURL: srv.URL + "/v1", Model: "mock-embed"}
}

// writeSessionFile writes a minimal session JSON the index can scan.
func writeSessionFile(t *testing.T, dir, id string, msgs []llm.Message) {
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

// TestVectorIndexHTTPSemantic: with an HTTP embedding backend, session search
// matches by MEANING, not vocabulary — the gap the RP default cannot close
// (query "kitten" shares zero tokens with the indexed "feline" session).
func TestVectorIndexHTTPSemantic(t *testing.T) {
	srv, _ := mockEmbedServer(t)
	dir := t.TempDir()

	writeSessionFile(t, dir, "sess-cats", []llm.Message{
		{Role: "user", Content: "investigated the feline behavior module"},
	})
	writeSessionFile(t, dir, "sess-db", []llm.Message{
		{Role: "user", Content: "tuned postgres sql indexes"},
	})

	vi := new(VectorIndex)
	if err := vi.InitWithConfig(dir, httpEmbedConfig(srv)); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}

	results, err := vi.Search("kitten care", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != "sess-cats" {
		t.Fatalf("search = %+v, want sess-cats (semantic match via embedding space)", results)
	}
}

// TestVectorIndexFingerprintInvalidation: an index persisted in one embedding
// space must not be served by a different one — switching backends forces a
// rebuild and the meta file records the new fingerprint.
func TestVectorIndexFingerprintInvalidation(t *testing.T) {
	srv, _ := mockEmbedServer(t)
	dir := t.TempDir()

	writeSessionFile(t, dir, "sess-1", []llm.Message{
		{Role: "user", Content: "worked on the login credential flow"},
	})

	// Build + persist with the default RP backend.
	rp := new(VectorIndex)
	if err := rp.Init(dir); err != nil {
		t.Fatalf("rp Init: %v", err)
	}
	if !rp.Ready() {
		t.Fatal("rp index should be ready")
	}
	meta, err := os.ReadFile(filepath.Join(dir, vectorMetaFile))
	if err != nil {
		t.Fatalf("meta not written by rp build: %v", err)
	}
	if !strings.Contains(string(meta), `"rp/256"`) {
		t.Errorf("rp meta = %s, want rp/256 fingerprint", meta)
	}

	// Same dir, HTTP backend: persisted RP vectors must be rejected and the
	// index rebuilt in the new space; the meta is restamped.
	httpIdx := new(VectorIndex)
	if err := httpIdx.InitWithConfig(dir, httpEmbedConfig(srv)); err != nil {
		t.Fatalf("http Init: %v", err)
	}
	got, err := httpIdx.Search("auth problems", 1)
	if err != nil {
		t.Fatalf("http Search: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-1" {
		t.Fatalf("search after backend switch = %+v, want sess-1", got)
	}
	meta, err = os.ReadFile(filepath.Join(dir, vectorMetaFile))
	if err != nil {
		t.Fatalf("meta not rewritten: %v", err)
	}
	if want := `"http/mock-embed/0"`; !strings.Contains(string(meta), want) {
		t.Errorf("meta = %s, want fingerprint %s", meta, want)
	}
}

// TestVectorIndexRebuildBackoff: when the embedding backend is down, Init
// leaves the index not-ready (search falls back to keyword) and the rebuild is
// NOT retried on every search until the cool-down elapses.
func TestVectorIndexRebuildBackoff(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeSessionFile(t, dir, "sess-1", []llm.Message{
		{Role: "user", Content: "some session content"},
	})

	vi := new(VectorIndex)
	if err := vi.InitWithConfig(dir, httpEmbedConfig(srv)); err != nil {
		t.Fatalf("InitWithConfig should be non-fatal when backend down: %v", err)
	}
	if vi.Ready() {
		t.Fatal("index must not be ready when the backend is down")
	}

	// Init already made one failed attempt; further searches within the
	// cool-down must not re-hit the backend.
	after := requests
	for range 5 {
		if got, _ := vi.Search("query", 1); got != nil {
			t.Fatalf("search with backend down = %+v, want nil (keyword fallback)", got)
		}
	}
	if requests != after {
		t.Errorf("rebuild retried %d times within cool-down, want 0", requests-after)
	}
}
