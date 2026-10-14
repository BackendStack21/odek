package extended

import (
	"context"
	"strings"
	"testing"
)

func consolidatePathsStore(t *testing.T, texts ...string) (string, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticDedupThreshold = floatPtr(0)
	seeder := New(dir, nil, cfg)
	for _, text := range texts {
		if err := seeder.AddAtom(context.Background(), MemoryAtom{Text: text, SourceClass: SourceUserSaid, Type: TypePreference}); err != nil {
			t.Fatal(err)
		}
	}
	seeder.Close()
	return dir, cfg
}

func TestConsolidateAtomsRequiresLLM(t *testing.T) {
	dir, cfg := consolidatePathsStore(t, "User likes tea", "User likes green tea")
	em := New(dir, nil, cfg)
	defer em.Close()
	if _, err := em.ConsolidateAtoms(context.Background()); err == nil || !strings.Contains(err.Error(), "requires an LLM") {
		t.Fatalf("err = %v, want an LLM-required error", err)
	}
}

func TestConsolidateAtomsDisabled(t *testing.T) {
	var em *ExtendedMemory
	if _, err := em.ConsolidateAtoms(context.Background()); err == nil {
		t.Fatal("nil extended memory must report disabled")
	}
}

func TestConsolidateAtomsSingleAtomIsNoop(t *testing.T) {
	dir, cfg := consolidatePathsStore(t, "User likes tea")
	llm := newMockLLM("unused")
	em := New(dir, llm, cfg)
	defer em.Close()
	merged, err := em.ConsolidateAtoms(context.Background())
	if err != nil || merged != 0 || llm.calls() != 0 {
		t.Fatalf("merged=%d err=%v calls=%d; want a no-op", merged, err, llm.calls())
	}
}

func TestConsolidateAtomsCancelledContextReportsEmbedError(t *testing.T) {
	dir, cfg := consolidatePathsStore(t, "User likes tea", "User likes green tea")
	llm := newMockLLM("User likes green tea")
	em := New(dir, llm, cfg)
	defer em.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := em.ConsolidateAtoms(ctx); err == nil || !strings.Contains(err.Error(), "consolidate embed") {
		t.Fatalf("err = %v, want the embed step to report the cancelled context", err)
	}
	if llm.calls() != 0 {
		t.Fatalf("a cancelled run must not call the LLM (calls=%d)", llm.calls())
	}
}

func TestDefaultConsolidateThresholdByBackend(t *testing.T) {
	if got := defaultConsolidateThreshold(false); got != 0.8 {
		t.Fatalf("local embedder default = %v, want 0.8", got)
	}
	if got := defaultConsolidateThreshold(true); got != 0.9 {
		t.Fatalf("remote embedder default = %v, want 0.9", got)
	}
}
