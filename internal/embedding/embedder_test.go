package embedding

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// mockEmbedServer serves the OpenAI embeddings wire format. Each text maps to
// a deterministic 8-dim vector keyed on which of a few known words it
// contains, so semantically "related" mock texts get identical vectors.
// requestCount and textCount track network usage for cache assertions.
func mockEmbedServer(t *testing.T) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var requests, texts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		texts.Add(int64(len(req.Input)))
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
	return srv, &requests, &texts
}

// mockVectorFor maps a text onto an 8-dim unit-ish vector by keyword buckets.
// Texts sharing a bucket are "semantically identical" to the mock model even
// with zero lexical overlap (e.g. "feline" and "cat").
func mockVectorFor(text string) []float32 {
	v := make([]float32, 8)
	buckets := map[int][]string{
		0: {"cat", "feline", "kitten"},
		1: {"database", "postgres", "sql"},
		2: {"auth", "login", "credential"},
	}
	for dim, words := range buckets {
		for _, w := range words {
			if containsWord(text, w) {
				v[dim] = 1
			}
		}
	}
	// Default direction so no vector is all-zero.
	v[7] = 0.1
	return v
}

func containsWord(text, w string) bool {
	return slices.Contains(strings.Fields(normalizeForEmbedding(text)), w)
}

func httpCfg(srv *httptest.Server) *Config {
	return &Config{
		Provider: "http",
		BaseURL:  srv.URL + "/v1",
		Model:    "mock-embed",
	}
}

func TestNewTextEmbedderDefaultsToRP(t *testing.T) {
	for _, cfg := range []*Config{
		nil,
		{},
		{Provider: "rp"},
		{Provider: "http"},                       // missing base_url + model
		{Provider: "http", BaseURL: "http://x"},  // missing model
		{Provider: "http", Model: "m"},           // missing base_url
		{Provider: "something-else", Model: "m"}, // unknown provider
	} {
		emb := New(cfg, 64)
		if _, ok := emb.(*rpTextEmbedder); !ok {
			t.Errorf("New(%+v) = %T, want *rpTextEmbedder", cfg, emb)
		}
		if got := emb.Fingerprint(); got != "rp/64" {
			t.Errorf("fingerprint = %q, want rp/64", got)
		}
	}
}

func TestNewTextEmbedderHTTP(t *testing.T) {
	srv, _, _ := mockEmbedServer(t)
	emb := New(httpCfg(srv), 64)
	he, ok := emb.(*httpTextEmbedder)
	if !ok {
		t.Fatalf("New = %T, want *httpTextEmbedder", emb)
	}
	if got := he.Fingerprint(); got != "http/mock-embed/0" {
		t.Errorf("fingerprint = %q, want http/mock-embed/0", got)
	}
}

func TestNewTextEmbedderExpandsEnv(t *testing.T) {
	t.Setenv("ODEK_TEST_EMBED_URL", "http://localhost:9999/v1")
	emb := New(&Config{
		Provider: "http",
		BaseURL:  "${ODEK_TEST_EMBED_URL}",
		Model:    "m",
	}, 64)
	if _, ok := emb.(*httpTextEmbedder); !ok {
		t.Fatalf("env-expanded base_url should yield http embedder, got %T", emb)
	}
}

func TestHTTPEmbedderSemanticMatch(t *testing.T) {
	srv, _, _ := mockEmbedServer(t)
	emb := New(httpCfg(srv), 64)

	a, err := emb.Embed("the feline sat on the mat")
	if err != nil {
		t.Fatal(err)
	}
	b, err := emb.Embed("a cat appeared")
	if err != nil {
		t.Fatal(err)
	}
	c, err := emb.Embed("postgres database migration")
	if err != nil {
		t.Fatal(err)
	}
	if simAB := Cosine(a, b); simAB < 0.9 {
		t.Errorf("cat/feline cosine = %v, want ≥ 0.9 (semantic match)", simAB)
	}
	if simAC := Cosine(a, c); simAC > 0.5 {
		t.Errorf("cat/database cosine = %v, want < 0.5", simAC)
	}
}

func TestHTTPEmbedderCachesRepeatEmbeds(t *testing.T) {
	srv, requests, _ := mockEmbedServer(t)
	emb := New(httpCfg(srv), 64)

	if _, err := emb.Embed("hello world"); err != nil {
		t.Fatal(err)
	}
	if _, err := emb.Embed("hello world"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (second embed should hit cache)", got)
	}
}

