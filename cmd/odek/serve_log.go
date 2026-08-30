package main

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// serveFileLog is odek serve's durable run/turn log. Provider failures (429
// saturation, outages) used to be visible nowhere: the turn was dropped and
// the only trace was a missing reply in the UI (2026-08-29 incidents). The
// log makes every serve turn and headless run auditable after the fact.
//
// The default path (~/.odek/serve.log) is a sibling of telegram.log and
// schedule.log so the storage janitor's rotation covers it. Lines carry IDs,
// statuses, and short failure classifications — never prompt or completion
// content.
type serveFileLog struct {
	mu sync.Mutex
	f  *os.File
}

var (
	serveLogMu     sync.Mutex
	serveLogActive *serveFileLog
)

// openServeLog opens (creating if needed) the log at path with
// operator-only permissions. A symlink at the path is rejected: the Lstat
// pre-check catches an existing symlink and O_NOFOLLOW closes the
// check-then-open race where the symlink is swapped in between (same defense
// as the events JSONL sink).
func openServeLog(path string) (*serveFileLog, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to write serve log to symlink %s", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	// Harden a pre-existing file that was created with looser permissions.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &serveFileLog{f: f}, nil
}

// setServeLog installs the process-wide serve log (nil disables logging).
func setServeLog(l *serveFileLog) {
	serveLogMu.Lock()
	serveLogActive = l
	serveLogMu.Unlock()
}

// activeServeLog returns the currently installed serve log, if any.
func activeServeLog() *serveFileLog {
	serveLogMu.Lock()
	defer serveLogMu.Unlock()
	return serveLogActive
}

// serveLogf appends one timestamped line to the active serve log, if any.
// Never panics, never blocks the request path on I/O errors beyond the
// single write.
func serveLogf(format string, args ...any) {
	serveLogMu.Lock()
	l := serveLogActive
	serveLogMu.Unlock()
	if l == nil {
		return
	}
	l.logf(format, args...)
}

func (l *serveFileLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// Close releases the underlying file.
func (l *serveFileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
