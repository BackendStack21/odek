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
