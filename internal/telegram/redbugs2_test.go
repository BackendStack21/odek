package telegram

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

// RED #B4 (V5): Session TTL expiry is dead code. GetOrCreate skips the
// fresh-hit branch for an expired cache entry and calls Load — which finds
// the SAME stale pointer still in the cache and returns it unchecked, so
// an in-memory session lives forever regardless of the configured TTL.
func TestRED_SessionTTLExpiresStaleCacheEntry(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(store, 30*time.Millisecond)

	cs1, err := sm.GetOrCreate(424242)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond) // past TTL

	cs2, err := sm.GetOrCreate(424242)
	if err != nil {
		t.Fatal(err)
	}
	if cs1 == cs2 {
		t.Fatal("GetOrCreate returned the same expired *ChatSession; TTL expiry never takes effect")
	}
}
