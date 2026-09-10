package mcpclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCallIntersectsServerAndParentDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name           string
		server, parent time.Duration
	}{
		{"no parent deadline", 25 * time.Millisecond, 0},
		{"longer parent deadline", 25 * time.Millisecond, time.Second},
		{"shorter parent deadline", time.Second, 25 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{timeout: tc.server, pending: make(map[int]chan callResponse), writeCh: make(chan *queuedRequest, 1)}
			ctx := context.Background()
			if tc.parent > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.parent)
				defer cancel()
			}
			start := time.Now()
			_, err := c.call(ctx, "tools/call", nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want deadline exceeded", err)
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("earlier deadline ignored: elapsed %v", elapsed)
			}
		})
	}
}
