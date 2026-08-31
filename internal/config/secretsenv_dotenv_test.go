package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bug-sweep 2026-08-31: the secrets.env parser fed the highest-priority
// config layer but ignored dotenv conveniences operators rely on, and a
// single overlong line silently dropped every secret after it.
//
// RED-first: each case fails against the old parser (literal "export KEY"
// keys, quotes kept in values, inline comments kept in values).

func writeSecretsAndLoad(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	globalDir := filepath.Join(dir, ".odek")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "secrets.env"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	_ = LoadConfig(CLIFlags{})
}

func TestSecretsEnv_DotenvConveniences(t *testing.T) {
	t.Setenv("SE_DOT_PLAIN", "")
	t.Setenv("SE_DOT_QUOTED", "")
	t.Setenv("SE_DOT_SINGLE", "")
	t.Setenv("SE_DOT_COMMENT", "")
	t.Setenv("SE_DOT_HASHURL", "")
	writeSecretsAndLoad(t, strings.Join([]string{
		"export SE_DOT_PLAIN=plainval",
		`SE_DOT_QUOTED="quoted val"`,
		`SE_DOT_SINGLE='single val'`,
		"SE_DOT_COMMENT=value # trailing comment",
		"SE_DOT_HASHURL=https://example.com/pw#frag",
		"# full line comment",
		"",
	}, "\n"))

	cases := map[string]string{
		"SE_DOT_PLAIN":   "plainval",      // export prefix stripped
		"SE_DOT_QUOTED":  "quoted val",    // double quotes stripped
		"SE_DOT_SINGLE":  "single val",    // single quotes stripped
		"SE_DOT_COMMENT": "value",         // whitespace-preceded comment stripped
		"SE_DOT_HASHURL": "https://example.com/pw#frag", // embedded # kept
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestSecretsEnv_OverlongLineDoesNotDropRemainingSecrets(t *testing.T) {
	t.Setenv("SE_DOT_AFTER_BIG", "")
	// 100KiB single line: over the old 64KiB scanner limit, under the new
	// 1MiB buffer — the GOOD secret after it must still load.
	big := "SE_DOT_BIG=" + strings.Repeat("x", 100*1024)
	writeSecretsAndLoad(t, big+"\nSE_DOT_AFTER_BIG=goodval\n")
	if got := os.Getenv("SE_DOT_AFTER_BIG"); got != "goodval" {
		t.Errorf("SE_DOT_AFTER_BIG = %q, want goodval — an overlong line must not silently drop later secrets", got)
	}
}
