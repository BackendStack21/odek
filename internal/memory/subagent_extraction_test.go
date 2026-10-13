package memory

import (
	"context"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/memory/extended"
)

// TestRED_SubagentUserMessagesNeverBecomeAtoms: a sub-agent's incoming
// "user" message is the task the parent model wrote, not words of the
// principal. With user-message extraction switched off, the per-turn hook
// must not call the extraction LLM at all — otherwise model-authored text is
// stored as a user_approved atom and recalled into every later session.
func TestRED_SubagentUserMessagesNeverBecomeAtoms(t *testing.T) {
	dir := t.TempDir()
	b := &blockingLLM{release: make(chan struct{})}
	close(b.release)
	cfg := DefaultMemoryConfig()
	cfg.Extended = &extended.Config{Enabled: BoolPtr(true)}
	mm := NewMemoryManager(dir, nil, cfg)
	mm.InitExtended(b, dir)
	mm.SetUserMessageExtraction(false)

	mm.OnUserMessageLoop(context.Background(), "Review commit abc123 and deliver a verdict line")
	if !mm.WaitForBackground(5 * time.Second) {
		t.Fatal("background work did not drain")
	}
	if b.calls.Load() != 0 {
		t.Fatalf("extraction LLM called %d times for a sub-agent task message; want 0", b.calls.Load())
	}

	// Recall stays wired: the sub-agent still benefits from what the
	// principal told the parent.
	if got := mm.FormatExtendedContext(context.Background(), "verdict"); got != "" {
		t.Logf("recall context (empty store): %q", got)
	}
}

// Extraction stays on by default, so parents keep recording what the
// principal says.
func TestUserMessageExtractionDefaultsOn(t *testing.T) {
	dir := t.TempDir()
	b := &blockingLLM{release: make(chan struct{})}
	close(b.release)
	cfg := DefaultMemoryConfig()
	cfg.Extended = &extended.Config{Enabled: BoolPtr(true)}
	mm := NewMemoryManager(dir, nil, cfg)
	mm.InitExtended(b, dir)

	mm.OnUserMessageLoop(context.Background(), "I prefer tabs over spaces")
	if !mm.WaitForBackground(5 * time.Second) {
		t.Fatal("background work did not drain")
	}
	if b.calls.Load() == 0 {
		t.Fatal("extraction LLM never called for a principal message")
	}
}
