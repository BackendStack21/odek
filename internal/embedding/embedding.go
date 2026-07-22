// Package embedding is the shared text-embedding seam used by every semantic
// retrieval path in odek: memory (episode recall, dedup, ranking, fact merge),
// session search, and skill matching. It abstracts over two embedding families
// behind a single TextEmbedder interface:
//
//   - corpus-fitted (RandomProjections): a fast, local, zero-dependency lexical
//     backend over bag-of-words + bigrams.
//   - stateless (HTTP APIs): any OpenAI-compatible embeddings endpoint, giving
//     real semantic similarity.
//
// Extracted from internal/memory so sessions and skills can reuse the exact
// same backends, config shape, and fingerprint semantics.
package embedding

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// Cosine computes cosine similarity between two go-vector Vectors. Mismatched
// or empty inputs score 0. A buggy/hostile embedding backend can return
// NaN/Inf components; a NaN score breaks sort ordering (non-strict-weak), so it
// is clamped to 0 ("no similarity") to keep ranking well-defined.
func Cosine(a, b vector.Vector) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		da := float64(a[i])
		db := float64(b[i])
		dot += da * db
		normA += da * da
		normB += db * db
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	sim := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	if math.IsNaN(sim) || math.IsInf(sim, 0) {
		return 0
	}
	return float32(sim)
}

