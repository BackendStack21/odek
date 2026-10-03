package guard

import (
	"testing"
	"time"
)

// The guard is a warning-only sidecar; its default timeout must stay low
// so a hung sidecar cannot add real latency to every tool result.
func TestDefaultTimeoutIsOneSecond(t *testing.T) {
	if got := timeout(nil); got != time.Second {
		t.Fatalf("timeout(nil) = %v, want 1s", got)
	}
	if got := timeout(&Config{TimeoutSeconds: 0}); got != time.Second {
		t.Fatalf("timeout(zero cfg) = %v, want 1s", got)
	}
	if got := DefaultConfig().TimeoutSeconds; got != 1 {
		t.Fatalf("DefaultConfig().TimeoutSeconds = %d, want 1", got)
	}
	// Operator override still honored.
	if got := timeout(&Config{TimeoutSeconds: 7}); got != 7*time.Second {
		t.Fatalf("timeout(7) = %v, want 7s", got)
	}
}
