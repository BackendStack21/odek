package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/events"
)

// Regression: eventsRing.snapshot documented "up to limit most-recent
// events ... oldest-first" but iterated oldest-first and stopped at the
// limit — returning the OLDEST matches once the ring held more events
// than the limit. /api/events therefore served stale data exactly when
// the ring was under pressure (the only situation where the limit
// matters). The fix walks newest→oldest, keeps the first `limit`, and
// reverses back to oldest-first.
func TestEventsRingSnapshot_MostRecentWindow(t *testing.T) {
	ring := &eventsRing{}
	for i := 0; i < 10; i++ {
		ring.add(events.Event{Type: "probe", Iteration: i})
	}

	got := ring.snapshot(3, "", "")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// The 3 most recent events are Iterations 7,8,9; oldest-first order.
	if got[0].Iteration != 7 || got[1].Iteration != 8 || got[2].Iteration != 9 {
		t.Fatalf("snapshot(limit=3) of 10 events = iterations %d,%d,%d; want 7,8,9 (most-recent window, oldest-first)",
			got[0].Iteration, got[1].Iteration, got[2].Iteration)
	}
}

// Same contract under a filter: with more matching events than the limit,
// the window must be the most recent matches, still oldest-first.
func TestEventsRingSnapshot_MostRecentWindowFiltered(t *testing.T) {
	ring := &eventsRing{}
	for i := 0; i < 6; i++ {
		runID := "run-old"
		if i >= 4 {
			runID = "run-new" // only events 4 and 5 match
		}
		ring.add(events.Event{Type: "probe", RunID: runID, Iteration: i})
	}

	got := ring.snapshot(1, "run-new", "")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Iteration != 5 {
		t.Fatalf("filtered snapshot(limit=1) = iteration %d; want 5 (most recent match)", got[0].Iteration)
	}
}

// Unbounded snapshot (limit <= 0) keeps returning the whole ring,
// oldest-first.
func TestEventsRingSnapshot_UnboundedStillOldestFirst(t *testing.T) {
	ring := &eventsRing{}
	for i := 0; i < 4; i++ {
		ring.add(events.Event{Type: "probe", Iteration: i})
	}
	got := ring.snapshot(0, "", "")
	if len(got) != 4 || got[0].Iteration != 0 || got[3].Iteration != 3 {
		t.Fatalf("unbounded snapshot = len %d [%d..%d]; want 4 events 0..3 oldest-first",
			len(got), got[0].Iteration, got[3].Iteration)
	}
}
