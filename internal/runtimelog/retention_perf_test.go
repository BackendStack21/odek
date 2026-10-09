package runtimelog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRED_Runtimelog_PruneNoExpiredDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	now := time.Now()
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, `{"timestamp":%q,"msg":"m%d"}`+"\n", now.Format(time.RFC3339Nano), i)
	}
	b.WriteString("not json\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	creates := 0
	orig := createPruneTemp
	createPruneTemp = func(d, p string) (*os.File, error) { creates++; return orig(d, p) }
	defer func() { createPruneTemp = orig }()
	n, err := Prune(context.Background(), path, now.Add(-time.Hour), false)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if creates != 0 {
		t.Fatalf("temp file created %d times with nothing expired", creates)
	}
}

func TestPruneRewriteKeepsRetainedAndOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	now := time.Now()
	old := now.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	fresh := now.Format(time.RFC3339Nano)
	big := strings.Repeat("x", maxPruneLine+10)
	content := fmt.Sprintf(`{"timestamp":%q}`+"\n%s\n"+`{"timestamp":%q}`+"\nmalformed\n", fresh, big, old)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cut := now.Add(-time.Hour)
	if n, err := Prune(context.Background(), path, cut, true); err != nil || n != 1 {
		t.Fatalf("preview n=%d err=%v", n, err)
	}
	n, err := Prune(context.Background(), path, cut, false)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := os.ReadFile(path)
	want := fmt.Sprintf(`{"timestamp":%q}`+"\n%s\nmalformed\n", fresh, big)
	if string(got) != want {
		t.Fatalf("retained content mismatch (len %d vs %d)", len(got), len(want))
	}
}
