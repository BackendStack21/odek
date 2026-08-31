package llm

import (
	"testing"
	"time"
)

// TestSetStreamIdleTimeout pins the setter contract: positive values apply,
// non-positive values are ignored (the built-in default stands).
func TestSetStreamIdleTimeout(t *testing.T) {
	orig := streamIdleTimeout
	t.Cleanup(func() { streamIdleTimeout = orig })

	SetStreamIdleTimeout(5 * time.Second)
	if streamIdleTimeout != 5*time.Second {
		t.Fatalf("streamIdleTimeout = %v, want 5s", streamIdleTimeout)
	}
	if StreamIdleTimeout() != 5*time.Second {
		t.Fatalf("StreamIdleTimeout() = %v, want 5s", StreamIdleTimeout())
	}

	SetStreamIdleTimeout(0)
	SetStreamIdleTimeout(-1 * time.Second)
	if streamIdleTimeout != 5*time.Second {
		t.Fatalf("non-positive override applied; streamIdleTimeout = %v, want 5s", streamIdleTimeout)
	}
}
