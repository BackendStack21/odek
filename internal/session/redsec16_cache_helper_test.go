package session

import "os"

// cachedRevisionForTest reports what the next Save would take from the
// revision stamp cache for id.
func (s *Store) cachedRevisionForTest(id string) *Session {
	info, err := os.Lstat(s.path(id))
	return s.cachedRevision(id, info, err)
}
