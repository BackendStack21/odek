//go:build integration

// E2E tests that exercise the full kode agent loop against real LLM APIs.
//
// These tests are gated by the "integration" build tag and require API keys
// to be set via environment variables:
//
//	export KODE_API_KEY_DEEPSEEK="sk-..."   # required for DeepSeek models
//	export KODE_API_KEY_OPENAI="sk-..."     # required for OpenAI models
//	go test -tags=integration -v -count=1 -run 'TestModelE2E' ./cmd/kode/
//
// Each test case creates a real kode.Agent with the given model config,
// runs a simple task ("respond with ALIVE"), and verifies:
//   - The agent loop completes without error
//   - The response contains the expected substring
//   - Token tracking produces non-zero values
//   - The loop runs within a 120s timeout

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/kode"
	"github.com/BackendStack21/kode/internal/render"
)

type modelCase struct {
	Name    string
	Model   string
	BaseURL string
	KeyEnv  string // env var name for the API key
}

// ── DeepSeek models ────────────────────────────────────────────────────

func TestModelE2E_DeepSeek_Chat(t *testing.T)   { testModel(t, modelCase{Name: "deepseek-chat", Model: "deepseek-chat", KeyEnv: "KODE_API_KEY_DEEPSEEK"}) }
func TestModelE2E_DeepSeek_V4Flash(t *testing.T) { testModel(t, modelCase{Name: "deepseek-v4-flash", Model: "deepseek-v4-flash", KeyEnv: "KODE_API_KEY_DEEPSEEK"}) }
func TestModelE2E_DeepSeek_V4Pro(t *testing.T)   { testModel(t, modelCase{Name: "deepseek-v4-pro", Model: "deepseek-v4-pro", KeyEnv: "KODE_API_KEY_DEEPSEEK"}) }

// ── OpenAI models ──────────────────────────────────────────────────────

func TestModelE2E_OpenAI_GPT4oMini(t *testing.T) {
	testModel(t, modelCase{
		Name:    "gpt-4o-mini",
		Model:   "gpt-4o-mini",
		BaseURL: "https://api.openai.com/v1",
		KeyEnv:  "KODE_API_KEY_OPENAI",
	})
}

// testModel runs a single E2E model test: creates an agent, runs a prompt,
// and verifies the response contains "ALIVE".
func testModel(t *testing.T, mc modelCase) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping E2E model test in short mode")
	}

	apiKey := os.Getenv(mc.KeyEnv)
	if apiKey == "" {
		t.Skipf("env %s not set — skipping %s", mc.KeyEnv, mc.Name)
	}

	baseURL := mc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
	}

	agent, err := kode.New(kode.Config{
		Model:         mc.Model,
		BaseURL:       baseURL,
		APIKey:        apiKey,
		MaxIterations: 15,
		SystemMessage: "You are a helpful assistant. Follow instructions precisely.",
		NoProjectFile: true,
		Renderer:      render.New(os.Stderr, false),
	})
	if err != nil {
		t.Fatalf("kode.New: %v", err)
	}
	defer agent.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	answer, err := agent.Run(ctx, "Respond with exactly the word 'ALIVE' and nothing else.")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Agent.Run: %v (after %v)", err, elapsed)
	}

	inputTok := agent.TotalInputTokens()
	outputTok := agent.TotalOutputTokens()

	t.Logf("✅ %-20s  latency=%v  input=%-5d  output=%-5d  total=%-5d",
		mc.Name, elapsed.Round(time.Millisecond), inputTok, outputTok, inputTok+outputTok)

	if !strings.Contains(answer, "ALIVE") {
		t.Errorf("response = %q, want substring %q", strings.TrimSpace(answer), "ALIVE")
	}
	if inputTok <= 0 {
		t.Error("TotalInputTokens = 0, expected > 0")
	}
	if outputTok <= 0 {
		t.Error("TotalOutputTokens = 0, expected > 0")
	}
}
