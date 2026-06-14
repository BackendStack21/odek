package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

// writeKeyToUnlinkedFile writes the API key to a 0600 temp file and
// immediately unlinks it. The returned *os.File still holds a valid FD
// — POSIX guarantees the inode survives as long as a process holds an
// open descriptor — so the parent can hand it to a child via
// cmd.ExtraFiles without ever leaving a readable file on disk.
//
// On Windows we cannot unlink an open file, so we fall back to a
// 0600 file in the user's TempDir. The returned cleanup function must be
// called after the child has exited and the file has been closed; on
// Windows it deletes the temp file, and on POSIX it is a no-op (the file
// was already unlinked).
func writeKeyToUnlinkedFile(key string) (*os.File, func(), error) {
	f, err := os.CreateTemp("", "odek-key-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp: %w", err)
	}

	cleanup := func() {
		// POSIX: file was already unlinked, nothing to do.
		// Windows: delete the temp file after the child has exited and
		// the parent's handle has been closed.
		if runtime.GOOS == "windows" {
			_ = os.Remove(f.Name())
		}
	}

	if err := f.Chmod(0600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("chmod: %w", err)
	}
	if _, err := f.WriteString(key); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("write: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("seek: %w", err)
	}
	// Unlink immediately on POSIX. The open FD keeps the inode alive
	// for the parent and the soon-to-be-forked child.
	if runtime.GOOS != "windows" {
		if err := os.Remove(f.Name()); err != nil {
			// Not fatal — file exists with 0600 and we still hold the
			// FD, but log via the error so callers can decide.
			fmt.Fprintf(os.Stderr, "odek: warning: could not unlink key tempfile: %v\n", err)
		}
	}
	return f, cleanup, nil
}

// keyFDEnvVar is the signal env var the parent sets when it passes an
// API key via an inherited file descriptor. The value is the FD number
// (currently always "3"). The env var carries no secret — only the
// instruction to read from a specific FD — so it is safe to leave in
// the child's environment.
const keyFDEnvVar = "ODEK_API_KEY_FD"

// readKeyFromInheritedFD reads the API key the parent passed via the
// FD identified by $ODEK_API_KEY_FD. Returns "" if the env var is not
// set (parent did not opt into the handoff — child should fall back to
// its own config-resolution chain).
//
// We require an explicit env signal rather than blindly reading FD 3
// because in many runtime contexts (Go test harness, certain shells)
// FD 3 is already wired to something the test framework cares about.
//
// The FD is closed before this function returns so the key bytes only
// exist on the heap for as long as we hold the string. The env var is
// also unset to keep it out of any subsequent child's environment.
func readKeyFromInheritedFD() string {
	fdStr := os.Getenv(keyFDEnvVar)
	if fdStr == "" {
		return ""
	}
	// Don't pass the signal through to grandchildren.
	os.Unsetenv(keyFDEnvVar)

	var fd int
	if _, err := fmt.Sscanf(fdStr, "%d", &fd); err != nil || fd < 3 {
		return ""
	}
	f := os.NewFile(uintptr(fd), "odek-key-fd")
	if f == nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return ""
	}
	for n > 0 && (buf[n-1] == '\n' || buf[n-1] == '\r' || buf[n-1] == ' ' || buf[n-1] == '\t') {
		n--
	}
	return string(buf[:n])
}
