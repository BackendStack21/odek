package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// insideRoots builds the prefix as resolvedRoot+"/", so an artifact root of
// "/" becomes "//" and no artifact path can ever match it — the same defect
// the earlier fix removed from pathutil.WithinRoot.
func TestRED_ValidateFilesystemRootConfinesNothing(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "out.txt")
	body := []byte("artifact body")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	size := int64(len(body))
	ref := Ref{
		Schema:    SchemaArtifactRef,
		ID:        "out",
		URI:       FileURI(path),
		MediaType: "text/plain",
		SHA256:    hex.EncodeToString(sum[:]),
		SizeBytes: &size,
	}
	if _, err := Validate(ref, []string{"/"}); err != nil {
		t.Fatalf("artifact under the configured root \"/\" was rejected: %v", err)
	}
}
