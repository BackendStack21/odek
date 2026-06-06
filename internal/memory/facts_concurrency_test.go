package memory

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestFacts_ConcurrentNoLostUpdates reproduces verification finding D-03: many
// MemoryManagers sharing one memory directory (as `odek serve` builds one per
// connection) writing facts at the same time used to lose updates via the
// read-modify-write race + a shared temp file. With the per-directory lock and
// unique temp file, every concurrent fact survives. Run with -race.
func TestFacts_ConcurrentNoLostUpdates(t *testing.T) {
	dir := t.TempDir()
	const n = 12
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false) // isolate the file race from RP merging

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine gets its OWN manager/FactStore over the shared dir.
			mm := NewMemoryManager(dir, nil, cfg)
			if err := mm.AddFact("user", fmt.Sprintf("concurrent fact number %d here", i)); err != nil {
				t.Errorf("AddFact %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	user, _, err := NewMemoryManager(dir, nil, cfg).ReadFacts()
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	for i := 0; i < n; i++ {
		if !strings.Contains(user, fmt.Sprintf("number %d here", i)) {
			t.Errorf("lost update: concurrent fact %d missing from user.md", i)
		}
	}
}
