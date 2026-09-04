package extended

// Tests for the background-call LLM deadline derivation: the deadline must
// follow the memory LLM client's own per-request timeout when it exposes a
// longer one, and fall back to the 30s default otherwise.

import (
	"context"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
)

type hintClient struct {
	timeout time.Duration
}

func (h *hintClient) SimpleCall(ctx context.Context, system, user string) (string, error) {
	return "", nil
}

func (h *hintClient) RequestTimeout() time.Duration { return h.timeout }

func TestLLMDeadline(t *testing.T) {
	if d := llmDeadline(nil); d != defaultLLMTimeout {
		t.Errorf("llmDeadline(nil) = %v, want default %v", d, defaultLLMTimeout)
	}
	// A client exposing a shorter timeout than the default keeps the default.
	short := &hintClient{timeout: 5 * time.Second}
	if d := llmDeadline(short); d != defaultLLMTimeout {
		t.Errorf("llmDeadline(short) = %v, want default %v", d, defaultLLMTimeout)
	}
	// A client exposing a longer timeout propagates it.
	long := &hintClient{timeout: 120 * time.Second}
	if d := llmDeadline(long); d != 120*time.Second {
		t.Errorf("llmDeadline(long) = %v, want 120s", d)
	}
}

// TestNewDerivesLLMDeadlineFromClient verifies the wiring through New: the
// real *llm.Client's per-request timeout (its 120s fallback when
// constructed with 0) becomes the ExtendedMemory background-call deadline.
func TestNewDerivesLLMDeadlineFromClient(t *testing.T) {
	c, err := llmclient.Dial("", "m", "k", "https://api.example.test/v1")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	em := New(t.TempDir(), c, Config{})
	if em.llmTimeout != 120*time.Second {
		t.Errorf("em.llmTimeout = %v, want 120s (client's own fallback timeout)", em.llmTimeout)
	}
}

// TestPredictorDetachesFromRecallBudget pins the fix for the predictor
// inheriting the recall path's ~5s vector-search budget: even with a caller
// context whose deadline is already spent, Predict must issue its LLM call
// under a fresh memory-LLM budget instead of failing instantly.
func TestPredictorDetachesFromRecallBudget(t *testing.T) {
	captured := make(chan time.Duration, 1)
	stub := &deadlineCapturingClient{captured: captured}
	p := NewPredictor(stub, Config{PredictiveIntents: 3})

	// Caller budget already exhausted (deadline in the past).
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()

	go func() { p.Predict(ctx, "hi", nil, UserState{}) }() //nolint:errcheck

	select {
	case remaining := <-captured:
		if remaining < 25*time.Second {
			t.Errorf("predictor LLM call ran with %v left — inherited the spent recall budget, want a fresh ~30s+ budget", remaining)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("predictor never issued its LLM call")
	}
}

type deadlineCapturingClient struct{ captured chan time.Duration }

func (d *deadlineCapturingClient) SimpleCall(ctx context.Context, system, user string) (string, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.captured <- time.Until(dl)
	} else {
		d.captured <- 0
	}
	return "[]", nil
}
