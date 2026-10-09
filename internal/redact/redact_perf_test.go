package redact

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// redactSecretsReference is the pattern layer applied with an unconditional
// ReplaceAllString per pattern, the behavior RedactSecrets must reproduce.
func redactSecretsReference(text string) string {
	if text == "" {
		return text
	}
	result := text
	if r := currentReplacer(); r != nil {
		result = r.Replace(result)
	}
	for _, p := range patterns {
		result = p.ReplaceAllString(result, "[REDACTED]")
	}
	return result
}

func packageStringLiterals(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob("*_test.go")
	fset := token.NewFileSet()
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
			return true
		})
	}
	return out
}

func TestRED_Redact_GatedPassesMatchUnconditionalPasses(t *testing.T) {
	ResetSecrets()
	RegisterSecret("known-secret-value-12345")
	defer ResetSecrets()

	corpus := packageStringLiterals(t)
	rng := rand.New(rand.NewSource(11))
	check := func(s string) {
		if got, want := RedactSecrets(s), redactSecretsReference(s); got != want {
			t.Fatalf("output differs for %q:\n got %q\nwant %q", s, got, want)
		}
	}
	for _, s := range corpus {
		check(s)
		for i := 0; i < 3; i++ {
			o := corpus[rng.Intn(len(corpus))]
			a := rng.Intn(len(s) + 1)
			b := a + rng.Intn(len(s)-a+1)
			check(s[:a] + "\n" + o + " " + s[b:])
			check(s[:b] + s[a:])
		}
	}
}

func TestRED_Redact_CleanTextDoesNotCopyPerPattern(t *testing.T) {
	ResetSecrets()
	text := strings.Repeat("func main() { fmt.Println(\"hello world\") } // plain code line\n", 1<<20/64)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out := RedactSecrets(text)
	runtime.ReadMemStats(&after)
	if out != text {
		t.Fatal("clean text must be returned unchanged")
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
		t.Fatalf("RedactSecrets allocated %d bytes for a 1 MiB clean input, want <= 8 MiB", alloc)
	}
}

func BenchmarkRedactSecretsClean1MiB(b *testing.B) {
	ResetSecrets()
	text := strings.Repeat("func main() { fmt.Println(\"hello world\") } // plain code line\n", 1<<20/64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		RedactSecrets(text)
	}
}
