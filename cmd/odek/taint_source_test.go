package main

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
)

// engineWrap renders an engine-derived block the way the surfaces do: the
// engine hands the label to the installed UntrustedWrapper.
func engineWrap(source, content string) string {
	return wrapEngineContext(source, content)
}

// A session whose only wrapped blocks are engine-derived context (plan,
// digest, memory block, return-after-break summary) never ingested external
// content: it must not be flagged, and trusted delegation stays available.
// One tool ingest flags it, and the flag sticks.
func TestRED_EngineContextSessionStaysTrusted(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := []session.Message{
		{Role: "system", Content: odek.ComposeSecureSystem("You are a test agent.")},
		{Role: "user", Content: "build the feature"},
		{Role: "system", Content: "[Conversation digest] " + engineWrap("compaction", "Task: build. Done: nothing.")},
		{Role: "system", Content: engineWrap("plan", "1. [pending] write code")},
		{Role: "system", Content: engineWrap("memory", "User prefers Go.")},
		{Role: "user", Name: session.ReturnAfterBreakName, Content: engineWrap("return_after_break", "You were building the feature.")},
		{Role: "assistant", Content: "ok"},
	}
	sess, err := store.Create(history, "test-model", "build the feature")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UntrustedIngested {
		t.Fatal("engine-derived context flagged the session as untrusted")
	}
	if got := runDelegateProbe(t, withSessionTaint(context.Background(), loaded), nil, loaded.Messages); got != "trusted" {
		t.Fatalf("session with only engine context spawned a %q child, want trusted", got)
	}

	// A tool ingest taints, and the flag survives the content leaving.
	loaded.Messages = append(loaded.Messages, session.Message{Role: "tool", Content: wrapUntrusted(context.Background(), "/repo/notes.txt", "fetched")})
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if !loaded.UntrustedIngested {
		t.Fatal("tool ingest did not flag the session")
	}
	loaded.Messages = loaded.Messages[:len(loaded.Messages)-1]
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.UntrustedIngested {
		t.Fatal("tool-ingest taint was not sticky")
	}
}

// A tool chooses its own source label (a relative path, a URL, a command), so
// a tool-side wrap must never carry an engine-derived label: a file named
// "plan" read by a tool still taints the history and the run.
func TestRED_ToolCannotMintEngineDerivedSource(t *testing.T) {
	for _, src := range []string{"plan", "compaction", "memory", "persisted_system", "return_after_break", "skill"} {
		var recorded string
		ctx := loop.WithIngestRecorder(context.Background(), func(source, _ string) { recorded = source })
		out := wrapUntrusted(ctx, src, "attacker text")
		if !session.ContentCarriesUntrusted(out) {
			t.Errorf("tool wrap with source %q does not taint the history: %q", src, out)
		}
		if recorded == src {
			t.Errorf("tool ingest recorded under engine-derived label %q", src)
		}
	}
}
