package memory

import (
	"testing"
)

// TestRED_FactTrailingSeparatorFragmentCorruptsNeighbour: Add rejects
// content containing the full entry separator "\n§\n", but an entry that
// merely ENDS with "\n§" passes. Joined with the next entry it forms
// "\n§\n§\n", which parseEntries splits at the first separator: the stored
// entry loses its tail and the following entry gains a "§\n" prefix, so the
// read-back fact list differs from what was written (and dedup/remove by
// exact text stop matching).
func TestRED_FactTrailingSeparatorFragmentCorruptsNeighbour(t *testing.T) {
	for _, frag := range []string{"first fact\n§", "§\nfirst fact"} {
		fs := NewFactStore(t.TempDir(), 0, 0)
		if err := fs.Add("user", "zero fact"); err != nil {
			t.Fatal(err)
		}
		if err := fs.Add("user", frag); err != nil {
			// Rejecting the fragment outright is also acceptable.
			continue
		}
		if err := fs.Add("user", "second fact"); err != nil {
			t.Fatal(err)
		}
		entries, err := fs.Entries("user")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"zero fact", frag, "second fact"}
		if len(entries) != len(want) || entries[0] != want[0] || entries[1] != want[1] || entries[2] != want[2] {
			t.Fatalf("entries read back = %q, want %q", entries, want)
		}
	}
	// The replace paths apply the same rule.
	fs := NewFactStore(t.TempDir(), 0, 0)
	if err := fs.Add("user", "alpha fact"); err != nil {
		t.Fatal(err)
	}
	if err := fs.ReplaceAt("user", 0, "alpha fact\n§"); err == nil {
		t.Fatal("ReplaceAt accepted a trailing separator fragment")
	}
	// A lone "§" or one embedded mid-line is ordinary text.
	if err := fs.Add("user", "§"); err != nil {
		t.Fatalf("lone § rejected: %v", err)
	}
	if err := fs.Add("user", "section § sign"); err != nil {
		t.Fatalf("embedded § rejected: %v", err)
	}
	entries, _ := fs.Entries("user")
	if len(entries) != 3 || entries[1] != "§" || entries[2] != "section § sign" {
		t.Fatalf("entries = %q", entries)
	}
}

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
