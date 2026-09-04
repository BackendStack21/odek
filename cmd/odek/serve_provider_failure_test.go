package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
)

func TestProviderFailureSummary_RateLimitUsesStatus(t *testing.T) {
	err := &llmclient.RateLimitError{
		APIError: llmclient.APIError{Status: 429, Provider: "test"},
		Attempts: 3,
	}
	got := providerFailureSummary(err)
	if !strings.Contains(got, "HTTP 429") || !strings.Contains(got, "3") {
		t.Fatalf("summary = %q, want HTTP 429 and attempt count", got)
	}
}

func TestProviderFailureSummary_CanceledAndTimeout(t *testing.T) {
	if got := providerFailureSummary(context.Canceled); got != "cancelled" {
		t.Errorf("canceled = %q", got)
	}
	if got := providerFailureSummary(context.DeadlineExceeded); got != "timed out" {
		t.Errorf("deadline = %q", got)
	}
}

func TestProviderFailureSummary_TruncatesAndStripsNewlines(t *testing.T) {
	got := providerFailureSummary(errors.New("line1\nSECRET_BODY"))
	if strings.Contains(got, "SECRET_BODY") || strings.Contains(got, "\n") {
		t.Fatalf("must not leak body after newline: %q", got)
	}
}
