package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Directory-name rules (a "secrets" folder) must not block ordinary media, but
// credential-looking files are rejected wherever they live.
func TestMediaSensitivityDirRulesOnlyForCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cwd, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(cwd, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"photo.png", "clip.mp4", "voice.ogg"} {
		if err := checkMediaPathSensitivity(filepath.Join(cwd, "secrets", n)); err != nil {
			t.Errorf("%s in a secrets dir rejected: %v", n, err)
		}
	}
	for _, n := range []string{"server.key", "id_rsa", "service-account.json", "notes.txt"} {
		if err := checkMediaPathSensitivity(filepath.Join(cwd, "secrets", n)); err == nil {
			t.Errorf("%s in a secrets dir accepted", n)
		}
	}
	for _, n := range []string{"deploy.pem", "terraform.tfstate"} {
		if err := checkMediaPathSensitivity(filepath.Join(cwd, n)); err == nil {
			t.Errorf("%s accepted", n)
		}
	}
}

// A crash mid-write can leave an empty counter; that is no usage, while
// non-numeric content still fails closed.
func TestDailyBudgetEmptyCounterIsZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".odek"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(budgetFilePath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bot := NewBot("testtoken")
	bot.SetDailyTokenBudget(1000)
	if used, _ := bot.DailyTokenUsage(); used != 0 {
		t.Errorf("empty counter reads as used=%d, want 0", used)
	}
	if err := bot.CheckDailyBudget(10); err != nil {
		t.Errorf("CheckDailyBudget on an empty counter: %v", err)
	}
	if data, _ := os.ReadFile(budgetFilePath()); string(data) != "10" {
		t.Errorf("counter = %q, want 10", data)
	}
}

func TestStripMarkdownCodeAndEscapes(t *testing.T) {
	cases := map[string]string{
		"a \\*b\\* c":            "a *b* c",
		"`x_y\\`z\\\\w`":         "x_y`z\\w",
		"```go\nfoo_bar *\n```t": "go\nfoo_bar *\nt",
		"*b* _i_ ~s~":            "b i s",
		"trailing\\":             "trailing",
	}
	for in, want := range cases {
		if got := StripMarkdown(in); got != want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCutEntityBalancedReopensEntities(t *testing.T) {
	text := "*" + strings.Repeat("word ", 40) + "end*"
	head, rest, ok := cutEntityBalanced(text, 60)
	if !ok || len(head) > 60 || !strings.HasSuffix(head, "*") || !strings.HasPrefix(rest, "*") {
		t.Fatalf("unbalanced cut: ok=%v head=%q rest=%q", ok, head, rest)
	}
	if _, _, ok := cutEntityBalanced("abcdef", 0); ok {
		t.Error("cut with no room reported ok")
	}
}
