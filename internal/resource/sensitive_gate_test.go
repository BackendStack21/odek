package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRefOrdinaryRootOutsideHomeLoads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, root := range []string{"/usr/share/dict", "/opt", "/srv"} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		// A missing file must fail as not-found, never as protected.
		_, err := NewFileResolver(root).Load(context.Background(), "README")
		if err != nil && strings.Contains(err.Error(), "protected") {
			t.Errorf("root %s: ordinary reference refused: %v", root, err)
		}
	}
	if protectedTarget("/opt/app/main.go") || protectedTarget("/srv/app/main.go") ||
		protectedTarget("/usr/local/src/proj/main.go") || protectedTarget("/var/www/site/index.php") ||
		protectedTarget("/tmp/proj/main.go") {
		t.Fatal("ordinary system-prefix project file treated as protected")
	}
	if !protectedTarget("/etc/shadow") || !protectedTarget("/etc/sudoers") {
		t.Fatal("system secrets must stay protected")
	}
}

func TestFileRefInWorkspaceCredentialFilesRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	secrets := []string{".env", "server.key", ".npmrc", "credentials.json", ".env.production"}
	for _, n := range secrets {
		if err := os.WriteFile(filepath.Join(ws, n), []byte("hunt-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{".env.example", "main.go"} {
		if err := os.WriteFile(filepath.Join(ws, n), []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := NewFileResolver(ws)
	for _, n := range secrets {
		if got, err := r.Load(context.Background(), n); err == nil || strings.Contains(got, "hunt-secret") {
			t.Errorf("@%s loaded", n)
		}
	}
	for _, n := range []string{".env.example", "main.go"} {
		if _, err := r.Load(context.Background(), n); err != nil {
			t.Errorf("@%s refused: %v", n, err)
		}
	}
	res, _ := r.Search(context.Background(), "server", 20)
	for _, x := range res {
		if x.Label == "server.key" {
			t.Errorf("search listed server.key")
		}
	}
}
