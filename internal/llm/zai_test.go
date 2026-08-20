package llm

// Tests for Z.ai GLM support: the thinking/effort request mapping and the
// billing-error fast-fail in the retry loop. GLM parameter tests exercise
// buildCallParams directly (it is a pure function); the billing tests run
// the full HTTP path against an httptest server, mirroring retry_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// glmParams builds the call params for a Z.ai client and returns them as a
// generic map for field assertions.
func glmParams(t *testing.T, model, thinking string) map[string]any {
	t.Helper()
	c := &Client{
		BaseURL:  "https://api.z.ai/api/paas/v4",
		APIKey:   "test-key",
		Model:    model,
		Thinking: thinking,
	}
	raw, err := json.Marshal(c.buildCallParams(nil, nil, nil))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	return m
}

func TestGLM_ThinkingLevelsSendObjectAndEffort(t *testing.T) {
	for _, tc := range []struct {
		thinking string
		effort   string
	}{
		{"low", "low"},
		{"high", "high"},
		{"max", "max"},
		// GLM has no "medium" effort level; odek's medium maps to "high".
		{"medium", "high"},
	} {
		m := glmParams(t, "glm-5.3", tc.thinking)
		th, ok := m["thinking"].(map[string]any)
		if !ok || th["type"] != "enabled" {
			t.Errorf("thinking %q: thinking = %v, want {type: enabled}", tc.thinking, m["thinking"])
		}
		if m["reasoning_effort"] != tc.effort {
			t.Errorf("thinking %q: reasoning_effort = %v, want %q", tc.thinking, m["reasoning_effort"], tc.effort)
		}
	}
}

func TestGLM_DisabledOnOlderModels(t *testing.T) {
	m := glmParams(t, "glm-4.6", "disabled")
	th, ok := m["thinking"].(map[string]any)
	if !ok || th["type"] != "disabled" {
		t.Fatalf("thinking = %v, want {type: disabled}", m["thinking"])
	}
	if _, present := m["reasoning_effort"]; present {
		t.Errorf("reasoning_effort must not be sent for plain disabled, got %v", m["reasoning_effort"])
	}
}

func TestGLM_DisabledOnForcedThinkingModelsMapsLow(t *testing.T) {
	// GLM-5.3 rejects thinking.type "disabled" outright; the documented
	// migration is enabled + reasoning_effort "low". The request must never
	// carry "disabled" for these models.
	m := glmParams(t, "glm-5.3", "disabled")
	th, ok := m["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" {
		t.Fatalf("thinking = %v, want {type: enabled} (forced-thinking model)", m["thinking"])
	}
	if m["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want low", m["reasoning_effort"])
	}
}

func TestGLM_EmptyThinkingSendsProviderDefault(t *testing.T) {
	m := glmParams(t, "glm-5.3", "")
	if _, present := m["thinking"]; present {
		t.Errorf("thinking = %v, want omitted (provider default)", m["thinking"])
	}
	if _, present := m["reasoning_effort"]; present {
		t.Errorf("reasoning_effort = %v, want omitted (provider default)", m["reasoning_effort"])
	}
}

func TestGLM_ThinkingObjectCarriesNoBudget(t *testing.T) {
	// The GLM thinking object is {"type": ...} only — a budget_tokens field
	// is Anthropic-specific and could be rejected as an unknown parameter.
	m := glmParams(t, "glm-5.3", "high")
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %v, want an object", m["thinking"])
	}
	if _, present := th["budget_tokens"]; present {
		t.Errorf("thinking object must not carry budget_tokens for GLM: %v", th)
	}
}

func TestIsBillingError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"z.ai insufficient balance", 429, `{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`, true},
		{"openai insufficient quota", 429, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`, true},
		{"openai quota message", 429, `You exceeded your current quota, please check your plan and billing details.`, true},
		{"deepseek balance", 429, `{"error":{"message":"Insufficient Balance"}}`, true},
		{"plain rate limit", 429, `{"error":{"message":"rate limit exceeded, retry later"}}`, false},
		{"billing text on non-429", 500, `Insufficient balance`, false},
	} {
		if got := isBillingError(tc.status, tc.body); got != tc.want {
			t.Errorf("%s: isBillingError(%d, …) = %v, want %v", tc.name, tc.status, got, tc.want)
		}
	}
}

func TestPostChat_Billing429FailsFastWithoutRetry(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`)) //nolint:errcheck
	}))
	defer ts.Close()

	c := New(ts.URL, "k", "glm-5.3", "high", 0, 5*time.Second)
	_, err := c.SimpleCall(context.Background(), "s", "u")
	if err == nil {
		t.Fatal("expected error for billing 429")
	}
	for _, want := range []string{"Insufficient balance", "not retried"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("server hits = %d, want exactly 1 (billing errors must not be retried)", n)
	}
}

func TestPostChat_RateLimit429StillRetries(t *testing.T) {
	// A generic 429 (real rate limiting) must keep the retry behavior.
	orig := retrySleep
	retrySleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { retrySleep = orig })

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded, retry later"}}`)) //nolint:errcheck
	}))
	defer ts.Close()

	c := New(ts.URL, "k", "glm-5.3", "high", 0, 5*time.Second)
	_, err := c.SimpleCall(context.Background(), "s", "u")
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if !strings.Contains(err.Error(), "retry exhausted") {
		t.Errorf("error = %v, want retry exhaustion", err)
	}
	if n := hits.Load(); n < 2 {
		t.Errorf("server hits = %d, want multiple attempts for a transient 429", n)
	}
}
