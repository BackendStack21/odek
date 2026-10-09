package guard

import "testing"

// Documented default: fallback_to_local is true when unset.
func TestRED_NewPiguardDefaultFallbackToLocal(t *testing.T) {
	g, err := New(&Config{Provider: ProviderPiguard})
	if err != nil {
		t.Fatalf("New with piguard and unset fallback_to_local returned error %v; documented default is fallback to local", err)
	}
	if g == nil {
		t.Fatal("New returned nil guard")
	}
	if _, ok := g.(*localGuard); !ok {
		t.Fatalf("expected local guard fallback, got %T", g)
	}
}

// An explicit fallback_to_local=false still surfaces the sidecar error.
func TestNewPiguardExplicitNoFallbackReturnsError(t *testing.T) {
	off := false
	g, err := New(&Config{Provider: ProviderPiguard, FallbackToLocal: &off})
	if err == nil {
		t.Fatalf("New with fallback_to_local=false returned guard %T, want error", g)
	}
}
