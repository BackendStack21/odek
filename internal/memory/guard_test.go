package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
)

// mockGuard is a guard that always reports injection for testing.
type mockGuard struct{}

func (m *mockGuard) Detect(ctx context.Context, text string) (guard.Result, error) {
	return guard.Result{Label: "INJECTION", Score: 0.99, Injected: true}, nil
}

func (m *mockGuard) DetectBatch(ctx context.Context, texts []string) ([]guard.Result, error) {
	res := make([]guard.Result, len(texts))
	for i := range res {
		res[i] = guard.Result{Label: "INJECTION", Score: 0.99, Injected: true}
	}
	return res, nil
}

func (m *mockGuard) DetectLong(ctx context.Context, text string) (guard.Result, error) {
	return guard.Result{Label: "INJECTION", Score: 0.99, Injected: true}, nil
}

func (m *mockGuard) Close() error { return nil }

// errGuard is a guard that always errors and does not support fallback.
type errGuard struct{}

func (e *errGuard) Detect(ctx context.Context, text string) (guard.Result, error) {
	return guard.Result{}, errors.New("sidecar unreachable")
}

func (e *errGuard) DetectBatch(ctx context.Context, texts []string) ([]guard.Result, error) {
	return nil, errors.New("sidecar unreachable")
}

func (e *errGuard) DetectLong(ctx context.Context, text string) (guard.Result, error) {
	return guard.Result{}, errors.New("sidecar unreachable")
}

func (e *errGuard) Close() error { return nil }

func TestMemoryManager_GuardRejectsFact(t *testing.T) {
	dir := t.TempDir()
	mm := NewMemoryManager(dir, nil, DefaultMemoryConfig())
	mm.SetGuard(&mockGuard{}, guard.Config{Provider: guard.ProviderPiguard})

	if err := mm.AddFact("user", "remember this fact"); err == nil {
		t.Fatal("expected AddFact to be rejected by mock guard")
	}
}

func TestMemoryManager_GuardFallbackAcceptsFact(t *testing.T) {
	dir := t.TempDir()
	mm := NewMemoryManager(dir, nil, DefaultMemoryConfig())
	fallback := true
	mm.SetGuard(&errGuard{}, guard.Config{Provider: guard.ProviderPiguard, FallbackToLocal: &fallback})

	// The local scan is clean, so the fact is accepted despite the sidecar error.
	if err := mm.AddFact("user", "remember this fact"); err != nil {
		t.Fatalf("expected AddFact to succeed with fallback, got %v", err)
	}
}

func TestMemoryManager_GuardDisabled(t *testing.T) {
	dir := t.TempDir()
	mm := NewMemoryManager(dir, nil, DefaultMemoryConfig())
	scanMemory := false
	mm.SetGuard(&mockGuard{}, guard.Config{
		Provider:        guard.ProviderPiguard,
		Scan:            &guard.ScanConfig{Memory: &scanMemory},
	})

	// Even though the mock guard reports injection, the memory scope is disabled,
	// so only the local scan runs and the benign fact is accepted.
	if err := mm.AddFact("user", "remember this fact"); err != nil {
		t.Fatalf("expected AddFact to succeed when memory scan is disabled, got %v", err)
	}
}
