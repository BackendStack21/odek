package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
)

type visionOwner struct {
	grant   budget.Grant
	err     error
	settled bool
	usage   *budget.Usage
}

func (o *visionOwner) BudgetSnapshot() budget.Snapshot               { return budget.Snapshot{} }
func (o *visionOwner) ReserveInferenceBudget() (budget.Grant, error) { return o.grant, o.err }
func (o *visionOwner) SettleExternalBudget(_ budget.Grant, u *budget.Usage) {
	o.settled = true
	o.usage = u
}

func TestProviderVisionRoutingUsageAndRequest(t *testing.T) {
	owner := &visionOwner{grant: budget.Grant{ID: 1, Limits: budget.Limits{MaxOutputTokens: 20, MaxRuntimeSeconds: 5, MaxCostUSD: 1}}}
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vision-key" {
			t.Error("wrong provider credentials")
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"A red square"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
	}))
	defer server.Close()
	a := &providerVisionAnalyzer{provider: "vision-provider", maxTokens: 1024, options: llmclient.Options{Provider: "main", APIKey: "main-key", BaseURL: "http://127.0.0.1:1", Providers: map[string]llmclient.ProviderOverride{"vision-provider": {APIKey: "vision-key", BaseURL: server.URL, Format: "openai"}}}, limits: budget.Limits{MaxCostUSD: 1, ModelPrices: map[string]budget.ModelPrice{"visual": {InputCostPerMillionUSD: 2, OutputCostPerMillionUSD: 4}}}}
	a.SetBudgetView(owner)
	var emitted events.Event
	a.SetEventEmitter(func(e events.Event) { emitted = e })
	result, err := a.AnalyzeVision(context.Background(), "visual", "What color?", []visionMedia{{MIME: "image/png", Data: []byte("pixels")}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "A red square" || result.Model != "visual" {
		t.Fatalf("result: %+v", result)
	}
	if !owner.settled || owner.usage == nil || owner.usage.InputTokens != 100 || owner.usage.OutputTokens != 10 || owner.usage.ToolCalls != 0 || owner.usage.CostUSD != 0.00024 {
		t.Fatalf("settlement: %+v", owner)
	}
	if request["model"] != "visual" || request["tools"] != nil {
		t.Fatalf("request options: %+v", request)
	}
	raw, _ := json.Marshal(request)
	if !strings.Contains(string(raw), "data:image/png;base64,cGl4ZWxz") || (!strings.Contains(string(raw), `"max_tokens":20`) && !strings.Contains(string(raw), `"max_completion_tokens":20`)) {
		t.Fatalf("missing image or cap: %s", raw)
	}
	eventJSON, _ := json.Marshal(emitted)
	if emitted.Type != events.TypeSideCallUsage || strings.Contains(string(eventJSON), "pixels") || strings.Contains(string(eventJSON), "What color") {
		t.Fatalf("event: %s", eventJSON)
	}
}

func TestProviderVisionFailureAndMissingUsage(t *testing.T) {
	for _, tc := range []struct {
		name, body       string
		status           int
		wantErr, unknown bool
	}{
		{"no_usage", `{"choices":[{"message":{"content":"description"},"finish_reason":"stop"}]}`, 200, false, true},
		{"empty", `{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`, 200, true, false},
		{"provider_error", `{"error":{"message":"SECRET_IMAGE_ECHO","type":"invalid_request_error"}}`, 400, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			owner := &visionOwner{grant: budget.Grant{ID: 1}}
			a := &providerVisionAnalyzer{provider: "v", view: owner, options: llmclient.Options{Providers: map[string]llmclient.ProviderOverride{"v": {Format: "openai", BaseURL: srv.URL, APIKey: "x"}}}}
			_, err := a.AnalyzeVision(context.Background(), "visual", "describe", []visionMedia{{MIME: "image/png", Data: []byte("pixels")}})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET_IMAGE_ECHO") {
				t.Fatal("provider content leaked")
			}
			if !owner.settled || (owner.usage == nil) != tc.unknown {
				t.Fatalf("usage: %+v", owner)
			}
		})
	}
}

func TestProviderVisionPreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &providerVisionAnalyzer{}
	if _, err := a.AnalyzeVision(ctx, "m", "p", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a.provider = "missing"
	if _, err := a.AnalyzeVision(context.Background(), "m", "p", nil); err == nil {
		t.Fatal("unknown provider accepted")
	}
	a.provider = "openai"
	// Credentials are required at client construction; supply an inert
	// override so the preflight path is exercised without env keys.
	a.options = llmclient.Options{Providers: map[string]llmclient.ProviderOverride{"openai": {Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "test"}}}
	a.limits.MaxCostUSD = 1
	if _, err := a.AnalyzeVision(context.Background(), "m", "p", nil); err == nil {
		t.Fatal("unpriced cost budget accepted")
	}
	a.limits = budget.Limits{}
	owner := &visionOwner{err: &budget.Error{Limit: budget.LimitInputTokens}}
	a.view = owner
	if _, err := a.AnalyzeVision(context.Background(), "m", "p", nil); err == nil || owner.settled {
		t.Fatalf("reserve failure: %v", err)
	}
	owner.err = nil
	owner.grant = budget.Grant{ID: 1}
	if _, err := a.AnalyzeVision(context.Background(), "m", "p", nil); err == nil || !owner.settled || owner.usage == nil || owner.usage.InputTokens != 0 {
		t.Fatalf("empty preflight: %v %+v", err, owner)
	}
}

func TestProviderVisionCancellation(t *testing.T) {
	// The handler must not wait on r.Context().Done(): a client-side cancel
	// does not reliably unblock an httptest handler that never read its
	// body, which deadlocks srv.Close(). Signal on a test-owned channel.
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer func() { close(release); srv.Close() }()
	a := &providerVisionAnalyzer{provider: "v", options: llmclient.Options{Timeout: time.Second, Providers: map[string]llmclient.ProviderOverride{"v": {Format: "openai", BaseURL: srv.URL, APIKey: "x"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-entered; cancel() }()
	_, err := a.AnalyzeVision(ctx, "visual", "describe", []visionMedia{{MIME: "image/png", Data: []byte("pixels")}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestVisionWiringAndPhotoArguments(t *testing.T) {
	cfg := config.ResolvedConfig{Provider: "main", APIKey: "main-key", Vision: config.VisionConfig{Backend: "provider", Provider: "v", Model: "visual", MaxTokens: 123}, LLM: config.LLMConfig{RequestTimeoutSeconds: 42}, Providers: map[string]config.FileProviderOverride{"v": {APIKey: "vision-key", BaseURL: "http://localhost:8888", Format: "openai"}}}
	tc := toolConfigFromResolved(cfg)
	tool := newConfiguredVisionTool(cfg.Dangerous, tc, nil)
	a, ok := tool.analyzer.(*providerVisionAnalyzer)
	if !ok || a.provider != "v" || a.options.Timeout != 42*time.Second || a.options.Providers["v"].APIKey != "vision-key" || a.maxTokens != 123 {
		t.Fatalf("bad wiring: %+v", a)
	}
	calls := telegramPhotoCalls(&telegramPhotoInput{Path: "/tmp/photo.png", Caption: "quoted \" caption"})
	var args visionArgs
	if len(calls) != 1 || calls[0].Function.Name != "vision" || json.Unmarshal([]byte(calls[0].Function.Arguments), &args) != nil || args.Path != "/tmp/photo.png" || !strings.Contains(args.Prompt, "caption") {
		t.Fatalf("calls: %+v", calls)
	}
}
