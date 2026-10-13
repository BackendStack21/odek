package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRED_EpisodeIndexFilesPrivate: the persisted episode vector index and
// embedder state are written by go-vector with os.Create (0666 & umask), so
// they ended up 0644 next to 0600 memory files. Summaries are
// reconstructible from the vectors' corpus and the embedder holds the
// vocabulary, so both files are private like everything else under
// ~/.odek/memory.
func TestRED_EpisodeIndexFilesPrivate(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	if err := es.Write("sess-1", "worked on the login credential flow", 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := es.recallByVector("login credential", 1); len(got) != 1 {
		t.Fatal("index should serve recall")
	}
	for _, name := range []string{episodeVectorFile, episodeEmbedFile, episodeIndexMetaFile} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s not persisted: %v", name, err)
		}
		if perm := st.Mode().Perm(); perm != 0600 {
			t.Errorf("%s mode %o, want 0600", name, perm)
		}
	}
}
