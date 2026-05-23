package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverModelContext_OpenRouterFormat(t *testing.T) {
	ResetModelCache()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("expected /models, got %s", r.URL.Path)
		}
		resp := modelsResponse{
			Data: []rawModel{
				{ID: "deepseek-v4-flash", ContextLength: 131072},
				{ID: "deepseek-v4-pro", ContextLength: 1048576},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ctx := DiscoverModelContext(srv.URL, "test-key", "deepseek-v4-flash")
	if ctx != 131072 {
		t.Errorf("expected 131072, got %d", ctx)
	}

	// Second model
	ctx = DiscoverModelContext(srv.URL, "test-key", "deepseek-v4-pro")
	if ctx != 1048576 {
		t.Errorf("expected 1048576, got %d", ctx)
	}
}

func TestDiscoverModelContext_UnknownModel(t *testing.T) {
	ResetModelCache()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := modelsResponse{
			Data: []rawModel{
				{ID: "gpt-4o", ContextLength: 128000},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ctx := DiscoverModelContext(srv.URL, "test-key", "unknown-model")
	if ctx != 0 {
		t.Errorf("expected 0 for unknown model, got %d", ctx)
	}
}

func TestDiscoverModelContext_ServerError(t *testing.T) {
	ResetModelCache()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx := DiscoverModelContext(srv.URL, "test-key", "any-model")
	if ctx != 0 {
		t.Errorf("expected 0 on server error, got %d", ctx)
	}
}

func TestDiscoverModelContext_Timeout(t *testing.T) {
	ResetModelCache()

	// A port that's very unlikely to be listening — tests connection refused handling
	ctx := DiscoverModelContext("http://127.0.0.1:1", "test-key", "any-model")
	if ctx != 0 {
		t.Errorf("expected 0 on connection error, got %d", ctx)
	}
}

func TestDiscoverModelContext_Cached(t *testing.T) {
	ResetModelCache()

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := modelsResponse{
			Data: []rawModel{
				{ID: "flash", ContextLength: 131072},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// First call hits the server
	ctx1 := DiscoverModelContext(srv.URL, "test-key", "flash")
	if ctx1 != 131072 {
		t.Errorf("expected 131072, got %d", ctx1)
	}

	// Second call should be cached
	ctx2 := DiscoverModelContext(srv.URL, "test-key", "flash")
	if ctx2 != 131072 {
		t.Errorf("expected 131072, got %d", ctx2)
	}

	if callCount != 1 {
		t.Errorf("expected 1 server call (cached), got %d", callCount)
	}
}

func TestDiscoverModelContext_MaxContextFallback(t *testing.T) {
	ResetModelCache()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := modelsResponse{
			Data: []rawModel{
				{ID: "my-model", MaxContext: 64000},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ctx := DiscoverModelContext(srv.URL, "test-key", "my-model")
	if ctx != 64000 {
		t.Errorf("expected 64000 from max_context fallback, got %d", ctx)
	}
}

func TestDiscoverModelContext_ModelsArrayFallback(t *testing.T) {
	ResetModelCache()

	// Some providers use "models" instead of "data"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string][]rawModel{
			"models": {
				{ID: "claude-sonnet-4", ContextLength: 200000},
			},
		})
	}))
	defer srv.Close()

	ctx := DiscoverModelContext(srv.URL, "test-key", "claude-sonnet-4")
	if ctx != 200000 {
		t.Errorf("expected 200000, got %d", ctx)
	}
}
