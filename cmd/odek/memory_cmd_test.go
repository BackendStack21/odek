package main

import (
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
)

// TestMemoryCmd_ListAndPromote exercises the human-gated promote path end to
// end through the CLI command: a seeded untrusted episode is pending, the
// command promotes it, and the approval is persisted to the on-disk index.
func TestMemoryCmd_ListAndPromote(t *testing.T) {
	home := setupTestHome(t)
	dir := filepath.Join(home, ".odek", "memory")

	es := memory.NewEpisodeStore(dir, nil)
	if err := es.WriteWithProvenance("20260108-web", "researched a library", 5,
		memory.EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := memoryCmd([]string{"list"}); err != nil {
		t.Fatalf("memory list: %v", err)
	}
	if err := memoryCmd([]string{"promote", "20260108-web"}); err != nil {
		t.Fatalf("memory promote: %v", err)
	}

	fresh := memory.NewEpisodeStore(dir, nil)
	idx, err := fresh.ReadIndex()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx) != 1 || !idx[0].Provenance.UserApproved {
		t.Errorf("episode not approved after promote: %+v", idx)
	}

	if err := memoryCmd([]string{"promote", "does-not-exist"}); err == nil {
		t.Error("promoting an unknown id should error")
	}
	if err := memoryCmd([]string{"bogus"}); err == nil {
		t.Error("unknown subcommand should error")
	}
}

// TestMemoryCmd_ListEmpty: list on a clean home must not error.
func TestMemoryCmd_ListEmpty(t *testing.T) {
	setupTestHome(t)
	if err := memoryCmd([]string{"list"}); err != nil {
		t.Fatalf("memory list on empty home: %v", err)
	}
}
