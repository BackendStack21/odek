package resource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// referenceWalk is the unbounded collect-then-sort behaviour walkAndMatch
// must stay equivalent to for the entries it returns.
func referenceWalk(root, term string) []string {
	var all []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(rel, term) {
			all = append(all, path)
		}
		return nil
	})
	sort.SliceStable(all, func(i, j int) bool { return len(all[i]) < len(all[j]) })
	return all
}

func TestRED_Resource_WalkAndMatchBoundedTopK(t *testing.T) {
	root := t.TempDir()
	for d := 0; d < 6; d++ {
		dir := filepath.Join(root, fmt.Sprintf("d%d", d), "nested")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			name := fmt.Sprintf("file_%s_%d.txt", strings.Repeat("x", i%7), i)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	f := NewFileResolver(root)
	want := referenceWalk(root, "file_")
	got := f.walkAndMatch(context.Background(), "file_", 5)
	if len(got) > 16 {
		t.Fatalf("window not bounded: %d", len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("rank %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestRED_Resource_WalkAndMatchHonoursContext(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("m%d.txt", i)), []byte("x"), 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := NewFileResolver(root).walkAndMatch(ctx, "m", 10); len(got) != 0 {
		t.Fatalf("cancelled walk returned %d results", len(got))
	}
}

func TestRelSuffix(t *testing.T) {
	sep := string(filepath.Separator)
	cases := [][3]string{
		{"/a/b", "/a/b" + sep + "c" + sep + "d", "c" + sep + "d"},
		{"/a/b" + sep, "/a/b" + sep + "c", "c"},
		{".", "c" + sep + "d", "c" + sep + "d"},
		{"/a/b", "/a/bc/d", "../bc/d"},
	}
	for _, c := range cases {
		want, _ := filepath.Rel(c[0], c[1])
		if got := relSuffix(c[0], c[1]); got != want {
			t.Errorf("relSuffix(%q,%q)=%q want %q", c[0], c[1], got, want)
		}
	}
}
