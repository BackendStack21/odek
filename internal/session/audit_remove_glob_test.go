package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Remove must only touch the named session's audit files. A session id
// containing glob metacharacters (ValidateSessionID allows '*', '?' and
// '[') must not sweep other sessions' quarantined sidecars.
func TestRED_AuditRemoveDoesNotGlobOtherSessions(t *testing.T) {
	dir := t.TempDir()
	a := NewAuditStore(dir)
	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(auditDir, "20260101-aaaaaa.json.corrupt-1")
	mine := filepath.Join(auditDir, "20260101-bbbbbb.json.corrupt-1")
	for _, p := range []string{other, mine} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Remove("*"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("Remove(\"*\") deleted another session's sidecar")
	}
	if err := a.Remove("20260101-bbbbbb"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("own sidecar not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other session's sidecar removed by an exact id")
	}
}

// Remove on a store whose audit directory does not exist yet is a no-op.
func TestAuditRemoveWithoutAuditDir(t *testing.T) {
	a := NewAuditStore(t.TempDir())
	if err := a.Remove("20260101-cccccc"); err != nil {
		t.Fatalf("remove without audit dir: %v", err)
	}
}
