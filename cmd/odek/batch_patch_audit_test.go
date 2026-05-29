package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests verify the high-severity correctness findings against the
// batch_patch tool documented in IMPROVEMENTS_ROADMAP.md (B-H1, B-H2).
// They are expected to FAIL on the current code and to pass once the
// underlying fixes land. Each test documents the exact line in
// cmd/odek/perf_tools.go that needs to change.

type bpResult struct {
	Results []struct {
		Path    string `json:"path"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Diff    string `json:"diff"`
	} `json:"results"`
}

// TestBatchPatch_PreservesFileMode verifies finding B-H2:
// perf_tools.go:192 unconditionally chmod()s the temp file to 0644,
// so a patch to a 0600 secrets file widens it to world-readable.
//
// EXPECTED TO FAIL today; will pass after the tool Stat()s the original
// and reapplies its mode.
func TestBatchPatch_PreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix mode bits not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(path, []byte("TOKEN=abc\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Defensive: confirm umask did not strip the bits we set.
	if st, _ := os.Stat(path); st.Mode().Perm() != 0600 {
		t.Fatalf("setup: file mode = %v, want 0600", st.Mode().Perm())
	}

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[{"path":%q,"old_string":"abc","new_string":"xyz"}]}`, path)
	out := callJSON(t, tool, args)

	var r bpResult
	mustUnmarshal(t, out, &r)
	if len(r.Results) != 1 || !r.Results[0].Success {
		t.Fatalf("patch failed: %+v", r.Results)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0600 {
		t.Errorf("file mode after patch = %#o, want 0600 (perf_tools.go:192 hardcodes 0644; preserve original mode)", mode)
	}
}

// TestBatchPatch_FailsLoudlyOnWriteError verifies finding B-H1:
// perf_tools.go:191 discards the error returned by tmpFile.Write, so a
// truncated write is then atomically renamed over the target — silent
// file corruption.
//
// We can't easily make os.File.Write fail in-process, so we drive the
// write-error path through the next-most-deterministic failure mode the
// same code path already supports: an unwritable directory. The tmp
// file creation will fail (line 184) and the Error field MUST be
// populated AND the original file MUST be untouched. This pins down
// the contract; the B-H1 fix is to add the same contract to the
// Write() error path on line 191.
func TestBatchPatch_AbortsOnTempFailureAndPreservesOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	original := "hello world\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Make the directory unwritable so CreateTemp() fails — this exercises
	// the same "abort before rename, preserve target" contract that the
	// Write() error path should follow once B-H1 is fixed.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	defer os.Chmod(dir, 0755) //nolint:errcheck // best-effort cleanup

	tool := &batchPatchTool{}
	args := fmt.Sprintf(`{"patches":[{"path":%q,"old_string":"hello","new_string":"bye"}]}`, path)
	out := callJSON(t, tool, args)

	var r bpResult
	mustUnmarshal(t, out, &r)
	if len(r.Results) != 1 {
		t.Fatalf("got %d results, want 1: %s", len(r.Results), out)
	}
	if r.Results[0].Success {
		t.Errorf("Success=true despite unwritable dir; tool should surface error")
	}
	if r.Results[0].Error == "" {
		t.Errorf("Error empty despite unwritable dir; tool should surface the failure")
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Errorf("original file content mutated to %q; aborted patches must not touch the target", string(data))
	}
}

// keep imports referenced — they are used through callJSON/mustUnmarshal
var _ = json.Unmarshal
var _ = strings.Contains
