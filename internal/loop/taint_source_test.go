package loop

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// Ingests recorded for engine-derived context (the engine re-wrapping its own
// plan, digest or memory block) do not taint the run; external ones do.
func TestRED_EngineDerivedIngestDoesNotTaint(t *testing.T) {
	ctx := withRunIngestTaint(context.Background(), []session.Message{{Role: "user", Content: "hi"}})
	rec := IngestRecorderFrom(ctx)
	for _, src := range []string{"plan", "compaction", "memory", "persisted_system", "progress_summary",
		"completed_effects", "plan_remaining", "skill", "extended_memory", "return_after_break"} {
		rec(src, "derived")
		if UntrustedIngested(ctx) {
			t.Fatalf("engine-derived ingest %q tainted the run", src)
		}
	}
	rec("episode", "recalled summary")
	if !UntrustedIngested(ctx) {
		t.Fatal("episode recall did not taint the run")
	}
	ctx = withRunIngestTaint(context.Background(), nil)
	IngestRecorderFrom(ctx)("tool:shell", "output")
	if !UntrustedIngested(ctx) {
		t.Fatal("tool ingest did not taint the run")
	}
}

// Project instructions are repository content: when they are wrapped into the
// runtime prompt the run is tainted from its first iteration. An autoloaded
// (reviewed) skill in the prompt is engine-derived and does not taint.
func TestBindRunTaintScansRuntimePrompt(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{{"project:AGENTS.md", true}, {"skill", false}} {
		sys := "identity\n\n" + defaultUntrustedWrap(tc.source, "instructions")
		e := New(nil, tool.NewRegistry(nil), 1, sys, nil, 0)
		ctx := withRunIngestTaint(context.Background(), nil)
		e.bindRunTaint(ctx)
		if got := UntrustedIngested(ctx); got != tc.want {
			t.Fatalf("%s in runtime prompt: tainted=%v, want %v", tc.source, got, tc.want)
		}
	}
}
