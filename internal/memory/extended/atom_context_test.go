package extended

import (
	"context"
	"testing"
	"time"
)

type gateLLM struct {
	started chan struct{}
	release chan struct{}
	resp    string
}

func (g *gateLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	close(g.started)
	<-g.release
	return g.resp, nil
}

// An atom extracted for an explicit AtomContext{SessionID: "A"} must keep
// session A even when the shared session context moves to session B while the
// (up to 30s) extraction LLM call is in flight.
func TestRED_ExtractedAtomKeepsOriginatingSession(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	llm := &gateLLM{started: make(chan struct{}), release: make(chan struct{}),
		resp: extractJSONResponse("User likes Python")}
	em := New(t.TempDir(), llm, cfg)

	done := make(chan struct{})
	go func() {
		em.OnUserMessage(AtomContext{SessionID: "sessionA", Turn: 1}, "I like Python")
		close(done)
	}()
	<-llm.started
	em.SetSessionContext("sessionB", "/other")
	close(llm.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	atoms, _ := em.List()
	if len(atoms) != 1 {
		t.Fatalf("want 1 atom, got %d", len(atoms))
	}
	if atoms[0].Context.SessionID != "sessionA" {
		t.Fatalf("atom session = %q, want sessionA (caller-supplied AtomContext overwritten)", atoms[0].Context.SessionID)
	}
}
