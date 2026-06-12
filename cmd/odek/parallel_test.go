package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelMap_OrderAndCorrectness(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	out := parallelMap(items, 3,
		func(i int) int { return i * i },
		func(i int, _ any) int { return -1 })
	for i, v := range out {
		if v != i*i {
			t.Errorf("out[%d] = %d, want %d", i, v, i*i)
		}
	}
}

func TestParallelMap_Empty(t *testing.T) {
	out := parallelMap(nil, 4,
		func(i int) int { return i },
		func(i int, _ any) int { return -1 })
	if len(out) != 0 {
		t.Errorf("expected empty output, got %d", len(out))
	}
}

// TestParallelMap_RecoversWorkerPanic is the core recoverability contract: a
// panic in one worker must NOT unwind (which would crash the process) and must
// not affect the other slots — the panicked slot gets the recovered() value.
func TestParallelMap_RecoversWorkerPanic(t *testing.T) {
	items := []int{0, 1, 2, 3, 4}
	out := parallelMap(items, 3,
		func(i int) string {
			if i == 2 {
				panic("boom")
			}
			return fmt.Sprintf("ok-%d", i)
		},
		func(i int, r any) string {
			return fmt.Sprintf("recovered-%d-%v", i, r)
		})

	want := []string{"ok-0", "ok-1", "recovered-2-boom", "ok-3", "ok-4"}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestParallelMap_MultiplePanicsAllRecovered(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5}
	out := parallelMap(items, 4,
		func(i int) int {
			if i%2 == 0 {
				panic(i)
			}
			return i
		},
		func(i int, _ any) int { return -i })
	for i := range items {
		want := i
		if i%2 == 0 {
			want = -i
		}
		if out[i] != want {
			t.Errorf("out[%d] = %d, want %d", i, out[i], want)
		}
	}
}

// TestParallelMap_NilRecoveredLeavesZero verifies a nil recovered handler is
// tolerated: the panicked slot is left as the zero value, and crucially the
// panic still does not crash the process.
func TestParallelMap_NilRecoveredLeavesZero(t *testing.T) {
	out := parallelMap([]int{0, 1, 2}, 2,
		func(i int) string {
			if i == 1 {
				panic("x")
			}
			return "v"
		}, nil)
	if out[0] != "v" || out[2] != "v" {
		t.Errorf("non-panicking slots wrong: %v", out)
	}
	if out[1] != "" {
		t.Errorf("panicked slot should be zero value, got %q", out[1])
	}
}

// TestParallelMap_ConcurrencyBounded verifies no more than `limit` workers run
// at once, even with far more items than the limit.
func TestParallelMap_ConcurrencyBounded(t *testing.T) {
	const n, limit = 30, 4
	var cur int64
	var mu sync.Mutex
	peak := int64(0)
	release := make(chan struct{})

	work := func(i int) int {
		c := atomic.AddInt64(&cur, 1)
		mu.Lock()
		if c > peak {
			peak = c
		}
		mu.Unlock()
		<-release // hold the worker so concurrency can be observed
		atomic.AddInt64(&cur, -1)
		return i
	}

	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	doneCh := make(chan []int, 1)
	go func() {
		doneCh <- parallelMap(items, limit, work, func(int, any) int { return -1 })
	}()

	// Wait until the limit is saturated.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&cur) < int64(limit) {
		select {
		case <-deadline:
			close(release)
			t.Fatal("workers never reached the concurrency limit")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Give any over-scheduled workers a moment to (incorrectly) start.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak > limit {
		close(release)
		t.Fatalf("peak concurrency %d exceeded limit %d", gotPeak, limit)
	}

	close(release)
	out := <-doneCh
	for i, v := range out {
		if v != i {
			t.Errorf("out[%d] = %d, want %d", i, v, i)
		}
	}
}

func TestParallelMap_LimitClamping(t *testing.T) {
	items := []int{0, 1, 2}
	// limit larger than len and limit < 1 must both still produce correct output.
	for _, limit := range []int{0, -5, 100} {
		out := parallelMap(items, limit,
			func(i int) int { return i + 1 },
			func(i int, _ any) int { return -1 })
		for i := range items {
			if out[i] != i+1 {
				t.Errorf("limit=%d: out[%d] = %d, want %d", limit, i, out[i], i+1)
			}
		}
	}
}

func TestToolConcurrency_InBand(t *testing.T) {
	c := toolConcurrency()
	if c < 4 || c > 8 {
		t.Errorf("toolConcurrency() = %d, want in [4,8]", c)
	}
}
