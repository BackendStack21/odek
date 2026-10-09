package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"testing"
)

// computeDiffReference is the original dense-LCS implementation, kept so the
// optimised computeDiff can be checked for identical output.
func computeDiffReference(a, b []string) []diffHunk {
	m, n := len(a), len(b)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				lcs[i][j] = lcs[i-1][j-1] + 1
			} else if lcs[i-1][j] >= lcs[i][j-1] {
				lcs[i][j] = lcs[i-1][j]
			} else {
				lcs[i][j] = lcs[i][j-1]
			}
		}
	}

	var hunks []diffHunk
	i, j := m, n

	var equalLines, addedLines, removedLines []diffLine

	flushHunk := func() {
		if len(equalLines) > 0 {
			hunks = append(hunks, diffHunk{Type: "equal", Lines: equalLines})
			equalLines = nil
		}
		if len(removedLines) > 0 {
			hunks = append(hunks, diffHunk{Type: "removed", Lines: removedLines})
			removedLines = nil
		}
		if len(addedLines) > 0 {
			hunks = append(hunks, diffHunk{Type: "added", Lines: addedLines})
			addedLines = nil
		}
	}

	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			flushHunk()
			equalLines = append([]diffLine{{OldLine: i, NewLine: j, Content: a[i-1]}}, equalLines...)
			i--
			j--
		} else if j > 0 && (i == 0 || lcs[i][j-1] >= lcs[i-1][j]) {
			addedLines = append([]diffLine{{NewLine: j, Content: b[j-1]}}, addedLines...)
			j--
		} else if i > 0 {
			removedLines = append([]diffLine{{OldLine: i, Content: a[i-1]}}, removedLines...)
			i--
		}
	}
	flushHunk()

	for i, k := 0, len(hunks)-1; i < k; i, k = i+1, k-1 {
		hunks[i], hunks[k] = hunks[k], hunks[i]
	}
	return hunks
}

func TestComputeDiff_MatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	check := func(a, b []string) {
		t.Helper()
		got, want := computeDiff(a, b), computeDiffReference(a, b)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("diff mismatch\na=%q\nb=%q\ngot =%+v\nwant=%+v", a, b, got, want)
		}
	}
	// Hand-picked tie cases: repeated lines make the LCS ambiguous.
	check(nil, nil)
	check([]string{"x"}, nil)
	check(nil, []string{"x"})
	check([]string{"x", "x"}, []string{"x"})
	check([]string{"x"}, []string{"x", "x"})
	check([]string{"a", "x", "x"}, []string{"a", "x"})
	check([]string{"x", "y", "x"}, []string{"x", "x"})
	check([]string{"a", "b", "c"}, []string{"a", "b", "c"})
	check([]string{"a", "b", "c"}, []string{"c", "b", "a"})
	// Random corpus over tiny alphabets (many ties, shared prefixes/suffixes).
	for iter := 0; iter < 4000; iter++ {
		alpha := 1 + rng.Intn(4)
		gen := func() []string {
			l := rng.Intn(14)
			s := make([]string, l)
			for i := range s {
				s[i] = string(rune('a' + rng.Intn(alpha)))
			}
			return s
		}
		a := gen()
		var b []string
		if rng.Intn(2) == 0 {
			// Mutate a: keeps long common prefix/suffix.
			b = append([]string(nil), a...)
			for k := rng.Intn(3); k >= 0 && len(b) > 0; k-- {
				pos := rng.Intn(len(b))
				switch rng.Intn(3) {
				case 0:
					b = append(b[:pos], b[pos+1:]...)
				case 1:
					b = append(b[:pos], append([]string{string(rune('a' + rng.Intn(alpha)))}, b[pos:]...)...)
				default:
					b[pos] = string(rune('a' + rng.Intn(alpha)))
				}
			}
		} else {
			b = gen()
		}
		check(a, b)
	}
}

func TestRED_ComputeDiff_NearIdenticalFilesStayCheap(t *testing.T) {
	const n = 3000
	a := make([]string, n)
	b := make([]string, n)
	for i := range a {
		a[i] = fmt.Sprintf("line %d", i)
		b[i] = a[i]
	}
	b[n/2] = "changed"
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	hunks := computeDiff(a, b)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
		t.Fatalf("diff of near-identical %d-line files allocated %d MiB; want under 8", n, alloc>>20)
	}
	if !reflect.DeepEqual(hunks, computeDiffReference(a, b)) {
		t.Fatal("output differs from reference")
	}
}

func TestComputeDiff_LongRunOfAdditionsIsLinear(t *testing.T) {
	a := []string{"head", "tail"}
	b := []string{"head"}
	for i := 0; i < 2000; i++ {
		b = append(b, fmt.Sprintf("new %d", i))
	}
	b = append(b, "tail")
	if !reflect.DeepEqual(computeDiff(a, b), computeDiffReference(a, b)) {
		t.Fatal("output differs from reference")
	}
}
