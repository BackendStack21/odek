package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

// Composer, boto and s3cmd credentials in $HOME are never uploadable media.
func TestRED_MediaMoreSecretFilesInHome(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	t.Setenv("HOME", home)
	t.Chdir(home)
	for _, rel := range []string{
		".composer/auth.json",
		".config/composer/auth.json",
		".boto",
		".s3cfg",
	} {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveMediaPathForChat(p, 42); err == nil {
			t.Errorf("credential file %s accepted as outbound media", rel)
		}
	}
	// An unrelated auth.json stays uploadable.
	p := filepath.Join(home, "auth.json")
	if err := os.WriteFile(p, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMediaPathForChat(p, 42); err != nil {
		t.Errorf("plain auth.json rejected: %v", err)
	}
}
