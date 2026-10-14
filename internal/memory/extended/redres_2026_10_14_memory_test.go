package extended

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
)

// A pronoun inside an untrusted wrapper's source label must not be rewritten:
// the rewrite would break the tag, and the wrapped body would then survive
// StripUntrustedWrappers as if the user had said it.
func TestRED_AnaphoraResolve_DoesNotBreakUntrustedWrapper(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.001
	em := New(t.TempDir(), newMockLLM(), cfg)
	defer em.Close()
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	em.index.markDirty()
	_ = em.AddAtom(context.Background(), MemoryAtom{Text: `the "quoted" thing`, SourceClass: SourceUserSaid, Type: TypeFact})
	em.index.Compact()

	for _, msg := range []string{
		`look <untrusted_content_abc source="it.txt">PAYLOAD-BODY</untrusted_content_abc>`,
		`look <untrusted_content_abc source="that.txt">PAYLOAD-BODY</untrusted_content_abc> and fix it`,
	} {
		got, _ := em.AnaphoraResolve(context.Background(), msg)
		if strings.Contains(StripUntrustedWrappers(got), "PAYLOAD-BODY") {
			t.Errorf("wrapped body survives wrapper stripping after resolution of %q: %q", msg, got)
		}
	}
}

// A slow delivery must not be able to land after a newer turn's delivery.
func TestRED_DeliverFollowUpsGen_StaleCannotOverwrite(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FollowUpSuggestionsEnabled = boolPtr(true)
	r := &Recall{cfg: cfg}
	var mu sync.Mutex
	var last string
	entered := make(chan struct{})
	release := make(chan struct{})
	first := true
	r.followUpSink = func(f []PredictedIntent) {
		mu.Lock()
		isFirst := first
		first = false
		mu.Unlock()
		if isFirst {
			close(entered)
			<-release
		}
		mu.Lock()
		last = f[0].Text
		mu.Unlock()
	}
	intents := func(s string) []PredictedIntent {
		return []PredictedIntent{{Text: s, Confidence: 1}}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.deliverFollowUpsGen(1, intents("stale")) }()
	<-entered
	go func() { defer wg.Done(); r.deliverFollowUpsGen(2, intents("fresh")) }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if last != "fresh" {
		t.Errorf("last delivered = %q, want fresh", last)
	}
}

// Two processes sharing a memory dir must not lose each other's links, and
// must not resurrect links to atoms the other one removed.
func TestRED_Associations_CrossProcessMerge(t *testing.T) {
	dir := t.TempDir()
	a := NewAssociationsWithDir(dir)
	b := NewAssociationsWithDir(dir)
	a.Link("atom-aaa", "atom-bbb")
	if err := a.Persist(); err != nil {
		t.Fatal(err)
	}
	b.Link("atom-ccc", "atom-ddd")
	if err := b.Persist(); err != nil {
		t.Fatal(err)
	}
	fresh := NewAssociationsWithDir(dir)
	if len(fresh.Related("atom-aaa")) != 1 || len(fresh.Related("atom-ccc")) != 1 {
		t.Errorf("link lost: aaa=%v ccc=%v", fresh.Related("atom-aaa"), fresh.Related("atom-ccc"))
	}
}

func TestRED_Associations_RemovedAtomNotResurrected(t *testing.T) {
	dir := t.TempDir()
	daemon := NewAssociationsWithDir(dir)
	daemon.Link("atom-aaa", "atom-bbb")
	_ = daemon.Persist()

	cli := NewAssociationsWithDir(dir)
	cli.RemoveAtom("atom-aaa")
	_ = cli.Persist()

	exists := map[string]bool{"atom-bbb": true, "atom-ccc": true}
	daemon.SetExists(func(id string) bool { return exists[id] })
	daemon.Link("atom-bbb", "atom-ccc")
	if err := daemon.Persist(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "associations.json"))
	if strings.Contains(string(raw), "atom-aaa") {
		t.Errorf("removed atom resurrected: %s", raw)
	}
	if !strings.Contains(string(raw), "atom-ccc") {
		t.Errorf("new link lost: %s", raw)
	}
}
