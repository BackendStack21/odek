package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/redact"
)

// Security review wave C, F2: RegisterSecretsFromEnv gates on
// sensitive-looking NAMES, but every value in secrets.env is by definition
// a secret the operator chose to inject — a bare echo of SMTP_URL or
// HARBOR_ROBOT content must be redacted like any other known secret
// (redact.go's documented bare-echo contract). secretsEnvNames was
// collected but never fed to the redaction layer.
func TestSecretsEnv_AllValuesRegisteredForRedaction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	global := filepath.Join(dir, ".odek")
	if err := os.MkdirAll(global, 0700); err != nil {
		t.Fatal(err)
	}
	// Non-sensitive NAME, long secret VALUE.
	const val = "smtp-inject-secret-value-9f3aba77cc"
	if err := os.WriteFile(filepath.Join(global, "secrets.env"), []byte("SMTP_URL="+val+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_ = LoadConfig(CLIFlags{})

	out := redact.RedactSecrets("configured as smtp://" + val + "@mail")
	if strings.Contains(out, val) {
		t.Fatalf("secrets.env value with a non-sensitive name survived redaction: %q", out)
	}
}