// Config selects the embedding backend.
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
//     falls back to "rp" so retrieval keeps working.
//
// BaseURL and APIKey support ${ENV_VAR} expansion so secrets can stay out of
// config files (e.g. "api_key": "${OPENAI_API_KEY}").
type Config struct {
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

// DefaultRPDim is the default RandomProjections dimensionality used when a
// caller passes rpDims <= 0.
const DefaultRPDim = 256

// DefaultTimeout bounds embedding API calls. Recall runs on the per-turn hot
// path, so a hung embedding server must never stall the loop for the
// HTTPEmbedder's default 30s.
const DefaultTimeout = 10 * time.Second

// TextEmbedder is the seam between retrieval paths and the vector backend. It
// abstracts over the two embedding families:
//
//   - corpus-fitted (RandomProjections): Fit must run on the FULL corpus
//     before Embed, and refit after the corpus changes, or new-term vectors
//     are degenerate. State is per-instance — consumers that fit different
//     corpora need separate instances.
//
//   - stateless (HTTP APIs): Fit only warms a cache; Embed works at any time
//     and vectors are stable across corpus changes.
//
// Fingerprint identifies the embedding space (provider/model/dims). Persisted
// vectors are only reusable by an embedder with the same fingerprint.
type TextEmbedder interface {
	// Fit prepares the embedder for a corpus of raw (unfeaturized) texts.
	Fit(corpus []string) error
	// Embed returns the vector for one raw text.
	Embed(text string) (vector.Vector, error)
	// EmbedAll returns vectors for texts in order. A nil vector marks a
	// per-text failure only for implementations that embed one-by-one;
	// batch implementations fail atomically.
	EmbedAll(texts []string) ([]vector.Vector, error)
	// Fingerprint identifies the embedding space for persistence compat.
	Fingerprint() string
	// SaveState persists fitted state to path (RP gob). No-op when stateless.
	SaveState(path string)
	// LoadState restores fitted state from path. Returns true when the
	// embedder is usable afterwards (always true for stateless backends).
	LoadState(path string) bool
}

// New builds the embedder selected by cfg. rpDims sets the RandomProjections
// dimensionality used when cfg selects (or falls back to) the "rp" provider —
// callers pass their legacy dims so persisted RP state stays loadable.
// Invalid/incomplete "http" config falls back to "rp".
func New(cfg *Config, rpDims int) TextEmbedder {
	if cfg == nil || cfg.Provider == "" || cfg.Provider == "rp" {
		return NewRP(rpDims)
	}
	if cfg.Provider != "http" {
		return NewRP(rpDims)
	}
	baseURL := strings.TrimSpace(os.ExpandEnv(cfg.BaseURL))
	model := strings.TrimSpace(cfg.Model)
	if baseURL == "" || model == "" {
		return NewRP(rpDims)
	}
	timeout := DefaultTimeout
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

// Shared returns an embedder factory like New, except that for stateless
// (HTTP) backends every call yields the SAME instance so its text→vector
// cache is shared across consumers — e.g. episode dedup and the episode
// vector-index rebuild then embed each corpus text once per process instead
// of once per pass. Corpus-fitted backends (RandomProjections) still get a
// FRESH instance per call: their Fit state is per-corpus, so sharing would
// produce degenerate vectors. The HTTP embedder is internally mutex-guarded
// and its Fit only warms the cache, so sharing it across goroutines is safe.
func Shared(cfg *Config, rpDims int) func() TextEmbedder {
	if emb := New(cfg, rpDims); isStateless(emb) {
		return func() TextEmbedder { return emb }
	}
	return func() TextEmbedder { return New(cfg, rpDims) }
}

// isStateless reports whether the embedder is safe to share across consumers
// (Fit does not capture corpus state).
func isStateless(emb TextEmbedder) bool {
	_, ok := emb.(*httpTextEmbedder)
	return ok
}

// ── RandomProjections backend (default) ──────────────────────────────────────

// rpTextEmbedder wraps go-vector RandomProjections behind TextEmbedder,
// applying the bigram featurization both at fit and embed time so corpus and
// query vectors live in the same feature space (the invariant featurize.go
// documents).
type rpTextEmbedder struct {
	rp   *vector.RandomProjections
	dims int
}

// NewRP builds a RandomProjections embedder of the given dimensionality. Pass
// dims <= 0 for DefaultRPDim.
func NewRP(dims int) TextEmbedder {
	if dims <= 0 {
		dims = DefaultRPDim
	}
	return &rpTextEmbedder{rp: vector.NewRandomProjections(dims), dims: dims}
}

func (e *rpTextEmbedder) Fit(corpus []string) error {
	e.rp.Fit(featurizeAll(corpus))
	return nil
}

func (e *rpTextEmbedder) Embed(text string) (vector.Vector, error) {
	return e.rp.Embed(featurizeForEmbedding(text))
}

func (e *rpTextEmbedder) EmbedAll(texts []string) ([]vector.Vector, error) {
	out := make([]vector.Vector, len(texts))
	for i, t := range texts {
		vec, err := e.Embed(t)
		if err != nil {
			continue // nil marks the failed entry; callers skip nils
		}
		out[i] = vec
	}
	return out, nil
}

func (e *rpTextEmbedder) Fingerprint() string { return fmt.Sprintf("rp/%d", e.dims) }

func (e *rpTextEmbedder) SaveState(path string) {
	if tmp := path + ".tmp"; e.rp.SaveEmbedder(tmp) == nil {
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
		}
	}
}

func (e *rpTextEmbedder) LoadState(path string) bool {
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

// httpTextEmbedder adapts vector.HTTPEmbedder to TextEmbedder. Texts are sent
// raw (no bigram featurization — transformer models need natural text). The
// cache makes repeated fit calls over a mostly-unchanged corpus cheap: only
// texts not seen before hit the network, in a single batch call.
type httpTextEmbedder struct {
	api *vector.HTTPEmbedder
	fp  string

	mu    sync.Mutex
	cache map[string]vector.Vector
}

func (e *httpTextEmbedder) Fit(corpus []string) error {
	_, err := e.EmbedAll(corpus)
	return err
}

func (e *httpTextEmbedder) Embed(text string) (vector.Vector, error) {
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

func (e *httpTextEmbedder) EmbedAll(texts []string) ([]vector.Vector, error) {
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

func (e *httpTextEmbedder) Fingerprint() string { return e.fp }

func (e *httpTextEmbedder) SaveState(string) {}

func (e *httpTextEmbedder) LoadState(string) bool { return true }
