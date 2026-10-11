package memory

import (
	"testing"
)

// TestRED_EpisodeIndexColdStartServesStaleGob: a new process cold-starts the
// per-turn recall index from the persisted gob (tryLoadLocked) without
// checking it against episodes/index.json. An episode written at the end of
// the previous process (markDirty only — no search ran afterwards, which is
// the normal `odek run` shape: recall at turn start, episode written at
// session end, exit) is never in the gob, so every later process loads the
// stale vectors and never recalls it until some process happens to write
// and then search.
func TestRED_EpisodeIndexColdStartServesStaleGob(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()

	// Process 1: one episode, a recall (persists the gob), then a new
	// episode written at session end, then exit.
	es1 := NewEpisodeStore(dir, nil)
	writeTestEpisode(t, es1, "20261011-aaaa", "refactored the login button styling and css colors")
	if got, _ := es1.recallByVector("login button css", 3); len(got) == 0 {
		t.Fatal("setup: first episode not recalled")
	}
	writeTestEpisode(t, es1, "20261011-bbbb", "migrated the postgres database schema to version two")

	// Process 2: fresh in-memory singleton over the same directory.
	resetEpIdxes()
	es2 := NewEpisodeStore(dir, nil)
	got, err := es2.recallByVector("postgres database schema migration", 3)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ep := range got {
		if ep.SessionID == "20261011-bbbb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("episode written after the last gob persist is never recalled by a new process; got %+v", got)
	}

	// Process 3: the rebuilt gob now matches the corpus and is loaded as-is.
	resetEpIdxes()
	es3 := NewEpisodeStore(dir, nil)
	vi := sharedEpisodeIndex(es3.dir, es3.newEmbedder)
	vi.mu.Lock()
	loaded := vi.tryLoadLocked()
	vi.mu.Unlock()
	if !loaded {
		t.Fatal("persisted index matching the current corpus was not reused")
	}
}
