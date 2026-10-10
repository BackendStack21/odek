package memory

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/memory/extended"
)

// Without Extended Memory every delegating method is a safe no-op.
func TestExtendedMemoryDisabledMethodsAreNoOps(t *testing.T) {
	m := NewMemoryManager(t.TempDir(), nil, MemoryConfig{Enabled: boolPtr(true)})
	ctx := context.Background()
	m.OnUserMessage(extended.AtomContext{}, "hello")
	m.SetSessionContext("s1", "/proj")
	if m.extSessionID != "s1" || m.extProject != "/proj" {
		t.Fatalf("session context not recorded: %q %q", m.extSessionID, m.extProject)
	}
	if got := m.FormatExtendedContext(ctx, "q"); got != "" {
		t.Fatalf("FormatExtendedContext = %q", got)
	}
	if got := m.FormatReturnAfterBreak(ctx); got != "" {
		t.Fatalf("FormatReturnAfterBreak = %q", got)
	}
	if err := m.ConfirmPendingReview("x"); err == nil {
		t.Fatal("ConfirmPendingReview succeeded without extended memory")
	}
	if err := m.RejectPendingReview("x"); err == nil {
		t.Fatal("RejectPendingReview succeeded without extended memory")
	}
	if got, err := m.ListPendingReview(); got != nil || err != nil {
		t.Fatalf("ListPendingReview = %v, %v", got, err)
	}
	if got := m.FollowUpSuggestions(); got != nil {
		t.Fatalf("FollowUpSuggestions = %v", got)
	}
	tool := &MemoryTool{}
	if tool.Name() != "memory" || tool.Schema() == nil {
		t.Fatal("memory tool name/schema")
	}
}
