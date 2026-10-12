package resource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileRefAncestorCredentialsDirDoesNotBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := filepath.Join(t.TempDir(), "credentials", "proj")
	if err := os.MkdirAll(filepath.Join(ws, ".docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"app.json", ".docker/Dockerfile"} {
		if err := os.WriteFile(filepath.Join(ws, n), []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"app.json", ".docker/Dockerfile"} {
		if _, err := NewFileResolver(ws).Load(context.Background(), n); err != nil {
			t.Errorf("@%s refused: %v", n, err)
		}
	}
}
