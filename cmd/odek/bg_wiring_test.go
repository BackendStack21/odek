package main

import (
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

// ── background-commands tool registration (cmd/odek contract) ──────────

// bgToolNames are the five bg_* tools the runtime must register.
var bgToolNames = map[string]bool{
	"bg_start":  true,
	"bg_list":   true,
	"bg_status": true,
	"bg_output": true,
	"bg_stop":   true,
}

// TestBuiltinTools_NoBgRuntime_NoBgTools pins the registration contract:
// without a runtime (background disabled, or a surface that did not build
// one) the tool list stays clean of bg_* entries.
func TestBuiltinTools_NoBgRuntime_NoBgTools(t *testing.T) {
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 4, "", toolConfig{}, nil)
	for _, tl := range tools {
		if bgToolNames[tl.Name()] {
			t.Errorf("tool %q registered without a background runtime — must stay absent", tl.Name())
		}
	}
}

// TestBuiltinTools_WithBgRuntime_ExactlyFiveBgTools pins the registration
// contract: with a runtime, exactly the five bg_* tools are appended and
// nothing else changes.
func TestBuiltinTools_WithBgRuntime_ExactlyFiveBgTools(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, "sess-test", "", nil)
	if rt == nil {
		t.Fatal("newBackgroundRuntime(Enabled=true, session) = nil, want runtime")
	}
	without := builtinTools(danger.DangerousConfig{}, nil, nil, 4, "", toolConfig{}, nil)
	with := builtinTools(danger.DangerousConfig{}, nil, nil, 4, "", toolConfig{}, nil, rt)

	if got := countBgTools(with); got != len(bgToolNames) {
		t.Fatalf("bg tool count = %d, want %d", got, len(bgToolNames))
	}
	if len(with) != len(without)+len(bgToolNames) {
		t.Fatalf("tool count with runtime = %d, without = %d — runtime must add exactly %d",
			len(with), len(without), len(bgToolNames))
	}
	// The non-bg tool sequence must be untouched (prefix preserved).
	for i, tl := range without {
		if with[i].Name() != tl.Name() {
			t.Fatalf("tool[%d] = %q, want %q — bg tools must append, not interleave", i, with[i].Name(), tl.Name())
		}
	}
}

// TestNewBackgroundRuntime_DisabledReturnsNil pins the gate: a disabled
// section (or an empty session id) yields no runtime, so surfaces simply
// skip tool registration.
func TestNewBackgroundRuntime_DisabledReturnsNil(t *testing.T) {
	if rt := newBackgroundRuntime(BackgroundSettings{Enabled: false, MaxJobs: 8}, "sess-test", "", nil); rt != nil {
		t.Fatal("newBackgroundRuntime(Enabled=false) = non-nil, want nil")
	}
	if rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, "", "", nil); rt != nil {
		t.Fatal("newBackgroundRuntime(empty session) = non-nil, want nil")
	}
}

func countBgTools(tools []odek.Tool) int {
	n := 0
	for _, tl := range tools {
		if bgToolNames[tl.Name()] {
			n++
		}
	}
	return n
}
