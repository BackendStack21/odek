package memory

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// EmbeddingConfig selects the embedding backend used by every semantic
// retrieval path in memory: per-turn episode recall, episode dedup, the
// non-LLM episode ranker, and fact merge-on-write.
//
// Provider values:
//
//   - "" or "rp" (default) — go-vector RandomProjections over bag-of-words +
//     bigrams. Fast, local, zero-dependency, but purely lexical: texts with no
//     shared vocabulary score 0 even when they mean the same thing.
//
//   - "http" — any OpenAI-compatible embeddings API (Ollama, llama.cpp server,
//     LM Studio, vLLM, OpenAI, Voyage…) via go-vector's HTTPEmbedder. Real
//     semantic similarity: "fixed the auth bug" matches "repaired login issue".
//     Requires BaseURL and Model; if either is missing the config silently
//     falls back to "rp" so memory keeps working.
//
// BaseURL and APIKey support ${ENV_VAR} expansion so secrets can stay out of
// config files (e.g. "api_key": "${OPENAI_API_KEY}").
type EmbeddingConfig struct {
	Provider string `json:"provider,omitempty"`
	// BaseURL is the API root, e.g. "http://localhost:11434/v1" (Ollama) or
	// "https://api.openai.com/v1"; "/embeddings" is appended by the client.
	BaseURL string `json:"base_url,omitempty"`
	// Model is the embedding model name, e.g. "nomic-embed-text" or
	// "text-embedding-3-small".
	Model string `json:"model,omitempty"`
	// Dims declares the expected vector dimensionality. 0 = infer from the
	// first response (recommended).
	Dims int `json:"dims,omitempty"`
	// APIKey is sent as "Authorization: Bearer <key>" when non-empty.
	APIKey string `json:"api_key,omitempty"`
	// TimeoutSeconds is the per-request HTTP timeout. Default: 10.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// defaultEmbedTimeout bounds embedding API calls. Episode recall runs on the
// per-turn hot path, so a hung embedding server must never stall the loop for
// the HTTPEmbedder's default 30s.
const defaultEmbedTimeout = 10 * time.Second

// textEmbedder is the seam between memory's retrieval paths and the vector
// backend. It abstracts over the two embedding families:
//
//   - corpus-fitted (RandomProjections): fit must run on the FULL corpus
//     before embed, and refit after the corpus changes, or new-term vectors
//     are degenerate. State is per-instance — consumers that fit different
//     corpora need separate instances.
//
//   - stateless (HTTP APIs): fit only warms a cache; embed works at any time
//     and vectors are stable across corpus changes.
//
// fingerprint identifies the embedding space (provider/model/dims). Persisted
// vectors are only reusable by an embedder with the same fingerprint.
type textEmbedder interface {
	// fit prepares the embedder for a corpus of raw (unfeaturized) texts.
	fit(corpus []string) error
	// embed returns the vector for one raw text.
	embed(text string) (vector.Vector, error)
	// embedAll returns vectors for texts in order. A nil vector marks a
	// per-text failure only for implementations that embed one-by-one;
	// batch implementations fail atomically.
	embedAll(texts []string) ([]vector.Vector, error)
	// fingerprint identifies the embedding space for persistence compat.
	fingerprint() string
	// saveState persists fitted state to path (RP gob). No-op when stateless.
	saveState(path string)
	// loadState restores fitted state from path. Returns true when the
	// embedder is usable afterwards (always true for stateless backends).
	loadState(path string) bool
}

// newTextEmbedder builds the embedder selected by cfg. rpDims sets the
// RandomProjections dimensionality used when cfg selects (or falls back to)
// the "rp" provider — callers pass their legacy dims so persisted RP state
// stays loadable. Invalid/incomplete "http" config falls back to "rp".
func newTextEmbedder(cfg *EmbeddingConfig, rpDims int) textEmbedder {
	if cfg == nil || cfg.Provider == "" || cfg.Provider == "rp" {
		return newRPTextEmbedder(rpDims)
	}
	if cfg.Provider != "http" {
		return newRPTextEmbedder(rpDims)
	}
	baseURL := strings.TrimSpace(os.ExpandEnv(cfg.BaseURL))
	model := strings.TrimSpace(cfg.Model)
	if baseURL == "" || model == "" {
		return newRPTextEmbedder(rpDims)
	}
	timeout := defaultEmbedTimeout
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	opts := []vector.HTTPEmbedderOption{
		vector.WithHTTPClient(&http.Client{Timeout: timeout}),
	}
	if key := strings.TrimSpace(os.ExpandEnv(cfg.APIKey)); key != "" {
		opts = append(opts, vector.WithAPIKey(key))
	}
	return &httpTextEmbedder{
		api: vector.NewHTTPEmbedder(baseURL, model, cfg.Dims, opts...),
		fp:  fmt.Sprintf("http/%s/%d", model, cfg.Dims),
	}
}

// ── RandomProjections backend (default) ──────────────────────────────────────

// rpTextEmbedder wraps go-vector RandomProjections behind textEmbedder,
// applying the bigram featurization both at fit and embed time so corpus and
// query vectors live in the same feature space (the invariant featurize.go
// documents).
type rpTextEmbedder struct {
	rp   *vector.RandomProjections
	dims int
}

func newRPTextEmbedder(dims int) *rpTextEmbedder {
	if dims <= 0 {
		dims = defaultOutputDim
	}
	return &rpTextEmbedder{rp: vector.NewRandomProjections(dims), dims: dims}
}

func (e *rpTextEmbedder) fit(corpus []string) error {
	e.rp.Fit(featurizeAll(corpus))
	return nil
}

func (e *rpTextEmbedder) embed(text string) (vector.Vector, error) {
	return e.rp.Embed(featurizeForEmbedding(text))
}

func (e *rpTextEmbedder) embedAll(texts []string) ([]vector.Vector, error) {
	out := make([]vector.Vector, len(texts))
	for i, t := range texts {
		vec, err := e.embed(t)
		if err != nil {
			continue // nil marks the failed entry; callers skip nils
		}
		out[i] = vec
	}
	return out, nil
}

func (e *rpTextEmbedder) fingerprint() string { return fmt.Sprintf("rp/%d", e.dims) }

func (e *rpTextEmbedder) saveState(path string) {
	if tmp := path + ".tmp"; e.rp.SaveEmbedder(tmp) == nil {
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
		}
	}
}

