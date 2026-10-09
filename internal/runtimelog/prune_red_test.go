package runtimelog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One oversized garbage line must not disable retention for the whole file:
// logquery treats it as a malformed record and moves on, Prune must too.
func TestRED_PruneBlockedByOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	old := `{"timestamp":"2020-01-01T00:00:00Z","event":"old"}` + "\n"
	fresh := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","event":"fresh"}` + "\n"
	junk := strings.Repeat("x", 2<<20) + "\n"
	if err := os.WriteFile(path, []byte(old+junk+fresh), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(context.Background(), path, time.Now().Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("Prune failed on one oversized line, so no expired record is ever removed: %v", err)
	}
	if n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
}

func TestPrune_OversizedLineKeptVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	old := `{"timestamp":"2020-01-01T00:00:00Z","event":"old"}` + "\n"
	fresh := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","event":"fresh"}` + "\n"
	junk := strings.Repeat("x", 3<<20)
	if err := os.WriteFile(path, []byte(old+junk+"\n"+fresh+junk), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(context.Background(), path, time.Now().Add(-time.Hour), false)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := os.ReadFile(path)
	want := junk + "\n" + fresh + junk + "\n"
	if string(got) != want {
		t.Fatalf("rewritten file differs: len %d want %d", len(got), len(want))
	}
}

func TestPrune_OversizedLinePreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	old := `{"timestamp":"2020-01-01T00:00:00Z","event":"old"}` + "\n"
	junk := strings.Repeat("y", 2<<20) + "\n"
	if err := os.WriteFile(path, []byte(junk+old), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(context.Background(), path, time.Now().Add(-time.Hour), true)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestPrune_CancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	if err := os.WriteFile(path, []byte(`{"timestamp":"2020-01-01T00:00:00Z"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prune(ctx, path, time.Now(), false); err == nil {
		t.Fatal("expected context error")
	}
}

func TestPrune_LastLineWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	old := `{"timestamp":"2020-01-01T00:00:00Z"}` + "\n"
	keep := `{"event":"keep"}`
	if err := os.WriteFile(path, []byte(old+keep), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(context.Background(), path, time.Now().Add(-time.Hour), false)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(path); string(b) != keep+"\n" {
		t.Fatalf("got %q", b)
	}
}