func TestHTTPEmbedderFitBatchesOnlyMisses(t *testing.T) {
	srv, requests, texts := mockEmbedServer(t)
	emb := New(httpCfg(srv), 64)

	corpus := []string{"one", "two", "three"}
	if err := emb.Fit(corpus); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("fit requests = %d, want 1 (single batch)", got)
	}
	if got := texts.Load(); got != 3 {
		t.Errorf("texts sent = %d, want 3", got)
	}

	// Refit with one new entry: only the miss goes over the wire.
	if err := emb.Fit(append(corpus, "four")); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("requests after refit = %d, want 2", got)
	}
	if got := texts.Load(); got != 4 {
		t.Errorf("texts sent after refit = %d, want 4 (only the new entry)", got)
	}
}

func TestHTTPEmbedderEmbedAllDedupsWithinBatch(t *testing.T) {
	srv, _, texts := mockEmbedServer(t)
	emb := New(httpCfg(srv), 64)

	vecs, err := emb.EmbedAll([]string{"same", "same", "same"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || vecs[0] == nil || vecs[1] == nil || vecs[2] == nil {
		t.Fatalf("embedAll returned %d vectors with nils, want 3 non-nil", len(vecs))
	}
	if got := texts.Load(); got != 1 {
		t.Errorf("texts sent = %d, want 1 (in-batch dedup)", got)
	}
}

func TestHTTPEmbedderErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	emb := New(&Config{Provider: "http", BaseURL: srv.URL + "/v1", Model: "m"}, 64)

	if _, err := emb.Embed("x"); err == nil {
		t.Fatal("embed should propagate API errors")
	}
	if err := emb.Fit([]string{"a", "b"}); err == nil {
		t.Fatal("fit should propagate API errors")
	}
}

func TestRPTextEmbedderRoundTrip(t *testing.T) {
	emb := NewRP(64)
	corpus := []string{"uses postgres for storage", "prefers tabs over spaces"}
	if err := emb.Fit(corpus); err != nil {
		t.Fatal(err)
	}
	vecs, err := emb.EmbedAll(corpus)
	if err != nil {
		t.Fatal(err)
	}
	q, err := emb.Embed("postgres storage")
	if err != nil {
		t.Fatal(err)
	}
	if simSame := Cosine(q, vecs[0]); simSame <= Cosine(q, vecs[1]) {
		t.Errorf("query should be closer to the postgres entry: %v vs %v",
			simSame, Cosine(q, vecs[1]))
	}

	// Persistence round-trip.
	path := t.TempDir() + "/rp.gob"
	emb.SaveState(path)
	emb2 := NewRP(64)
	if !emb2.LoadState(path) {
		t.Fatal("loadState failed")
	}
	q2, err := emb2.Embed("postgres storage")
	if err != nil {
		t.Fatal(err)
	}
	if Cosine(q, q2) < 0.999 {
		t.Errorf("loaded embedder should reproduce vectors, cosine = %v", Cosine(q, q2))
	}
}

func TestHTTPEmbedderCacheResetWhenFull(t *testing.T) {
	srv, _, _ := mockEmbedServer(t)
	emb := New(httpCfg(srv), 64).(*httpTextEmbedder)

	// Fill past the cap in chunks; the cache must reset, not grow unbounded.
	batch := make([]string, 512)
	for round := range 10 {
		for i := range batch {
			batch[i] = fmt.Sprintf("text-%d-%d", round, i)
		}
		if _, err := emb.EmbedAll(batch); err != nil {
			t.Fatal(err)
		}
	}
	emb.mu.Lock()
	size := len(emb.cache)
	emb.mu.Unlock()
	if size > maxEmbedCacheEntries {
		t.Errorf("cache size = %d, want ≤ %d", size, maxEmbedCacheEntries)
	}
}

// TestSharedFactory: the Shared factory returns ONE cache-warm instance for
// stateless (HTTP) backends so consumers like episode dedup and the vector
// index rebuild share the text→vector cache, but a FRESH instance per call
// for corpus-fitted (RandomProjections) backends whose Fit state is per-corpus.
func TestSharedFactory(t *testing.T) {
	rpFactory := Shared(nil, 0)
	if rpFactory() == rpFactory() {
		t.Error("rp backend: Shared must return a fresh instance per call (per-corpus Fit state)")
	}

	srv, _, _ := mockEmbedServer(t)
	httpFactory := Shared(&Config{Provider: "http", BaseURL: srv.URL + "/v1", Model: "mock-embed"}, 0)
	first, second := httpFactory(), httpFactory()
	if first != second {
		t.Fatal("http backend: Shared must return the same cache-warm instance")
	}
	// Sanity: the shared instance works and caches across Fit calls.
	if err := first.Fit([]string{"hello world"}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Embed("hello world"); err != nil {
		t.Fatal(err)
	}
}
