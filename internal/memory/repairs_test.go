package memory

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// These tests close the gaps and harden the failure modes surfaced by the
// PR #27 verification pass (AI Verification Protocol). They guard:
//   - C10: api_key ${ENV_VAR} expansion (only base_url was asserted before).
//   - C12: graceful degradation of the ranker (recency) and merge (no-merge)
//     paths when embedding errors — safety-critical, previously untested.
//   - C13: dedup never deletes a matched episode on an embed error.
//   - The cosineVector NaN/Inf guard (hostile embedding backend).
//   - The HIGH finding: rebuild embeds OFF the index lock, so a slow embedding
//     backend cannot serialize concurrent recall.

// ── C10: api_key env expansion ────────────────────────────────────────────────

// TestNewTextEmbedderExpandsAPIKey asserts api_key ${ENV_VAR} expansion reaches
// the Authorization header — the half of C10 not covered by the base_url test.
func TestNewTextEmbedderExpandsAPIKey(t *testing.T) {
	t.Setenv("ODEK_TEST_EMBED_KEY", "sk-expanded")
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAuth <- r.Header.Get("Authorization"):
		default:
		}
		json.NewEncoder(w).Encode(struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{{0, []float32{1, 0}}}})
	}))
	defer srv.Close()

	emb := newTextEmbedder(&EmbeddingConfig{
		Provider: "http", BaseURL: srv.URL + "/v1", Model: "m",
		APIKey: "${ODEK_TEST_EMBED_KEY}",
	}, 64)
	if _, err := emb.embed("hi"); err != nil {
		t.Fatal(err)
	}
	if got := <-gotAuth; got != "Bearer sk-expanded" {
		t.Errorf("Authorization = %q, want Bearer sk-expanded (api_key env expansion)", got)
	}
}

// ── failingEmbedder: an embedder whose embed paths always error ───────────────

type failingEmbedder struct{}

func (failingEmbedder) fit([]string) error                         { return errEmbed }
func (failingEmbedder) embed(string) (vector.Vector, error)        { return nil, errEmbed }
func (failingEmbedder) embedAll([]string) ([]vector.Vector, error) { return nil, errEmbed }
func (failingEmbedder) fingerprint() string                        { return "failing/0" }
func (failingEmbedder) saveState(string)                           {}
func (failingEmbedder) loadState(string) bool                      { return false }

var errEmbed = errors.New("embed: backend unavailable")

// ── C12: ranker degrades to recency on embed error ────────────────────────────

func TestEmbedderRankerRecencyFallbackOnError(t *testing.T) {
	ranker := newEmbedderRanker(func() textEmbedder { return failingEmbedder{} })
	in := []EpisodeMeta{
		{SessionID: "a", Summary: "first"},
		{SessionID: "b", Summary: "second"},
		{SessionID: "c", Summary: "third"},
	}
	out, err := ranker("any query", in)
	if err != nil {
		t.Fatalf("ranker should not surface embed errors, got %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("ranker dropped entries: got %d want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].SessionID != in[i].SessionID {
			t.Errorf("recency fallback should preserve input order: pos %d = %q want %q",
				i, out[i].SessionID, in[i].SessionID)
		}
	}
}

// ── C12: merge classification degrades to "nobody" on embed error ─────────────

func TestMergeDetectorAddWithoutMergeOnEmbedError(t *testing.T) {
	md := newMergeDetectorWithEmbedder(failingEmbedder{}, MergeThreshold, AddThreshold)
	md.Fit([]string{"user prefers postgres", "project uses go"})
	action, idx, sim := md.Classify("user prefers postgres")
	if action != "nobody" {
		t.Errorf("on embed error Classify should return 'nobody' (add without merge), got %q (idx=%d sim=%v)", action, idx, sim)
	}
}

// ── C13: dedup never deletes a matched episode on embed error ─────────────────

func TestFindDuplicateSafeOnEmbedError(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	store := NewEpisodeStoreWithLifecycle(dir, nil, 0.9, 0, 0) // dedup enabled
	store.setEmbedderFactory(func() textEmbedder { return failingEmbedder{} })

	if err := store.Write("sess-1", "the original episode summary", 5); err != nil {
		t.Fatal(err)
	}
	// A second, near-identical write: with a working embedder this could dedup
	// (replace) sess-1. With a failing embedder, findDuplicate must return
	// (-1,0) so writeLocked never deletes sess-1.
	if err := store.Write("sess-2", "the original episode summary v2", 5); err != nil {
		t.Fatal(err)
	}
	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range idx {
		have[e.SessionID] = true
	}
	if !have["sess-1"] {
		t.Error("sess-1 was deleted — a failed dedup embed must NOT evict the matched episode")
	}
	if !have["sess-2"] {
		t.Error("sess-2 was not stored")
	}
}

// ── cosineVector NaN/Inf guard ────────────────────────────────────────────────

func TestCosineVectorGuardsNaNInf(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	if got := cosineVector(vector.Vector{nan, 1}, vector.Vector{1, 1}); got != 0 {
		t.Errorf("cosine with NaN component = %v, want 0", got)
	}
	if got := cosineVector(vector.Vector{inf, 1}, vector.Vector{1, 1}); got != 0 {
		t.Errorf("cosine with Inf component = %v, want 0", got)
	}
	// Sanity: a normal pair still scores > 0.
	if got := cosineVector(vector.Vector{1, 0}, vector.Vector{1, 0}); got <= 0 {
		t.Errorf("cosine of identical vectors = %v, want > 0", got)
	}
}

// ── HIGH finding: rebuild embeds OFF the lock; slow backend ≠ serialized recall ─

// TestRebuildDoesNotSerializeRecall proves the fix for the protocol's one HIGH
// finding: while one goroutine is blocked in a rebuild's batch embed against a
// slow backend, concurrent recalls return promptly (single-flight serves the
// current state) instead of queueing behind the rebuild under the index lock.
func TestRebuildDoesNotSerializeRecall(t *testing.T) {
	resetEpIdxes()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal that the rebuild's embed call has reached the server, then
		// block until released — simulating a slow embedding backend.
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for i := range req.Input {
			out.Data = append(out.Data, datum{Index: i, Embedding: []float32{1, 0, 0, 0, 0, 0, 0, 0.1}})
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	dir := t.TempDir()
	store := NewEpisodeStore(dir, nil)
	store.setEmbedderFactory(newHTTPEmbedderFactory(srv))
	if err := store.Write("sess-1", "episode summary one", 5); err != nil {
		t.Fatal(err)
	}

	// G1 triggers the rebuild and blocks inside the embed call.
	go func() { store.recallByVector("query", 1) }() //nolint:errcheck // blocked rebuild
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("rebuild embed never reached the backend")
	}

	// While G1 is blocked under the (released-held) backend, concurrent recalls
	// must NOT hang behind it. If rebuild still held the index write lock during
	// the network call, these would block until release and time out.
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range 5 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				store.recallByVector("query", 1) //nolint:errcheck // must return promptly
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("concurrent recalls were serialized behind the slow rebuild (lock held during network I/O)")
	}

	// Release the backend; the rebuild completes and a subsequent recall works.
	close(release)
}
