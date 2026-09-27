package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A read failure on the audit log (permissions, I/O error) must surface as an
// error instead of being treated as "no history yet" — otherwise a transient
// read error causes RecordTurn/RecordIngest to silently rewrite history.
// A directory in place of the log file gives a portable non-NotExist read
// failure (permission bits are not reliable under sandboxes/CI roots).
func TestAuditStore_ReadErrorPropagates(t *testing.T) {
	root := t.TempDir()
	s := NewAuditStore(root)
	if err := os.MkdirAll(filepath.Join(root, "audit"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "audit", "20260927-probe01.json"), 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Load("20260927-probe01"); err == nil {
		t.Fatal("Load on unreadable audit file = nil error, want error")
	}
	err := s.RecordTurn("20260927-probe01", AuditTurn{Turn: 1, UserMessage: "hi"})
	if err == nil {
		t.Fatal("RecordTurn on unreadable audit file = nil error, want error (history overwrite)")
	}
}
