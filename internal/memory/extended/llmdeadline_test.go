package extended

// Tests for the background-call LLM deadline derivation: the deadline must
// follow the memory LLM client's own per-request timeout when it exposes a
// longer one, and fall back to the 30s default otherwise.

import (
	"context"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
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
	c := llm.New("https://api.example.test/v1", "k", "m", "", 0, 0)
	em := New(t.TempDir(), c, Config{})
	if em.llmTimeout != 120*time.Second {
		t.Errorf("em.llmTimeout = %v, want 120s (client's own fallback timeout)", em.llmTimeout)
	}
}
