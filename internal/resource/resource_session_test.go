package resource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The @-resource session resolver must carry the same hardening as the
// file resolver: a planted symlink inside the sessions dir must not inline
// arbitrary files (e.g. ~/.ssh keys) into model context, and reads must be
// size-capped.
func TestSessionResolver_Load_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "id_ed25519")
	if err := os.WriteFile(secretPath, []byte("PRIVATE KEY MATERIAL"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(dir, "20260903-deadbeef.json")); err != nil {
		t.Fatal(err)
	}

	r := NewSessionResolver(dir)
	content, err := r.Load(t.Context(), "20260903-deadbeef")
	if err == nil {
		t.Fatalf("symlinked session file loaded: %d bytes of target content inlined (want error)", len(content))
	}
	if strings.Contains(content, "PRIVATE KEY MATERIAL") {
		t.Fatal("symlink target content leaked through the session resolver")
	}
}

func TestSessionResolver_Load_CapsSize(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 2*1024*1024) // 2 MiB
	id := "20260903-cafebabe"
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(big), 0600); err != nil {
		t.Fatal(err)
	}

	r := NewSessionResolver(dir)
	_, err := r.Load(t.Context(), id)
	if err == nil {
		t.Fatal("oversized session file loaded without a size cap (want error)")
	}
}
