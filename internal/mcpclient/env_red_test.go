package mcpclient

import "testing"

func TestRED_EnvSanitizationSecretShapedNames(t *testing.T) {
	for _, k := range []string{"DB_PASSWD", "GPG_PASSPHRASE", "SESSION_COOKIE", "HTTP_AUTHORIZATION"} {
		if !isSensitiveEnvVar(k) {
			t.Errorf("%s is secret-shaped but not stripped", k)
		}
	}
}

func TestEnvSanitization_AuthShapedOverridesDropped(t *testing.T) {
	env := buildEnv(map[string]string{"DB_PASSWD": "x", "SESSION_COOKIE": "y", "KEEP_ME": "z"})
	found := false
	for _, e := range env {
		if e == "DB_PASSWD=x" || e == "SESSION_COOKIE=y" {
			t.Fatalf("secret-shaped override leaked: %s", e)
		}
		if e == "KEEP_ME=z" {
			found = true
		}
	}
	if !found {
		t.Fatal("benign override dropped")
	}
}
