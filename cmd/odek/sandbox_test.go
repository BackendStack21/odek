package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/sandbox"
)

func TestInjectFilesToSandbox_FileNotFound(t *testing.T) {
	// Non-existent file should log a warning and count as skipped
	tDir := t.TempDir()
	stderr := captureStderr(t)
	count, err := sandbox.InjectFiles("nonexistent-container", []string{"missing.txt"}, tDir)
	if err != nil {
		t.Logf("docker cp error (expected): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 injected for missing file, got %d", count)
	}
	stderrOut := stderr()
	if !strings.Contains(stderrOut, "warning") || !strings.Contains(stderrOut, "missing.txt") {
		t.Errorf("expected warning about missing file, got: %s", stderrOut)
	}
}

func TestInjectFilesToSandbox_Directory(t *testing.T) {
	// Directory should log a warning and be skipped
	tDir := t.TempDir()
	subDir := filepath.Join(tDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t)
	count, err := sandbox.InjectFiles("nonexistent-container", []string{"subdir"}, tDir)
	if err != nil {
		t.Logf("docker cp error (expected): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 injected for directory, got %d", count)
	}
	stderrOut := stderr()
	if !strings.Contains(stderrOut, "directory") {
		t.Errorf("expected warning about directory, got: %s", stderrOut)
	}
}

func TestInjectFilesToSandbox_EmptyList(t *testing.T) {
	count, err := sandbox.InjectFiles("any", nil, "/tmp")
	if err != nil {
		t.Errorf("unexpected error for empty list: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for empty list, got %d", count)
	}

	count, err = sandbox.InjectFiles("any", []string{}, "/tmp")
	if err != nil {
		t.Errorf("unexpected error for empty slice: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for empty slice, got %d", count)
	}
}

func TestInjectFilesToSandbox_DockerCpFails(t *testing.T) {
	// File exists but container doesn't — docker cp should fail
	tDir := t.TempDir()
	testFile := filepath.Join(tDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	count, err := sandbox.InjectFiles("container-does-not-exist-abc123", []string{"test.txt"}, tDir)
	if err == nil {
		t.Error("expected error for docker cp to non-existent container")
	}
	if count != 0 {
		t.Errorf("expected 0 injected when docker cp fails, got %d", count)
	}
	if !strings.Contains(err.Error(), "docker cp") {
		t.Errorf("expected docker cp error, got: %v", err)
	}
}

// captureStderr returns a function that reads stderr output written
// during the test. It redirects os.Stderr to a pipe during the test.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	return func() string {
		os.Stderr = orig
		w.Close()
		var buf strings.Builder
		// Read from pipe
		b := make([]byte, 1024)
		for {
			n, err := r.Read(b)
			if n > 0 {
				buf.Write(b[:n])
			}
			if err != nil {
				break
			}
		}
		return buf.String()
	}
}
