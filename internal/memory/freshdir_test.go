package memory

import (
	"path/filepath"
	"testing"
)

// First AddFact into a memory directory that does not exist yet must work
// (fresh install: ~/.odek/memory is created lazily).
func TestRED_AddFactCreatesMissingMemoryDir(t *testing.T) {
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	mgr := NewMemoryManager(filepath.Join(t.TempDir(), "odek", "memory"), nil, cfg)
	if err := mgr.AddFact("user", "prefers tabs"); err != nil {
		t.Fatalf("AddFact into fresh memory dir failed: %v", err)
	}
}
