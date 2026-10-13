package extended

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every atoms.json mutation takes the cross-process lock; when the store
// directory cannot be created (a file sits where the directory should be)
// Add, Remove and Pin fail cleanly instead of writing beside it.
func TestAtomStoreLockFileErrors(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "extended")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewAtomStore(blocker)
	atom := MemoryAtom{ID: "0123456789abcdef0123456789abcdef", Text: "x", Type: TypeFact, SourceClass: SourceUserSaid, CreatedAt: time.Now()}
	if err := s.Add(atom, 0); err == nil {
		t.Error("Add into a blocked store dir should fail")
	}
	if err := s.Remove(atom.ID); err == nil {
		t.Error("Remove in a blocked store dir should fail")
	}
	if err := s.Pin(atom.ID, true); err == nil {
		t.Error("Pin in a blocked store dir should fail")
	}
	if _, err := s.sweepOrphanChunks(true, time.Now()); err == nil {
		t.Error("sweep in a blocked store dir should fail")
	}
}
