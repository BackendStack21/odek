package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrichTask_NoRefsOrCtx(t *testing.T) {
	result, err := enrichTask("hello world", nil, "/tmp")
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

	result, err := enrichTask("@hello.txt what does this say?", nil, dir)
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

	result, err := enrichTask("analyze this", []string{"data.txt"}, dir)
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
	_, err := enrichTask("analyze this", []string{"nonexistent.txt"}, t.TempDir())
	if err == nil {
		t.Fatal("expected error for nonexistent ctx file")
	}
}

func TestEnrichTask_WithBothCtxAndAtRef(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main content"), 0644)
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("lib content"), 0644)

	result, err := enrichTask("@lib.txt compare with ctx", []string{"main.txt"}, dir)
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
