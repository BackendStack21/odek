package main

// RED-first TOCTOU regression tests for the artifact read path
// (cmd/odek/artifact_read_tool.go + the renderArtifacts inline path in
// subagent_tool.go).
//
// Bug: artifact_read re-opened the registered artifact BY PATH at read time
// with plain os.Stat/os.Open — both follow symlinks — and the ref's recorded
// sha256 was never re-checked. A same-user process (the threat model
// includes approved MCP servers) could swap ~/.odek/artifacts/.../<file> for
// a symlink after collation and artifact_read would stream any readable file
// outside all artifact roots into the parent context, paged.
//
// Each test registers a REAL artifact through the production store path
// (registerTaskArtifacts → artifact.Validate), then tampers with the file
// and asserts the read fails closed instead of returning foreign content.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/artifact"
)

// registerArtifactForTOCTOU writes one artifact file into a dedicated task
// dir and registers it through the production path (registerTaskArtifacts,
// which runs artifact.Validate against that dir as the only root). Returns
// the registered root dir and the on-disk artifact path.
func registerArtifactForTOCTOU(t *testing.T, id, content string) (root, path string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "task-root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(root, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"status":"success","summary":"ok","artifacts":[{"schema":%q,"id":%q,"uri":"file://%s","media_type":"text/markdown","sha256":%q,"size_bytes":%d}]}`,
		artifact.SchemaArtifactRef, id, path, expectedSHA(t, content), len(content))
	if notes := registerTaskArtifacts(raw, root, 0); len(notes) != 0 {
		t.Fatalf("clean registration must not produce notes: %v", notes)
	}
	// The registration helper silently skips validation failures — make
	// sure the entry actually landed before tampering.
	if _, ok := lookupSubagentArtifact(id); !ok {
		t.Fatal("artifact was not registered (validation silently dropped it)")
	}
	return root, path
}

func newArtifactReadToolForTOCTOU(t *testing.T) *artifactReadTool {
	t.Helper()
	tool := &artifactReadTool{}
	tool.SetContext(t.Context())
	return tool
}

// TestArtifactReadTool_SymlinkSwapOutsideRootsRejected pins the reported
// bug: swapping the artifact file for a symlink pointing OUTSIDE the
// artifact roots after registration must fail the read — never stream the
// symlink target's content.
func TestArtifactReadTool_SymlinkSwapOutsideRootsRejected(t *testing.T) {
	resetArtifactRegistryForTest()
	root, path := registerArtifactForTOCTOU(t, "report", "# Report\nlegit findings")

	// A sibling of the artifact root — outside every registered root.
	secretDir := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secretDir, "secret.txt")
	const secretBody = "TOP SECRET outside-root payload"
	if err := os.WriteFile(secret, []byte(secretBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// The swap: same directory entry, now a symlink out of the roots.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Fatal(err)
	}
	// Guard the test's own premise: the final component IS a symlink now.
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("test setup: swap did not produce a symlink (err=%v)", err)
	}

	got, err := newArtifactReadToolForTOCTOU(t).Call(`{"id":"report"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, secretBody) {
		t.Errorf("symlinked target content leaked into the parent context:\n%s", got)
	}
	if !strings.Contains(got, `"error"`) {
		t.Errorf("swapped artifact must fail the read with an error, got:\n%s", got)
	}
}

// TestArtifactReadTool_ReplacedContentRejected pins the digest half of the
// fix: replacing the file with a same-size regular file (no symlink — so
// only the sha256 re-check can catch it) must fail the read.
func TestArtifactReadTool_ReplacedContentRejected(t *testing.T) {
	resetArtifactRegistryForTest()
	original := strings.Repeat("A", 64)
	_, path := registerArtifactForTOCTOU(t, "blob", original)

	// Same length, different bytes: size_bytes still matches, only the
	// digest betrays the swap.
	swapped := strings.Repeat("B", 64)
	if err := os.WriteFile(path, []byte(swapped), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := newArtifactReadToolForTOCTOU(t).Call(`{"id":"blob"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, swapped) {
		t.Errorf("replaced content leaked into the parent context:\n%s", got)
	}
	if !strings.Contains(got, `"error"`) {
		t.Errorf("digest mismatch must fail the read with an error, got:\n%s", got)
	}
}

// TestArtifactReadTool_UnhashableRefRejected pins fail-closed behavior for
// refs registered without a sha256: with nothing recorded to verify against,
// the read must refuse rather than serve unverified bytes.
func TestArtifactReadTool_UnhashableRefRejected(t *testing.T) {
	resetArtifactRegistryForTest()
	dir := t.TempDir()
	path := filepath.Join(dir, "bare.md")
	const body = "no digest recorded for me"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(body))
	registerSubagentArtifact(artifactEntry{Ref: artifact.Ref{
		Schema: artifact.SchemaArtifactRef, ID: "bare", MediaType: "text/markdown",
		URI: "file://" + path, SizeBytes: &size,
		// SHA256 intentionally absent.
	}, Path: path, TaskIdx: 0})

	got, err := newArtifactReadToolForTOCTOU(t).Call(`{"id":"bare"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, body) {
		t.Errorf("unverifiable ref must not be served:\n%s", got)
	}
	if !strings.Contains(got, `"error"`) {
		t.Errorf("sha256-less ref must fail closed with an error, got:\n%s", got)
	}
}

// TestArtifactReadTool_UnchangedFileStillReads is the positive control: an
// untouched artifact reads exactly as before the hardening.
func TestArtifactReadTool_UnchangedFileStillReads(t *testing.T) {
	resetArtifactRegistryForTest()
	const body = "# Report\nlegit findings"
	_, _ = registerArtifactForTOCTOU(t, "report", body)

	got, err := newArtifactReadToolForTOCTOU(t).Call(`{"id":"report"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, body) {
		t.Errorf("unchanged artifact must still read:\n%s", got)
	}
	if strings.Contains(got, `"error"`) {
		t.Errorf("unchanged artifact must not error:\n%s", got)
	}
}
