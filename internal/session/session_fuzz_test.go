package session

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzSessionLoad feeds random session IDs and random file contents into the
// session load/save path and asserts it never panics, never accepts an
// invalid ID, never returns a session whose embedded ID mismatches the
// requested one, and never writes outside the store directory.
func FuzzSessionLoad(f *testing.F) {
	validID := "20260101-abcdef0123456789abcdef0123456789"
	type seed struct {
		id   string
		data string
	}
	seeds := []seed{
		{validID, `{"id":"` + validID + `","model":"gpt","messages":[{"role":"user","content":"hi"}]}`},
		{validID, `{"id":"different-id-mismatch","messages":[]}`},
		{validID, `not json`},
		{validID, ``},
		{validID, `{"id":123}`},
		{validID, `{"id":"` + validID + `","messages":[{"role":"user","content":"` + "x" + `"}],"buffer":["a","b"]}`},
		{validID, `null`},
		{validID, `[]`},
		{validID, `{"id":"` + validID + `","created_at":"not-a-time"}`},
		{"../../../etc/passwd", `{"id":"x"}`},
		{"..", `{}`},
		{".", `{}`},
		{"", `{}`},
		{"a/b", `{}`},
		{`a\b`, `{}`},
		{"a\x00b", `{}`},
		{validID + "..hidden", `{}`},
		{"normal-name", `{"id":"normal-name","messages":[]}`},
		{validID, `{"id":"` + validID + `","messages":[{"role":"tool","content":"sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`},
	}
	for _, s := range seeds {
		f.Add(s.id, s.data)
	}

	f.Fuzz(func(t *testing.T, id, data string) {
		dir := t.TempDir()
		s := &Store{dir: dir}

		if err := ValidateSessionID(id); err != nil {
			// Invalid IDs must be rejected by both the read and write paths —
			// this is what guarantees no writes outside the store directory.
			if _, lerr := s.Load(id); lerr == nil {
				t.Fatalf("Load(%q) succeeded for invalid ID", id)
			}
			if serr := s.Save(&Session{ID: id}); serr == nil {
				t.Fatalf("Save(%q) succeeded for invalid ID", id)
			}
			return
		}

		path := s.path(id)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			// e.g. filename too long for the filesystem; the load path must
			// still not panic or succeed below.
			if _, lerr := s.Load(id); lerr == nil {
				t.Fatalf("Load(%q) succeeded although the file could not be written", id)
			}
			return
		}

		sess, err := s.Load(id)
		if err != nil {
			return
		}
		if sess.ID != id {
			t.Fatalf("Load(%q) returned session with mismatched embedded ID %q", id, sess.ID)
		}

		// Round-trip: saving the loaded session must succeed and must land
		// inside the store directory.
		if err := s.Save(sess); err != nil {
			t.Fatalf("Save(%q) after successful Load failed: %v", id, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Save(%q) did not write %s: %v", id, path, err)
		}
		if filepath.Dir(path) != dir {
			t.Fatalf("session path %q escapes store dir %q", path, dir)
		}
	})
}
