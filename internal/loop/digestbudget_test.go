package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// The extractive digest installed by trimContext is not counted against the
// running token total, so after trimming the history can still be far over
// the context budget even though droppable turns remain only as the digest.
func TestRED_TrimContextCountsInstalledDigestAgainstBudget(t *testing.T) {
	const maxContext = 6000
	e := New(nil, tool.NewRegistry(nil), 10, "sys", nil, maxContext)
	e.SetCompaction(true)
	e.ctxLeadDroppableFrom = -1

	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "the task"},
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, session.Message{Role: "assistant", Content: strings.Repeat("x", 990)})
	}
	msgs = append(msgs, session.Message{Role: "user", Content: "latest"})

	out := e.trimContext(context.Background(), msgs, nil)
	got := estimateMessages(out)
	budget := contextBudget(maxContext)
	if got > budget {
		t.Fatalf("after trimContext the history is estimated at %d tokens, over the %d budget (maxContext %d); the installed digest was not accounted for", got, budget, maxContext)
	}
}
