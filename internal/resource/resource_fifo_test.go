package resource

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0600)
}

// A FIFO (or any non-regular file) in the workspace must not hang or
// over-read @-resource loads: the open was blocking (no writer → forever,
// ctx ignored), and a FIFO's zero size passed the size gate leaving the
// subsequent ReadAll unbounded.
func TestFileResolver_Load_FIFODoesNotHang(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	r := NewFileResolver(dir)
	type res struct {
		content string
		err     error
	}
	done := make(chan res, 1)
	var once sync.Once
	go func() {
		content, err := r.Load(context.Background(), "pipe")
		once.Do(func() { done <- res{content, err} })
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("FIFO load unexpectedly succeeded: %d bytes", len(got.content))
		}
	case <-time.After(3 * time.Second):
		once.Do(func() {})
		t.Fatal("Load on a writerless FIFO hung — non-regular files must be rejected without blocking")
	}
}
