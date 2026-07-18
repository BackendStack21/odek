package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
)

// withTestIngestRecorder attaches a simple recorder to ctx for testing
// enrichTask's ingest logging.
func withTestIngestRecorder(ctx context.Context, recorder func(source, content string)) context.Context {
	return loop.WithIngestRecorder(ctx, recorder)
}

func TestEnrichTask_NoRefsOrCtx(t *testing.T) {
	result, err := enrichTask(context.Background(), "hello world", nil, "/tmp")
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}
	if result != "hello world" {
		t.Errorf("expected unchanged task, got %q", result)
	}
}

func TestEnrichTask_WithAtRef(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hello.txt")
	os.WriteFile(src, []byte("Hello, World!"), 0644)

	result, err := enrichTask(context.Background(), "@hello.txt what does this say?", nil, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}

	if !strings.Contains(result, "Hello, World!") {
		t.Errorf("expected file content in result, got %q", result)
	}
	if !strings.Contains(result, "@hello.txt") {
		t.Errorf("expected @reference in result header, got %q", result)
	}
}

func TestEnrichTask_WithCtxFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "data.txt")
	os.WriteFile(src, []byte("file content"), 0644)

	result, err := enrichTask(context.Background(), "analyze this", []string{"data.txt"}, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}

	if !strings.Contains(result, "file content") {
		t.Errorf("expected ctx file content in result, got %q", result)
	}
	if !strings.HasPrefix(result, "--- data.txt ---") {
		t.Errorf("expected ctx block header at start, got %q", result)
	}
	if !strings.Contains(result, "analyze this") {
		t.Errorf("expected task at end, got %q", result)
	}
}

func TestEnrichTask_CtxFileNotFound(t *testing.T) {
	_, err := enrichTask(context.Background(), "analyze this", []string{"nonexistent.txt"}, t.TempDir())
	if err == nil {
		t.Fatal("expected error for nonexistent ctx file")
	}
}

func TestEnrichTask_WithBothCtxAndAtRef(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main content"), 0644)
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("lib content"), 0644)

	result, err := enrichTask(context.Background(), "@lib.txt compare with ctx", []string{"main.txt"}, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}

	if !strings.Contains(result, "main content") {
		t.Errorf("expected ctx file content: %q", result)
	}
	if !strings.Contains(result, "lib content") {
		t.Errorf("expected @ref file content: %q", result)
	}
}

func TestEnrichTask_RecordsIngestWhenContextCarriesRecorder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("secret data"), 0644)

	var recorded []struct{ source, content string }
	recorder := func(source, content string) {
		recorded = append(recorded, struct{ source, content string }{source, content})
	}
	ctx := withTestIngestRecorder(context.Background(), recorder)

	_, err := enrichTask(ctx, "read @note.txt", nil, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}

	if len(recorded) != 1 {
		t.Fatalf("expected 1 recorded ingest, got %d", len(recorded))
	}
	if recorded[0].source != "resource:@note.txt" {
		t.Errorf("source = %q, want resource:@note.txt", recorded[0].source)
	}
	if recorded[0].content != "secret data" {
		t.Errorf("content = %q, want secret data", recorded[0].content)
	}
}

func TestEnrichTask_RecordsCtxFileIngest(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ctx.txt"), []byte("ctx body"), 0644)

	var recorded []struct{ source, content string }
	recorder := func(source, content string) {
		recorded = append(recorded, struct{ source, content string }{source, content})
	}
	ctx := withTestIngestRecorder(context.Background(), recorder)

	_, err := enrichTask(ctx, "analyze", []string{"ctx.txt"}, dir)
	if err != nil {
		t.Fatalf("enrichTask error: %v", err)
	}

	if len(recorded) != 1 {
		t.Fatalf("expected 1 recorded ingest, got %d", len(recorded))
	}
	if recorded[0].source != "ctx:ctx.txt" {
		t.Errorf("source = %q, want ctx:ctx.txt", recorded[0].source)
	}
	if recorded[0].content != "ctx body" {
		t.Errorf("content = %q, want ctx body", recorded[0].content)
	}
}

func TestParseRunFlags_CtxFlag(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--ctx", "main.go,lib.go",
		"analyze these files",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if len(f.Ctx) != 2 {
		t.Fatalf("expected 2 ctx files, got %d: %v", len(f.Ctx), f.Ctx)
	}
	if f.Ctx[0] != "main.go" {
		t.Errorf("Ctx[0] = %q, want %q", f.Ctx[0], "main.go")
	}
	if f.Ctx[1] != "lib.go" {
		t.Errorf("Ctx[1] = %q, want %q", f.Ctx[1], "lib.go")
	}
	if f.Task != "analyze these files" {
		t.Errorf("Task = %q, want %q", f.Task, "analyze these files")
	}
}

func TestParseRunFlags_CtxShortFlag(t *testing.T) {
	f, err := parseRunFlags([]string{
		"-c", "data.csv",
		"analyze",
	})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if len(f.Ctx) != 1 || f.Ctx[0] != "data.csv" {
		t.Errorf("Ctx = %v, want [data.csv]", f.Ctx)
	}
	if f.Task != "analyze" {
		t.Errorf("Task = %q, want %q", f.Task, "analyze")
	}
}
