package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionResolver_Load_SanitizesAuthToken(t *testing.T) {
	dir := t.TempDir()
	id := "20260918-sanitize"
	data := `{"id":"` + id + `","auth_token":"secret-token","task":"private","messages":[{"role":"user","content":"hello"}]}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := NewSessionResolver(dir).Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "secret-token") || strings.Contains(content, "private") {
		t.Fatalf("session metadata leaked: %s", content)
	}
	if !strings.Contains(content, "hello") {
		t.Fatalf("transcript missing: %s", content)
	}
}

func TestSessionResolver_SearchIgnoresAuxiliaryJSONAndRoutesPrefix(t *testing.T) {
	dir := t.TempDir()
	id := "20260918-search"
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"id":"`+id+`","messages":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"entries":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := NewSessionResolver(dir).Search(context.Background(), "sess:"+id, 10)
	if err != nil || len(got) != 1 || got[0].ID != "@sess:"+id {
		t.Fatalf("search=%v err=%v", got, err)
	}
}

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
