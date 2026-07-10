// Package extended implements the Extended Memory subsystem for odek.
//
// Extended Memory stores atomic memory units ("atoms") extracted from user
// messages and recalled via semantic search. It is opt-in and invisible when
// disabled.
package extended

import (
	"fmt"
	"os"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/llm"
)

// Config controls the Extended Memory subsystem.
type Config struct {
	Enabled                 *bool             `json:"enabled,omitempty"`
	MaxSizeMB               int               `json:"max_size_mb,omitempty"`
	SemanticSearchTopK      int               `json:"semantic_search_top_k,omitempty"`
	SemanticSearchOverfetch int               `json:"semantic_search_overfetch,omitempty"`
	SemanticSearchMinScore  float32           `json:"semantic_search_min_score,omitempty"`
	SemanticSearchRerank    *bool             `json:"semantic_search_rerank,omitempty"`
	AtomMaxChars            int               `json:"atom_max_chars,omitempty"`
	MemoryBudgetChars       int               `json:"memory_budget_chars,omitempty"`
	DecayHalfLifeDays       int               `json:"decay_half_life_days,omitempty"`
	QuarantineTTLDays       int               `json:"quarantine_ttl_days,omitempty"`
	EvictionPolicy          string            `json:"eviction_policy,omitempty"`
	PredictiveIntents       int               `json:"predictive_intents,omitempty"`
	AutoExtractPerTurn      *bool             `json:"auto_extract_per_turn,omitempty"`
	InferUserState          *bool             `json:"infer_user_state,omitempty"`
	LLM                     *LLMConfig        `json:"llm,omitempty"`
	Embedding               *embedding.Config `json:"embedding,omitempty"`
}

// LLMConfig selects a dedicated LLM for Extended Memory extraction and
// reranking. When nil, the wiring layer reuses the main agent llm.Client.
type LLMConfig struct {
	BaseURL        string  `json:"base_url,omitempty"`
	APIKey         string  `json:"api_key,omitempty"`
	Model          string  `json:"model,omitempty"`
	Thinking       string  `json:"thinking,omitempty"`
	MaxTokens      int     `json:"max_tokens,omitempty"`
	Temperature    float64 `json:"temperature,omitempty"`
	TimeoutSeconds int     `json:"timeout_seconds,omitempty"`
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// DefaultConfig returns the default Extended Memory configuration.
// Extended Memory is opt-in: Enabled defaults to false.
func DefaultConfig() Config {
	return Config{
		Enabled:                 boolPtr(false),
		MaxSizeMB:               100,
		SemanticSearchTopK:      10,
		SemanticSearchOverfetch: 4,
		SemanticSearchMinScore:  0.55,
		SemanticSearchRerank:    boolPtr(true),
		AtomMaxChars:            300,
		MemoryBudgetChars:       2000,
		DecayHalfLifeDays:       30,
		QuarantineTTLDays:       7,
		EvictionPolicy:          "retention_decay",
		PredictiveIntents:       3,
		AutoExtractPerTurn:      boolPtr(true),
		InferUserState:          boolPtr(true),
	}
}

// Resolve merges cfg over DefaultConfig, producing a fully populated Config.
func Resolve(cfg Config) Config {
	def := DefaultConfig()
	if cfg.Enabled != nil {
		def.Enabled = cfg.Enabled
	}
	if cfg.MaxSizeMB > 0 {
		def.MaxSizeMB = cfg.MaxSizeMB
	}
	if cfg.SemanticSearchTopK > 0 {
		def.SemanticSearchTopK = cfg.SemanticSearchTopK
	}
	if cfg.SemanticSearchOverfetch > 0 {
		def.SemanticSearchOverfetch = cfg.SemanticSearchOverfetch
	}
	if cfg.SemanticSearchMinScore > 0 {
		def.SemanticSearchMinScore = cfg.SemanticSearchMinScore
	}
	if cfg.SemanticSearchRerank != nil {
		def.SemanticSearchRerank = cfg.SemanticSearchRerank
	}
	if cfg.AtomMaxChars > 0 {
		def.AtomMaxChars = cfg.AtomMaxChars
	}
	if cfg.MemoryBudgetChars > 0 {
		def.MemoryBudgetChars = cfg.MemoryBudgetChars
	}
	if cfg.DecayHalfLifeDays > 0 {
		def.DecayHalfLifeDays = cfg.DecayHalfLifeDays
	}
	if cfg.QuarantineTTLDays > 0 {
		def.QuarantineTTLDays = cfg.QuarantineTTLDays
	}
	if cfg.EvictionPolicy != "" {
		def.EvictionPolicy = cfg.EvictionPolicy
	}
	if cfg.PredictiveIntents > 0 {
		def.PredictiveIntents = cfg.PredictiveIntents
	}
	if cfg.AutoExtractPerTurn != nil {
		def.AutoExtractPerTurn = cfg.AutoExtractPerTurn
	}
	if cfg.InferUserState != nil {
		def.InferUserState = cfg.InferUserState
	}
	if cfg.LLM != nil {
		def.LLM = cfg.LLM
	}
	if cfg.Embedding != nil {
		def.Embedding = cfg.Embedding
	}
	return def
}

// ResolveLLM returns the LLM client to use for Extended Memory. If cfg.LLM is
// set, it builds a dedicated client; otherwise it returns the provided main
// client unchanged. When falling back to the main client and the main model
// has thinking enabled, a warning is logged because reasoning tokens are
// wasted on memory-only calls.
func ResolveLLM(cfg Config, mainLLM LLMClient, thinking string) LLMClient {
	if cfg.LLM == nil {
		if thinking != "" {
			fmt.Fprintf(os.Stderr, "odek: warning: extended memory is reusing the main LLM which has thinking enabled (%q); consider setting memory.extended.llm for cheaper extraction/recall\n", thinking)
		}
		return mainLLM
	}
	lmc := cfg.LLM
	if lmc.BaseURL == "" || lmc.Model == "" {
		fmt.Fprintf(os.Stderr, "odek: warning: extended memory llm requires base_url and model; falling back to main LLM\n")
		return mainLLM
	}
	timeout := time.Duration(lmc.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := llm.NewWithMaxTokens(
		lmc.BaseURL, lmc.APIKey, lmc.Model,
		lmc.Thinking, 0, lmc.MaxTokens, timeout,
	)
	if lmc.Temperature >= 0 {
		client.Temperature = lmc.Temperature
	}
	return client
}
