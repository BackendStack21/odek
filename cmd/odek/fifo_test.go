package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// callWithin runs fn and fails the test if it does not return in d. The
// fifo is opened for writing afterwards to release a blocked open(2).
func redFifoCall(t *testing.T, fifo string, fn func() string) {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- fn() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// release the blocked reader so the goroutine does not leak
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		t.Fatalf("tool blocked forever in open(2) on a FIFO in the workspace (agent wedged, no timeout/ctx)")
	}
}

func redMkfifo(t *testing.T) string {
	dir := t.TempDir()
	p := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip(err)
	}
	return p
}

func TestRED_ReadFileBlocksOnFIFO(t *testing.T) {
	p := redMkfifo(t)
	args, _ := json.Marshal(map[string]string{"path": p})
	redFifoCall(t, p, func() string {
		out, _ := (&readFileTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
		return out
	})
}

func TestRED_HeadTailBlocksOnFIFO(t *testing.T) {
	p := redMkfifo(t)
	args, _ := json.Marshal(map[string]string{"path": p})
	redFifoCall(t, p, func() string {
		out, _ := (&headTailTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
		return out
	})
}

func TestRED_ChecksumBlocksOnFIFO(t *testing.T) {
	p := redMkfifo(t)
	args, _ := json.Marshal(map[string]string{"path": p})
	redFifoCall(t, p, func() string {
		out, _ := (&checksumTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
		return out
	})
}

// A single FIFO anywhere under a searched tree wedges the whole search.
func TestRED_SearchFilesBlocksOnFIFOInTree(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip(err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o644)
	args, _ := json.Marshal(map[string]string{"pattern": "needle", "path": dir})
	redFifoCall(t, p, func() string {
		out, _ := (&searchFilesTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
		return out
	})
}

func TestOpenRegularNoFollow(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "f.txt")
	os.WriteFile(reg, []byte("x"), 0o644)
	fifo := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip(err)
	}
	link := filepath.Join(dir, "l")
	os.Symlink(reg, link)

	if f, err := openRegularNoFollow(reg); err != nil {
		t.Fatalf("regular: %v", err)
	} else {
		f.Close()
	}
	if f, err := openRegularNoFollow(dir); err != nil {
		t.Fatalf("dir: %v", err)
	} else {
		f.Close()
	}
	if _, err := openRegularNoFollow(fifo); err == nil {
		t.Fatal("fifo must be refused")
	}
	if _, err := openRegularNoFollow(link); err == nil {
		t.Fatal("symlink must be refused")
	}
	if _, err := openRegularNoFollow(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing must error")
	}
}

func TestPatchRefusesFIFO(t *testing.T) {
	p := redMkfifo(t)
	args, _ := json.Marshal(map[string]any{"path": p, "old_string": "a", "new_string": "b"})
	redFifoCall(t, p, func() string {
		out, _ := (&patchTool{dangerousConfig: danger.DangerousConfig{}}).Call(string(args))
		return out
	})
}
