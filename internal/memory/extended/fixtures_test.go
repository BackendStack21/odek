package extended

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
)

// mockLLM is a concurrency-safe LLM mock for tests.
type mockLLM struct {
	mu        sync.Mutex
	responses []string
	lastUser  string
	lastSys   string
	callCount int
}

func newMockLLM(responses ...string) *mockLLM {
	return &mockLLM{responses: responses}
}

func (m *mockLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSys = system
	m.lastUser = user
	m.callCount++
	if len(m.responses) > 0 {
		r := m.responses[0]
		m.responses = m.responses[1:]
		return r, nil
	}
	return "", nil
}

func (m *mockLLM) lastUserPrompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastUser
}

// mockEmbedder is a deterministic embedding backend for tests. It produces
// a one-hot vector based on the hash of the text so cosine similarity is
// deterministic and stable across Fit/Embed calls.
type mockEmbedder struct {
	mu    sync.Mutex
	cache map[string]vector.Vector
	dims  int
}

func newMockEmbedder(dims int) *mockEmbedder {
	return &mockEmbedder{cache: make(map[string]vector.Vector), dims: dims}
}

func (e *mockEmbedder) Fit(corpus []string) error { return nil }

func (e *mockEmbedder) Embed(text string) (vector.Vector, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if vec, ok := e.cache[text]; ok {
		return vec, nil
	}
	vec := textHashVector(text, e.dims)
	e.cache[text] = vec
	return vec, nil
}

func (e *mockEmbedder) EmbedAll(texts []string) ([]vector.Vector, error) {
	out := make([]vector.Vector, len(texts))
	for i, t := range texts {
		vec, err := e.Embed(t)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (e *mockEmbedder) Fingerprint() string { return fmt.Sprintf("mock/%d", e.dims) }

func (e *mockEmbedder) SaveState(path string) {
	_ = os.WriteFile(path, []byte(e.Fingerprint()), 0600)
}

func (e *mockEmbedder) LoadState(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(data) == e.Fingerprint()
}

func (e *mockEmbedder) cacheSize() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.cache)
}

// textHashVector creates a simple deterministic sparse vector from text.
func textHashVector(text string, dims int) vector.Vector {
	vec := make(vector.Vector, dims)
	for _, c := range strings.ToLower(text) {
		idx := int(c) % dims
		vec[idx] += 1.0
	}
	return vec
}

// ensure mockEmbedder implements the embedding.TextEmbedder interface.
var _ embedding.TextEmbedder = (*mockEmbedder)(nil)

// extractJSONResponse builds a valid JSON extraction response.
func extractJSONResponse(items ...string) string {
	var parts []string
	for _, item := range items {
		parts = append(parts, fmt.Sprintf(`{"text":%q,"type":"observation","confidence":0.9}`, item))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
