package extended

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowPredictLLM answers the predictor prompt only after d, and anything else
// (the rerank) immediately. It ignores ctx cancellation on the slow path on
// purpose: the predictor detaches from the caller's deadline, so the caller
// must stop waiting by itself.
type slowPredictLLM struct {
	d     time.Duration
	mu    sync.Mutex
	calls int
}

func (s *slowPredictLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if strings.Contains(system, "intent-prediction") || strings.Contains(user, "Current user message") {
		time.Sleep(s.d)
		return `[{"text":"follow up","confidence":0.9}]`, nil
	}
	return "none", nil
}

// TestRED_RecallStopsWaitingAtCallerDeadline: turn-start recall is wrapped in
// a short budget, but the intent predictor runs on a DETACHED context with the
// memory-LLM deadline (up to minutes). Recall used to block on the predictor's
// channel, so a slow predictor stalled the whole turn far past the caller's
// deadline. Recall must give up on prediction when its own context expires and
// return what the literal query found.
func TestRED_RecallStopsWaitingAtCallerDeadline(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.PredictiveIntents = 3
	cfg.SemanticSearchRerank = boolPtr(false) // isolate the predictor stall
	llm := &slowPredictLLM{d: 4 * time.Second}
	em := New(t.TempDir(), llm, cfg)
	defer em.Close()
	if err := em.AddAtom(context.Background(), MemoryAtom{
		Text: "the build command is make build", Type: TypeFact,
		SourceClass: SourceUserSaid, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	budget := 400 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	_ = em.FormatExtendedContext(ctx, "how do I build this")
	elapsed := time.Since(start)

	// Generous ceiling: the point is "bounded by the caller", not the exact
	// number. Before the fix this took the predictor's full 4s.
	if elapsed > budget+1500*time.Millisecond {
		t.Fatalf("recall took %v with a %v budget — it waited for the detached predictor", elapsed, budget)
	}
}

// Late predictions must still reach the follow-up sink: the predictor keeps
// running on its detached context, so abandoning the WAIT must not throw away
// the suggestions it was generating.
func TestRED_LatePredictionStillFeedsFollowUpSink(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.PredictiveIntents = 3
	cfg.SemanticSearchRerank = boolPtr(false)
	llm := &slowPredictLLM{d: 300 * time.Millisecond}
	em := New(t.TempDir(), llm, cfg)
	defer em.Close()
	if err := em.AddAtom(context.Background(), MemoryAtom{
		Text: "the build command is make build", Type: TypeFact,
		SourceClass: SourceUserSaid, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = em.FormatExtendedContext(ctx, "how do I build this")

	// The prediction lands after the caller gave up; the sink must still see it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(em.LastFollowUps()) > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("a prediction that arrived after the caller's deadline never reached the follow-up sink")
}
