package llm

import "testing"

// Bug-sweep 2026-08-31: provider cache-token normalization.
//
// Anthropic reports cache tokens EXCLUSIVELY (input_tokens excludes them);
// OpenAI (prompt_tokens_details.cached_tokens) and DeepSeek
// (prompt_cache_hit_tokens + prompt_cache_miss_tokens = prompt_tokens)
// report them INCLUSIVELY, as subsets of prompt_tokens.
//
// CallResult.InputTokens must be exclusive ("uncached" input) on every
// provider, with cache volumes carried in CacheReadTokens/CacheCreationTokens,
// so that budget enforcement can sum them without double-counting.

func TestParseResponse_CacheExclusiveNormalization_OpenAI(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "ok"}}],
		"usage": {
			"prompt_tokens": 300,
			"completion_tokens": 30,
			"prompt_tokens_details": {"cached_tokens": 200}
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (300 prompt − 200 cached; exclusive)", result.InputTokens)
	}
	if result.CacheReadTokens != 200 {
		t.Errorf("CacheReadTokens = %d, want 200 (OpenAI cached_tokens)", result.CacheReadTokens)
	}
	if result.CachedTokens != 200 {
		t.Errorf("CachedTokens = %d, want 200 (display field unchanged)", result.CachedTokens)
	}
}

func TestParseResponse_CacheExclusiveNormalization_DeepSeek(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "ok"}}],
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 40,
			"prompt_cache_hit_tokens": 750,
			"prompt_cache_miss_tokens": 250
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0 (prompt 1000 = hit 750 + miss 250 exactly; every token is cache-accounted)", result.InputTokens)
	}
	if result.CacheReadTokens != 750 {
		t.Errorf("CacheReadTokens = %d, want 750", result.CacheReadTokens)
	}
	if result.CacheCreationTokens != 250 {
		t.Errorf("CacheCreationTokens = %d, want 250", result.CacheCreationTokens)
	}
}

func TestParseResponse_CacheExclusiveNormalization_AnthropicUnchanged(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "ok"}}],
		"usage": {
			"prompt_tokens": 500,
			"completion_tokens": 50,
			"cache_creation_input_tokens": 400,
			"cache_read_input_tokens": 100
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	// Anthropic prompt_tokens is already uncached-only: no adjustment.
	if result.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500 (Anthropic is exclusive already)", result.InputTokens)
	}
	if result.CacheCreationTokens != 400 || result.CacheReadTokens != 100 {
		t.Errorf("cache fields = %d/%d, want 400/100", result.CacheCreationTokens, result.CacheReadTokens)
	}
}

func TestParseResponse_CacheExclusiveGuards(t *testing.T) {
	// Hostile/broken payloads must not produce negative InputTokens.
	raw := `{
		"choices": [{"message": {"content": "ok"}}],
		"usage": {
			"prompt_tokens": 50,
			"completion_tokens": 5,
			"prompt_tokens_details": {"cached_tokens": 500}
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens < 0 {
		t.Errorf("InputTokens = %d, must never go negative", result.InputTokens)
	}
}
