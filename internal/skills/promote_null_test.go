package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordPromotion_NullRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, promotedRegistryFile), []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecordPromotion(dir, "null-skill", []byte("body")); err != nil {
		t.Fatalf("RecordPromotion panicked or failed for valid null registry: %v", err)
	}
	if !isPromotedContent(dir, "null-skill", []byte("body")) {
		t.Fatal("promotion was not recorded")
	}
}