func (e *rpTextEmbedder) loadState(path string) bool {
	emb, err := vector.LoadEmbedder(path)
	if err != nil {
		return false
	}
	e.rp = emb
	return true
}

// ── HTTP backend (OpenAI-compatible APIs) ─────────────────────────────────────

// maxEmbedCacheEntries bounds the text→vector cache. Sized comfortably above
// the episode cap (500) and fact-entry counts; when exceeded the cache resets
// rather than evicting — simpler, and a full reset is just one extra batch
// call on the next fit.
const maxEmbedCacheEntries = 4096

// httpTextEmbedder adapts vector.HTTPEmbedder to textEmbedder. Texts are sent
// raw (no bigram featurization — transformer models need natural text). The
// cache makes repeated fit calls over a mostly-unchanged corpus cheap: only
// texts not seen before hit the network, in a single batch call.
type httpTextEmbedder struct {
	api *vector.HTTPEmbedder
	fp  string

	mu    sync.Mutex
	cache map[string]vector.Vector
}

func (e *httpTextEmbedder) fit(corpus []string) error {
	_, err := e.embedAll(corpus)
	return err
}

func (e *httpTextEmbedder) embed(text string) (vector.Vector, error) {
	e.mu.Lock()
	if vec, ok := e.cache[text]; ok {
		e.mu.Unlock()
		return vec, nil
	}
	e.mu.Unlock()

	vec, err := e.api.Embed(text)
	if err != nil {
		return nil, err
	}
	e.storeInCache(map[string]vector.Vector{text: vec})
	return vec, nil
}

func (e *httpTextEmbedder) embedAll(texts []string) ([]vector.Vector, error) {
	out := make([]vector.Vector, len(texts))

	// Collect cache misses, deduplicating repeated texts within the batch.
	e.mu.Lock()
	missSet := make(map[string]bool)
	var misses []string
	for i, t := range texts {
		if vec, ok := e.cache[t]; ok {
			out[i] = vec
		} else if !missSet[t] {
			missSet[t] = true
			misses = append(misses, t)
		}
	}
	e.mu.Unlock()

	if len(misses) > 0 {
		vecs, err := e.api.EmbedBatch(misses)
		if err != nil {
			return nil, err
		}
		fetched := make(map[string]vector.Vector, len(misses))
		for i, t := range misses {
			fetched[t] = vecs[i]
		}
		e.storeInCache(fetched)
		for i, t := range texts {
			if out[i] == nil {
				out[i] = fetched[t]
			}
		}
	}
	return out, nil
}

func (e *httpTextEmbedder) storeInCache(vecs map[string]vector.Vector) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cache == nil || len(e.cache)+len(vecs) > maxEmbedCacheEntries {
		e.cache = make(map[string]vector.Vector, len(vecs))
	}
	maps.Copy(e.cache, vecs)
}

func (e *httpTextEmbedder) fingerprint() string { return e.fp }

func (e *httpTextEmbedder) saveState(string) {}

func (e *httpTextEmbedder) loadState(string) bool { return true }
