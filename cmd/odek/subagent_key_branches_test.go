package main

import (
	"os"
	"runtime"
	"testing"
)

// TestReadKeyFromInheritedFD_InvalidFDStringReturnsEmpty verifies the
// non-numeric env value path: parser fails, function returns "" rather
// than misinterpreting the value.
func TestReadKeyFromInheritedFD_InvalidFDStringReturnsEmpty(t *testing.T) {
	t.Setenv(keyFDEnvVar, "not-a-number")
	if got := readKeyFromInheritedFD(); got != "" {
		t.Errorf("expected empty string for non-numeric FD, got %q", got)
	}
	if v := os.Getenv(keyFDEnvVar); v != "" {
		t.Errorf("env var should be unset after the call, still has %q", v)
	}
}

// TestReadKeyFromInheritedFD_RejectsLowFDNumbers ensures FD numbers
// below 3 (stdin/stdout/stderr) are refused — the parent only ever
// hands keys through inherited extras, never the standard streams.
func TestReadKeyFromInheritedFD_RejectsLowFDNumbers(t *testing.T) {
	for _, fd := range []string{"0", "1", "2"} {
		t.Run("fd="+fd, func(t *testing.T) {
			t.Setenv(keyFDEnvVar, fd)
			if got := readKeyFromInheritedFD(); got != "" {
				t.Errorf("expected empty string when FD=%s, got %q", fd, got)
			}
		})
	}
}

// TestReadKeyFromInheritedFD_UnsetsEnvVarBeforeRead asserts that the
// signal env var is removed even on the early-return path (invalid
// number), so grandchildren can't accidentally inherit and re-read it.
func TestReadKeyFromInheritedFD_UnsetsEnvVarBeforeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FD inheritance via ExtraFiles is POSIX-specific")
	}
	t.Setenv(keyFDEnvVar, "0") // valid number but below floor — function returns ""
	if got := readKeyFromInheritedFD(); got != "" {
		t.Errorf("expected empty string when FD=0, got %q", got)
	}
	if v := os.Getenv(keyFDEnvVar); v != "" {
		t.Errorf("env var should be unset after early return, still has %q", v)
	}
}

// TestWriteKeyToUnlinkedFile_EmptyKeyStillProducesValidFD covers the
// degenerate but valid case of an empty key — the function should still
// hand back a readable FD (containing zero bytes) and unlink the file.
func TestWriteKeyToUnlinkedFile_EmptyKeyStillProducesValidFD(t *testing.T) {
	f, err := writeKeyToUnlinkedFile("")
	if err != nil {
		t.Fatalf("writeKeyToUnlinkedFile(\"\"): %v", err)
	}
	defer f.Close()
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	if n != 0 {
		t.Errorf("expected 0 bytes for empty key, got %d (%q)", n, string(buf[:n]))
	}
}
