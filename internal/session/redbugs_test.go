package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/llm"
)

// RED #7 (S1): Cleanup passes index IDs straight to the filesystem with
// no ValidateSessionID check. A planted/tampered index.json entry with
// id "../victim" makes Cleanup delete files outside the sessions dir —
// the exact threat model Load/Delete/saveLocked already defend against.
func TestRED_CleanupValidatesSessionIDs(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "sessions")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(base, "victim.json")
	if err := os.WriteFile(victim, []byte(`{"important":"data"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := []*IndexEntry{{
		ID:        "../victim",
		Title:     "planted",
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-time.Hour),
	}}
	data, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(storeDir, "index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStoreWithDir(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(time.Now()); err != nil {
		t.Fatalf("Cleanup error: %v", err)
	}
	if _, err := os.Stat(victim); os.IsNotExist(err) {
		t.Fatalf("Cleanup deleted %s outside the store dir via unvalidated index id", victim)
	}
}

// RED #8 (S4): A stale index entry (file deleted before the index was
// rewritten — Delete removes the file first) breaks Latest() entirely
// and surfaces phantom rows in List(), even though valid sessions exist.
func TestRED_StaleIndexEntriesHandled(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{
		ID:        generateID(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Task:      "real session",
		Messages:  []llm.Message{{Role: "user", Content: "hi"}},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	// Plant a stale newest entry pointing at a deleted file.
	idx := []*IndexEntry{
		{
			ID:        sess.ID,
			Title:     "real session",
			CreatedAt: sess.CreatedAt,
			UpdatedAt: sess.UpdatedAt,
		},
		{
			ID:        "ghost0000000000",
			Title:     "phantom",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now().Add(time.Hour),
		},
	}
	data, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest() = %v; want the real session despite stale newest index entry", err)
	}
	if got == nil || got.ID != sess.ID {
		t.Fatalf("Latest() = %+v; want session %s", got, sess.ID)
	}

	list, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == "ghost0000000000" {
			t.Errorf("List() returned phantom session from stale index entry")
		}
	}
	if len(list) == 0 {
		t.Errorf("List() empty; want the real session")
	}
}

// fakeCountingEmbedder implements embedding.TextEmbedder counting Embed calls.
type fakeCountingEmbedder struct {
	calls int
}

func (f *fakeCountingEmbedder) Fit(corpus []string) error            { return nil }
func (f *fakeCountingEmbedder) Embed(text string) (vector.Vector, error) {
	f.calls++
	return nil, errFakeDown
}
func (f *fakeCountingEmbedder) EmbedAll(texts []string) ([]vector.Vector, error) {
	return nil, errFakeDown
}
func (f *fakeCountingEmbedder) Fingerprint() string       { return "fake" }
func (f *fakeCountingEmbedder) SaveState(path string)     {}
func (f *fakeCountingEmbedder) LoadState(path string) bool { return false }

var errFakeDown = errorString("embedding backend down")

type errorString string

func (e errorString) Error() string { return string(e) }

// RED #9 (S3): The failedAt cool-down is only consulted in rebuildLocked.
// Once the index is ready, Add/Search hit the embedding backend on every
// call even during the cool-down, hammering a down server — contradicting
// the package docs ("a down server must not be re-hit on every search or
// session save").
func TestRED_VectorIndexCooldownOnReadyPath(t *testing.T) {
	vi := &VectorIndex{
		emb:   &fakeCountingEmbedder{},
		ready: true,
	}
	// Simulate a failure that just happened: the cool-down window is open.
	vi.failedAt = time.Now()

	emb := vi.emb.(*fakeCountingEmbedder)

	_, _ = vi.Search("query", 5)
	_ = vi.Add("sess-1", []llm.Message{{Role: "user", Content: "hello"}})
	_, _ = vi.Search("query2", 5)

	if emb.calls != 0 {
		t.Errorf("embedder called %d times during cool-down; want 0 (backend must not be hammered)", emb.calls)
	}
}
