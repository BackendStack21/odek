package main

import (
	"runtime"
	"sync"
)

// toolConcurrency is the worker count the batch tools use for bounded
// parallelism. It scales with the machine (GOMAXPROCS) but stays in a modest
// [4, 8] band: enough to overlap file/network/subprocess I/O on a multi-core
// dev machine, low enough to avoid thrashing disks, sockets, or the process
// table. The batch tools previously hard-coded 4; this never drops below that.
func toolConcurrency() int {
	n := runtime.GOMAXPROCS(0)
	switch {
	case n < 4:
		return 4
	case n > 8:
		return 8
	default:
		return n
	}
}

// parallelMap applies work to every item with at most `limit` workers running
// at once, returning results in input order: out[i] == work(items[i]).
//
// It recovers a panic in any single worker and routes it through
// recovered(item, panicValue) instead of letting it unwind. This is the whole
// point of centralising the pattern: a panic in a goroutine is NOT caught by a
// deferred recover in the function that spawned it — it crashes the entire
// process. Every batch tool fans work out to goroutines, so without this guard
// one malformed input (a nil deref, an out-of-range slice) in a single worker
// takes down the whole agent. Three of the hand-rolled copies this replaces
// were missing per-worker recovery entirely.
//
// A nil `recovered` is tolerated: a panicked slot is left as the zero value.
func parallelMap[I, O any](items []I, limit int, work func(I) O, recovered func(I, any) O) []O {
	out := make([]O, len(items))
	if len(items) == 0 {
		return out
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(items) {
		limit = len(items)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, limit)
	for i := range items {
		sem <- struct{}{} // acquire before spawning — bounds live goroutines too
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil && recovered != nil {
					// The handler runs while unwinding a worker panic; if it
					// panics too, that second panic must not escape either — the
					// guarantee is that NO worker can crash the process. A
					// panicked handler leaves the slot at its zero value.
					defer func() { _ = recover() }()
					out[idx] = recovered(items[idx], r)
				}
			}()
			out[idx] = work(items[idx])
		}(i)
	}
	wg.Wait()
	return out
}
