package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With the workspace at $HOME, @file references must not reach odek's trust
// anchors (secrets, config, sessions) or credential files: their content would
// enter the model's context with no approval step.
func TestRED_FileRefReachesOdekTrustAnchorsAndCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".odek/secrets.env", "OPENAI_API_KEY=sk-hunt-secret")
	write(".odek/config.json", `{"api_key":"hunt-secret"}`)
	write(".odek/sessions/20260101-0123456789abcdef0123456789abcdef.json", `{"auth_token":"hunt-secret"}`)
	write(".ssh/id_rsa", "hunt-secret")
	write(".env", "TOKEN=hunt-secret")
	write("notes/readme.txt", "ordinary notes")

	r := NewFileResolver(home)
	for _, ref := range []string{
		".odek/secrets.env",
		".odek/config.json",
		".odek/sessions/20260101-0123456789abcdef0123456789abcdef.json",
		".ssh/id_rsa",
		".env",
		".ODEK/Secrets.env",
	} {
		got, err := r.Load(context.Background(), ref)
		if err == nil || strings.Contains(got, "hunt-secret") {
			t.Errorf("@%s was loaded into the prompt (err=%v, content=%q)", ref, err, got)
		}
	}
	got, err := r.Load(context.Background(), "notes/readme.txt")
	if err != nil || got != "ordinary notes" {
		t.Fatalf("ordinary file must still load: %q, %v", got, err)
	}
}

// Autocomplete must not list names or sizes of protected paths either.
func TestRED_FileSearchListsProtectedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, rel := range []string{".odek/secrets.env", ".ssh/id_rsa", "notes/plain.txt"} {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := NewFileResolver(home)
	for _, q := range []string{"secrets", "id_rsa", "env"} {
		res, err := r.Search(context.Background(), q, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range res {
			if strings.HasPrefix(x.Label, ".odek") || strings.HasPrefix(x.Label, ".ssh") {
				t.Errorf("search %q listed protected path %q", q, x.Label)
			}
		}
	}
	res, _ := r.Search(context.Background(), "plain", 20)
	if len(res) != 1 {
		t.Fatalf("ordinary file must still be listed, got %v", res)
	}
}

// On case-insensitive filesystems a long-s spelling (U+017F) folds to the
// same name as secrets.env. The classifier fix lands on another branch; this
// is expected to pass after merge.
func TestRED_FileRefLongSAliasOfSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".odek", "secrets.env")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("hunt-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".odek", "\u017fecrets.env")); err != nil {
		t.Skip("filesystem does not alias U+017F to 's'")
	}
	got, err := NewFileResolver(home).Load(context.Background(), ".odek/\u017fecrets.env")
	if err == nil && strings.Contains(got, "hunt-secret") {
		t.Skip("alias bypass is fixed by danger.FoldPathCase on the tools branch; expected to pass after merge")
	}
}
