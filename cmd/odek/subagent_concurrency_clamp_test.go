package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// builtinTools clamps the sub-agent concurrency itself, independent of the
// config loader's clamp: an unclamped resolved value must not lift the limiter
// above the operator ceiling of 8.
func TestBuiltinToolsClampsSubagentConcurrency(t *testing.T) {
	for _, in := range []int{64, 9, 1000000} {
		tools := builtinTools(danger.DangerousConfig{}, nil, nil, in, "", toolConfig{}, nil)
		found := false
		for _, tl := range tools {
			if d, ok := tl.(*delegateTasksTool); ok {
				found = true
				if d.maxConcurrency > 8 {
					t.Errorf("max_concurrency=%d produced a sub-agent limiter of %d, want at most 8", in, d.maxConcurrency)
				}
			}
		}
		if !found {
			t.Fatal("delegate_tasks not registered")
		}
	}
}
