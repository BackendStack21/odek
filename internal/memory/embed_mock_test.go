package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// mockEmbedServer serves the OpenAI embeddings wire format with deterministic,
// keyword-bucketed vectors so semantically "related" texts (e.g. "feline" and
// "kitten") get identical vectors even with zero lexical overlap. It mirrors
// the helper in internal/embedding's tests; memory keeps its own copy because
// Go test helpers are package-private. requests/texts track network usage.
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
		for _, w := range bucket {
			for _, tok := range words {
				if strings.Trim(tok, ".,;:!?") == w {
					v[dim] = 1
				}
			}
		}
	}
	v[7] = 0.1 // default direction so no vector is all-zero
	return v
}
