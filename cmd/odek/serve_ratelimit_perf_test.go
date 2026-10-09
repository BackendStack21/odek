package main

import (
	"fmt"
	"testing"
	"time"
)

func TestRED_Serve_RateLimiterSteadyStateNoAllocs(t *testing.T) {
	rl := newRateLimiter(50, time.Minute)
	for i := 0; i < 10; i++ {
		rl.allow("1.2.3.4")
	}
	if a := testing.AllocsPerRun(100, func() { rl.allow("1.2.3.4") }); a > 0 {
		t.Fatalf("allow allocs = %v, want 0", a)
	}
}

func TestRED_Serve_RateLimiterGCAmortised(t *testing.T) {
	rl := newRateLimiter(10, time.Hour)
	const n = 5000
	for i := 0; i < n; i++ {
		if !rl.allow(fmt.Sprintf("k%d", i)) {
			t.Fatal("unexpected limit")
		}
	}
	// Doubling policy: O(log n) sweeps, not one per call past 64 keys.
	if rl.gcRuns > 20 {
		t.Fatalf("gc ran %d times for %d distinct keys", rl.gcRuns, n)
	}
}

func TestRateLimiter_StillLimitsAndExpires(t *testing.T) {
	rl := newRateLimiter(3, 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !rl.allow("a") {
			t.Fatal("should allow")
		}
	}
	if rl.allow("a") {
		t.Fatal("should limit")
	}
	time.Sleep(70 * time.Millisecond)
	if !rl.allow("a") {
		t.Fatal("window should have expired")
	}
}
