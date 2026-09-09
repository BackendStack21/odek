package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
)

func TestUsageFrame_OmitsWhenNothingMeasured(t *testing.T) {
	if _, ok := usageFrame(loop.IterationInfo{}); ok {
		t.Fatal("empty iteration must not emit a usage frame")
	}
}

func TestUsageFrame_IncludesWindow(t *testing.T) {
	frame, ok := usageFrame(loop.IterationInfo{
		WindowTokens:     38412,
		MaxContextTokens: 200000,
		OutputTokens:     512,
		CallDurationMs:   8100,
		CallInputTokens:  18432,
		CallOutputTokens: 78,
		TokensPerSecond:  9.6,
	})
	if !ok {
		t.Fatal("expected usage frame")
	}
	if frame["type"] != "usage" {
		t.Errorf("type = %v, want usage", frame["type"])
	}
	if frame["windowTokens"] != 38412 {
		t.Errorf("windowTokens = %v, want 38412", frame["windowTokens"])
	}
	if frame["maxContextTokens"] != 200000 {
		t.Errorf("maxContextTokens = %v", frame["maxContextTokens"])
	}
	if frame["outputTokens"] != 512 {
		t.Errorf("outputTokens = %v, want 512 (run-cumulative)", frame["outputTokens"])
	}
	if frame["callDurationMs"] != int64(8100) || frame["callOutputTokens"] != 78 || frame["tokensPerSecond"] != 9.6 {
		t.Errorf("this-call fields missing: %v", frame)
	}
}

func TestUsageFrame_OmitsWindowWhenProviderSilent(t *testing.T) {
	frame, ok := usageFrame(loop.IterationInfo{
		CallDurationMs:   80,
		CallOutputTokens: 16,
		TokensPerSecond:  200,
		OutputTokens:     16,
	})
	if !ok {
		t.Fatal("timed think step with no prompt size must still emit usage")
	}
	if _, present := frame["windowTokens"]; present {
		t.Errorf("windowTokens must be omitted (not zeroed) so the gauge holds: %v", frame)
	}
	if _, present := frame["maxContextTokens"]; present {
		t.Errorf("maxContextTokens must be omitted when unknown: %v", frame)
	}
	if frame["callDurationMs"] != int64(80) || frame["tokensPerSecond"] != 200.0 {
		t.Errorf("speed fields missing: %v", frame)
	}
}

func TestUsageFrame_OutputTokensOnly(t *testing.T) {
	frame, ok := usageFrame(loop.IterationInfo{CallOutputTokens: 4, OutputTokens: 10})
	if !ok {
		t.Fatal("call output tokens must emit usage even without duration")
	}
	if frame["callOutputTokens"] != 4 {
		t.Errorf("callOutputTokens = %v, want 4", frame["callOutputTokens"])
	}
	if _, present := frame["windowTokens"]; present {
		t.Errorf("windowTokens present: %v", frame)
	}
}
