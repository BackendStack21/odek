package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countAuditReads swaps the audit read hook for a counter.
func countAuditReads(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := auditReadFile
	auditReadFile = func(p string) ([]byte, error) {
		n++
		return os.ReadFile(p)
	}
	t.Cleanup(func() { auditReadFile = orig })
	return &n
}

func TestRED_Audit_SteadyStateAppendDoesNotRereadLog(t *testing.T) {
	s := NewAuditStore(t.TempDir())
	reads := countAuditReads(t)
	const n = 50
	for i := 0; i < n; i++ {
		if err := s.RecordIngest("sess1", 1, "src", "content"); err != nil {
			t.Fatal(err)
		}
	}
	if *reads > 2 {
		t.Fatalf("appends re-read the log %d times for %d appends; want at most 2", *reads, n)
	}
	log, err := s.Load("sess1")
	if err != nil || len(log.Ingests) != n {
		t.Fatalf("ingests=%d err=%v", len(log.Ingests), err)
	}
}

func TestAudit_MemoRevalidatesAfterExternalChange(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)
	if err := s.RecordIngest("sess1", 1, "a", "x"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "audit", "sess1.json")

	// External writer leaves a torn fragment: the next append must repair it.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"ing`)
	f.Close()
	if err := s.RecordIngest("sess1", 1, "b", "y"); err != nil {
		t.Fatal(err)
	}
	log, _ := s.Load("sess1")
	if len(log.Ingests) != 2 {
		t.Fatalf("want 2 ingests after torn-tail repair, got %d", len(log.Ingests))
	}

	// Symlink swapped in: must never be followed.
	target := filepath.Join(dir, "victim")
	os.WriteFile(target, []byte("keep"), 0600)
	os.Remove(path)
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlinks unsupported")
	}
	if err := s.RecordIngest("sess1", 1, "c", "z"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("symlink target modified: %q", b)
	}

	// Directory swapped in: refuse.
	os.Remove(path)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIngest("sess1", 1, "d", "w"); err == nil {
		t.Fatal("append into a directory must fail")
	}
}

func TestAudit_MemoDroppedByRemove(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)
	_ = s.RecordIngest("sess1", 1, "a", "x")
	if err := s.Remove("sess1"); err != nil {
		t.Fatal(err)
	}
	_ = s.RecordIngest("sess1", 1, "b", "y")
	log, _ := s.Load("sess1")
	if len(log.Ingests) != 1 || !strings.Contains(log.Ingests[0].Source, "b") {
		t.Fatalf("unexpected log after remove: %+v", log.Ingests)
	}
}
