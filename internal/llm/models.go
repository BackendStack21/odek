// Package llm provides an OpenAI-compatible HTTP client using only stdlib.
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// modelCache caches the full model list per endpoint so multiple model
// lookups from the same provider share a single API call.
var (
	modelCache   map[string]map[string]int // key: "baseURL|apiKey" → modelID → contextLength
	modelCacheMu sync.RWMutex
)

func init() {
	modelCache = make(map[string]map[string]int)
}

// cacheKey returns a unique key for the (baseURL, apiKey) pair.
func cacheKey(baseURL, apiKey string) string {
	return baseURL + "|" + apiKey
}

// rawModel is a single model entry from the /models endpoint.
// Different providers use different field names for context length.
type rawModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`   // OpenRouter, Together, some providers
	MaxContext    int    `json:"max_context"`      // Fallback field name
	MaxInput      int    `json:"max_input_tokens"` // Common alternative
}

// modelsResponse is the top-level response from GET /models.
type modelsResponse struct {
	Data   []rawModel `json:"data"`
	Models []rawModel `json:"models"` // Some providers use this wrapper
}

// ResetModelCache clears the model discovery cache. Used in tests.
func ResetModelCache() {
	modelCacheMu.Lock()
	modelCache = make(map[string]map[string]int)
	modelCacheMu.Unlock()
}

// DiscoverModelContext queries the /models endpoint of the configured base URL
// to discover the context window for the given model. Returns 0 if the
// endpoint doesn't support model attribute discovery or the model isn't found.
//
// Results are cached per (baseURL, apiKey) so multiple agents using the same
// provider share a single API call. The full model list is cached so
// different model lookups from the same endpoint don't re-query.
//
// Call this at startup before creating the engine. The HTTP call uses a 5s
// timeout and never blocks startup for more than that.
func DiscoverModelContext(baseURL, apiKey, model string) int {
	cacheK := cacheKey(baseURL, apiKey)

	// Check cache first
	modelCacheMu.RLock()
	if models, ok := modelCache[cacheK]; ok {
		if val, ok2 := models[model]; ok2 {
			modelCacheMu.RUnlock()
			return val
		}
		// Model not in cached list — don't re-query
		modelCacheMu.RUnlock()
		return 0
	}
	modelCacheMu.RUnlock()

	// Query the API
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0
	}

	// Search both possible array fields
	models := parsed.Data
	if len(models) == 0 {
		models = parsed.Models
	}

	// Build a lookup map from the model list
	lookup := make(map[string]int, len(models))
	for _, m := range models {
		val := m.ContextLength
		if val == 0 {
			val = m.MaxContext
		}
		if val == 0 {
			val = m.MaxInput
		}
		lookup[m.ID] = val
	}

	// Cache the full list
	modelCacheMu.Lock()
	modelCache[cacheK] = lookup
	modelCacheMu.Unlock()

	return lookup[model]
}
