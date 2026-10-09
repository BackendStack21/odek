package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

// Credential files that live in $HOME must never be uploadable as media even
// when the bot runs from $HOME.
func TestRED_MediaSecretFilesInHome(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	t.Setenv("HOME", home)
	t.Chdir(home)
	cases := []string{
		".netrc",
		".git-credentials",
		".npmrc",
		".pypirc",
		".docker/config.json",
		".kube/config",
		".config/gcloud/application_default_credentials.json",
		".config/gh/hosts.yml",
		".pgpass",
		".azure/accessTokens.json",
	}
	for _, rel := range cases {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveMediaPathForChat(p, 42); err == nil {
			t.Errorf("credential file %s accepted as outbound media", rel)
		}
	}
}

// Ordinary files that merely share a directory or name stem stay uploadable.
func TestMediaCredentialCheck_AllowsOrdinaryFiles(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	t.Setenv("HOME", home)
	t.Chdir(home)
	for _, rel := range []string{"notes.txt", "config.json", ".docker/other.json", "kube/config"} {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveMediaPathForChat(p, 42); err != nil {
			t.Errorf("ordinary file %s rejected: %v", rel, err)
		}
	}
}
