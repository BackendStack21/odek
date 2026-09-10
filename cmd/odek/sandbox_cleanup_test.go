package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStandaloneSandboxCleanupRetriesAndIsIdempotent(t *testing.T) {
	var attempts atomic.Int32
	cleanup := newSandboxCleanup("standalone-test", func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("removal has no deadline")
		}
		if attempts.Add(1) < 3 {
			return errors.New("Docker temporarily unavailable")
		}
		return nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := cleanup(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := attempts.Load(); got != 3 {
		t.Fatalf("removal attempts: %d, want 3", got)
	}
}

func TestStandaloneSandboxCleanupReportsExhaustionAndCanRecover(t *testing.T) {
	failure := errors.New("Docker unavailable")
	attempts := 0
	fail := true
	cleanup := newSandboxCleanup("standalone-test", func(context.Context) error {
		attempts++
		if fail {
			return failure
		}
		return nil
	})
	var got error
	diagnostic := captureStderrDuring(t, func() { got = cleanup() })
	if !errors.Is(got, failure) || attempts != 3 {
		t.Fatalf("cleanup: %v, attempts %d", got, attempts)
	}
	if !strings.Contains(diagnostic, "after 3 attempts") || !strings.Contains(diagnostic, "docker rm -f standalone-test") {
		t.Fatalf("missing actionable cleanup failure: %s", diagnostic)
	}
	fail = false
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if attempts != 4 {
		t.Fatalf("failed cleanup could not retry: %d", attempts)
	}
}

func TestSandboxRemovalRetriesTimedOutAttemptWithFreshContext(t *testing.T) {
	attempts := 0
	var previous context.Context
	err := retrySandboxRemoval(func(ctx context.Context) error {
		attempts++
		if previous != nil && previous.Err() == nil {
			t.Error("previous attempt context not cancelled")
		}
		previous = ctx
		if ctx.Err() != nil {
			t.Error("new attempt already cancelled")
		}
		if attempts == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, 10*time.Millisecond)
	if err != nil || attempts != 2 {
		t.Fatalf("timeout recovery: %v, attempts %d", err, attempts)
	}
	if previous.Err() == nil {
		t.Fatal("successful attempt context not released")
	}
}
