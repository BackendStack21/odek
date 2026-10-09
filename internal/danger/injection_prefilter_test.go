package danger

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scanInjectionReference is ScanInjection without the literal prefilter: every
// pattern runs its full automaton on every text variant.
func scanInjectionReference(content string) []ScanResult {
	if content == "" {
		return nil
	}
	var results []ScanResult
	if ContainsInvisible(content) {
		results = append(results, ScanResult{Label: "hidden unicode characters"})
	}
	if HasConfusableScript(content) {
		results = append(results, ScanResult{Label: "mixed confusable script"})
	}
	normalized := NormalizeForScan(content)
	folded := FoldHomoglyphs(normalized)
	foldDistinct := folded != normalized
	var foldedNu string
	if strings.Contains(normalized, "ν") {
		foldedNu = FoldHomoglyphs(strings.ReplaceAll(normalized, "ν", "n"))
	}
	for _, p := range injectionPatterns {
		if p.Re.MatchString(normalized) || (foldDistinct && p.Re.MatchString(folded)) ||
			(foldedNu != "" && p.Re.MatchString(foldedNu)) {
			results = append(results, ScanResult{Label: p.Label, Pattern: p.Re.String()})
		}
	}
	if scanMarkdownHeaders(content) {
		results = append(results, ScanResult{Label: markdownHeaderLabel, Pattern: markdownHeaderRe.String()})
	}
	return results
}

func sameResults(a, b []ScanResult) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// testStringLiterals collects every string literal in the package's test files.
func testStringLiterals(t *testing.T) []string {
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

func TestRED_Injection_PrefilterEquivalentToFullScan(t *testing.T) {
	corpus := testStringLiterals(t)
	for _, p := range injectionPatterns {
		corpus = append(corpus, p.Label)
	}
	rng := rand.New(rand.NewSource(7))
	check := func(s string) {
		if got, want := ScanInjection(s), scanInjectionReference(s); !sameResults(got, want) {
			t.Fatalf("verdict differs for %q:\n got %v\nwant %v", s, got, want)
		}
	}
	for _, s := range corpus {
		check(s)
		check(strings.ToUpper(s))
		// Random mutations: splice corpus entries, drop or duplicate a span.
		for i := 0; i < 3; i++ {
			o := corpus[rng.Intn(len(corpus))]
			if len(s) > 0 {
				a := rng.Intn(len(s) + 1)
				b := a + rng.Intn(len(s)-a+1)
				check(s[:a] + " " + o + " " + s[b:])
				check(s[:b] + s[a:])
			}
		}
	}
}

func TestRED_Injection_EveryPatternHasRequiredLiterals(t *testing.T) {
	missing := 0
	for i, p := range injectionPatterns {
		if len(injectionLiterals[i]) == 0 {
			missing++
			t.Logf("no literal for %q", p.Label)
		}
	}
	if missing > 0 {
		t.Fatalf("%d patterns have no literal prefilter", missing)
	}
}

// The prefiltered scan must beat running every pattern unconditionally by a
// wide margin. The comparison is relative so it holds under -race and on a
// loaded CI runner, where absolute wall-clock bounds do not.
func TestRED_Injection_LargeCleanScanIsFast(t *testing.T) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. func main() { return 42 }\n", 256*1024/64)
	normalized := NormalizeForScan(text)
	start := time.Now()
	for _, p := range injectionPatterns {
		if p.Re.MatchString(normalized) {
			t.Fatalf("pattern %q matched clean text", p.Label)
		}
	}
	unfiltered := time.Since(start)
	start = time.Now()
	if r := ScanInjection(text); r != nil {
		t.Fatalf("unexpected results: %v", r)
	}
	filtered := time.Since(start)
	if filtered*3 > unfiltered {
		t.Fatalf("prefiltered scan took %v vs %v unfiltered, want at least 3x faster", filtered, unfiltered)
	}
}

func BenchmarkScanInjection(b *testing.B) {
	for _, n := range []int{1 << 10, 256 << 10} {
		text := strings.Repeat("The quick brown fox jumps over the lazy dog. func main() { return 42 }\n", n/64+1)[:n]
		b.Run("prefilter/"+strconv.Itoa(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ScanInjection(text)
			}
		})
		b.Run("reference/"+strconv.Itoa(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scanInjectionReference(text)
			}
		})
	}
}
