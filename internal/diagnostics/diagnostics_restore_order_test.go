package diagnostics

import (
	"testing"

	"github.com/BackendStack21/odek/internal/events"
)

// Restoring observers out of installation order must not revive a removed observer.
func TestRED_RestoreOutOfOrderLeavesNoStaleObserver(t *testing.T) {
	stale := 0
	h1 := Install(func(events.Event) { stale++ })
	h2 := Install(func(events.Event) {})
	h1()
	h2()
	Report("test", "after_restore", "", nil)
	Warning("test", "after_restore", nil)
	if stale != 0 {
		t.Fatalf("first observer was removed but still received events")
	}
}

// Removing the active observer falls back past removed entries to the nearest
// observer that is still installed.
func TestRestoreSkipsRemovedObserversToLiveOne(t *testing.T) {
	var base, middle, top int
	removeBase := Install(func(events.Event) { base++ })
	removeMiddle := Install(func(events.Event) { middle++ })
	removeTop := Install(func(events.Event) { top++ })
	removeMiddle()
	removeTop()
	Warning("test", "fallback", nil)
	if base != 1 || middle != 0 || top != 0 {
		t.Fatalf("delivery base=%d middle=%d top=%d, want 1 0 0", base, middle, top)
	}
	removeBase()
	Warning("test", "none_left", nil)
	if base != 1 {
		t.Fatalf("removed base observer received events: %d", base)
	}
}

// Removing the same observer twice is a no-op.
func TestRemoveTwiceIsNoop(t *testing.T) {
	var live int
	removeLive := Install(func(events.Event) { live++ })
	t.Cleanup(removeLive)
	removeOther := Install(func(events.Event) {})
	removeOther()
	removeOther()
	Warning("test", "twice", nil)
	if live != 1 {
		t.Fatalf("live observer received %d events, want 1", live)
	}
}
