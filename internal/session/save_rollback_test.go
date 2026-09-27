package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

// A failed save must leave the caller's snapshot untouched: saveLocked
// mutates sess.Messages in place, and without a full restore the memory
// copy stays trimmed/redacted while the on-disk revision never advanced.
func TestFailedIndexSaveRestoresMessages(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-test-abc123secretkeyvalue"
	original := "handle with care " + secret
	sess := &Session{
		ID:        generateID(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Task:      original,
		Messages: []Message{
			{Role: "user", Content: original},
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	// Sabotage the index write: replace the index path with a directory so
	// the atomic rename fails on the second save.
	idxPath := filepath.Join(dir, "index.json")
	if err := os.Remove(idxPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(idxPath, 0o755); err != nil {
		t.Fatal(err)
	}

	msgsBefore := append([]Message(nil), sess.Messages...)
	err = store.Save(sess)
	if err == nil {
		t.Fatalf("expected the sabotaged index write to fail")
	}
	if !errors.Is(err, os.ErrExist) && !strings.Contains(err.Error(), "index") {
		t.Fatalf("unexpected error class: %v", err)
	}

	// The caller's in-memory snapshot must be intact after the failed save.
	if len(sess.Messages) != len(msgsBefore) {
		t.Fatalf("failed save mutated Messages: %d -> %d", len(msgsBefore), len(sess.Messages))
	}
	if sess.Messages[0].Content != original {
		t.Fatalf("failed save redacted/truncated the caller's Messages in place: %q", sess.Messages[0].Content)
	}

	// Repair and retry: the next Save must succeed (no stuck ErrConflict).
	if err := os.Remove(idxPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("retry Save after repaired index must succeed, got: %v", err)
	}
}

// blockingEmbedder simulates a slow remote embedding backend: Embed blocks
// until released. While Search is inside Embed, Add must complete — the
// embed call must not run under the index mutex.
type blockingEmbedder struct {
	mu       sync.Mutex
	release  chan struct{}
	started  chan struct{}
	onceDone bool
}

func (b *blockingEmbedder) Fit(corpus []string) error { return nil }
func (b *blockingEmbedder) Embed(text string) (vector.Vector, error) {
	if !b.onceDone {
		b.onceDone = true
		close(b.started)
		<-b.release
	}
	return vector.Vector{1, 2, 3}, nil
}
func (b *blockingEmbedder) EmbedAll(texts []string) ([]vector.Vector, error) {
	out := make([]vector.Vector, len(texts))
	for i := range texts {
		out[i] = vector.Vector{1, 2, 3}
	}
	return out, nil
}
func (b *blockingEmbedder) Fingerprint() string        { return "blocking" }
func (b *blockingEmbedder) SaveState(path string)      {}
func (b *blockingEmbedder) LoadState(path string) bool { return false }

func TestSearchEmbedDoesNotBlockAdd(t *testing.T) {
	emb := &blockingEmbedder{
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
	vi := &VectorIndex{
		emb:   emb,
		ready: true,
		store: newTestVectorStore(),
	}
	vi.store.Add("seed", vector.Vector{1, 2, 3})

	searchDone := make(chan struct{})
	go func() {
		defer close(searchDone)
		_, _ = vi.Search("query", 5)
	}()

	select {
	case <-emb.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Search never reached Embed")
	}

	// Add must complete while Search is parked inside Embed.
	addDone := make(chan error, 1)
	go func() {
		addDone <- vi.Add("sess-blocking", []Message{{Role: "user", Content: "hello"}})
	}()
	select {
	case err := <-addDone:
		if err != nil {
			t.Fatalf("Add failed while Search was embedding: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Add blocked on the index mutex while Search was embedding")
	}

	close(emb.release)
	<-searchDone
}

func newTestVectorStore() *vector.Store {
	return vector.NewStore(vector.CosineDistance)
}
