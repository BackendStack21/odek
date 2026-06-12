package main

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestCtxTool_DefaultsToBackground(t *testing.T) {
	var c ctxTool
	if c.toolCtx() != context.Background() {
		t.Error("unset ctxTool should return context.Background()")
	}
}

func TestCtxTool_SetAndGet(t *testing.T) {
	var c ctxTool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.SetContext(ctx)
	if c.toolCtx() != ctx {
		t.Error("toolCtx should return the context set via SetContext")
	}
}

// TestCtxTool_ConcurrentSetContext mirrors the loop calling SetContext on a
// shared tool instance from parallel goroutines — it must be race-free.
func TestCtxTool_ConcurrentSetContext(t *testing.T) {
	var c ctxTool
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.SetContext(ctx)
			_ = c.toolCtx()
		}()
	}
	wg.Wait()
}

// TestHTTPBatch_ContextCancelled verifies the propagated context aborts the
// fetch: a cancelled context yields an error entry instead of a real request.
func TestHTTPBatch_ContextCancelled(t *testing.T) {
	tool := newHTTPBatchTool(allowAllDanger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	tool.SetContext(ctx)

	result := callJSON(t, tool, `{"requests":[{"url":"http://example.com/"}]}`)
	var r struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(r.Results))
	}
	if r.Results[0].Error == "" {
		t.Fatal("expected an error from the cancelled context")
	}
	if !strings.Contains(r.Results[0].Error, "context canceled") {
		t.Errorf("error should reflect cancellation, got: %s", r.Results[0].Error)
	}
}
