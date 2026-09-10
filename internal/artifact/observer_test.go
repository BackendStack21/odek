package artifact

import (
	"context"
	"testing"
)

func TestObserverIsolatedAndContextScoped(t *testing.T) {
	roots := []string{"/allowed"}
	seen := 0
	ctx := WithObserver(context.Background(), func(ref Ref, received []string) {
		seen++
		if received[0] != "/allowed" {
			t.Fatal("observer mutation leaked to another delivery")
		}
		received[0] = "/changed"
		if ref.ID == "first" {
			panic("preview unavailable")
		}
	})
	refs := []Ref{{ID: "first"}, {ID: "second"}}
	Observe(context.Background(), refs, roots)
	Observe(ctx, refs, roots)
	if seen != 2 || roots[0] != "/allowed" {
		t.Fatalf("deliveries=%d roots=%v", seen, roots)
	}
}
