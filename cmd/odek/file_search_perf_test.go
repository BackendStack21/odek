package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

func makeMtimeFiles(t *testing.T, dir string, n int) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%03d.txt", i))
		if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Distinct, shuffled mtimes (with a few ties).
		mt := base.Add(time.Duration((i*37)%n/2) * time.Second)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
}

func countStats(t *testing.T) *int64 {
	t.Helper()
	var n int64
	os0, ol0 := sortStat, sortLstat
	sortStat = func(p string) (os.FileInfo, error) { atomic.AddInt64(&n, 1); return os0(p) }
	sortLstat = func(p string) (os.FileInfo, error) { atomic.AddInt64(&n, 1); return ol0(p) }
	t.Cleanup(func() { sortStat, sortLstat = os0, ol0 })
	return &n
}

func TestRED_Glob_SortStatsEachFileOnce(t *testing.T) {
	dir := t.TempDir()
	const n = 200
	makeMtimeFiles(t, dir, n)
	calls := countStats(t)
	tool := &globTool{}
	if _, err := tool.Call(fmt.Sprintf(`{"path":%q,"pattern":"*.txt","limit":500}`, dir)); err != nil {
		t.Fatal(err)
	}
	if *calls > n {
		t.Fatalf("glob sort issued %d stat calls for %d files; want at most %d", *calls, n, n)
	}
}

func TestRED_SearchFilesTargetFiles_SortStatsEachFileOnce(t *testing.T) {
	dir := t.TempDir()
	const n = 200
	makeMtimeFiles(t, dir, n)
	calls := countStats(t)
	tool := &searchFilesTool{}
	if _, err := tool.Call(fmt.Sprintf(`{"path":%q,"pattern":"*.txt","target":"files","limit":500}`, dir)); err != nil {
		t.Fatal(err)
	}
	if *calls > n {
		t.Fatalf("search_files sort issued %d stat calls for %d files; want at most %d", *calls, n, n)
	}
}

// The ordering must be newest first with the path as tie-break only when a
// file cannot be stat'ed; check the order matches a plain mtime sort.
func TestGlobSearchOrderIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	names := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
	for i, nm := range names {
		p := filepath.Join(dir, nm)
		os.WriteFile(p, []byte("needle\n"), 0o644)
		mt := base.Add(time.Duration((i*3)%4) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	// mtimes: a=0 b=3 c=2 d=1 minutes => b, c, d, a
	want := []string{"b.txt", "c.txt", "d.txt", "a.txt"}
	for _, tc := range []struct {
		tool interface{ Call(string) (string, error) }
		args string
	}{
		{&globTool{}, fmt.Sprintf(`{"path":%q,"pattern":"*.txt"}`, dir)},
		{&searchFilesTool{}, fmt.Sprintf(`{"path":%q,"pattern":"*.txt","target":"files"}`, dir)},
	} {
		out, err := tc.tool.Call(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Matches []struct{ Path string } `json:"matches"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range r.Matches {
			got = append(got, filepath.Base(unwrapUntrusted(m.Path)))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestRED_SearchContent_FileGlobFiltersBeforeClassification(t *testing.T) {
	home := makeTestHomeDir(t)
	t.Setenv("HOME", home)
	odekDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(odekDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(odekDir, "config.json"), []byte("secret needle\n"), 0o644)
	os.WriteFile(filepath.Join(odekDir, "notes.md"), []byte("needle\n"), 0o644)
	run := func(glob string) int {
		ap := &countingApprover{allowFirstN: 100}
		cfg := danger.DangerousConfig{
			Classes:  map[danger.RiskClass]danger.Action{danger.SystemWrite: danger.Prompt},
			Approver: ap,
		}
		tool := &searchFilesTool{dangerousConfig: cfg}
		if _, err := tool.Call(fmt.Sprintf(`{"pattern":"needle","target":"content","file_glob":%q,"path":%q}`, glob, odekDir)); err != nil {
			t.Fatal(err)
		}
		return ap.calls
	}
	all, md := run("*"), run("*.md")
	// The non-matching config.json must not be classified when the glob
	// excludes it.
	if md >= all {
		t.Fatalf("classification count with *.md = %d, with * = %d; the excluded file must not be classified", md, all)
	}
}

func TestRED_SearchContent_DoesNotAllocateScannerBufferPerFile(t *testing.T) {
	dir := t.TempDir()
	const n = 300
	for i := 0; i < n; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), []byte("hello\nworld\n"), 0o644)
	}
	tool := &searchFilesTool{}
	args := fmt.Sprintf(`{"pattern":"nomatch","target":"content","path":%q}`, dir)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := tool.Call(args); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Fatalf("searching %d tiny files allocated %d MiB; want under 64", n, alloc>>20)
	}
}
