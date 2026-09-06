package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func skipIfRootEdges(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection does not work as root")
	}
}

// Empty userDir is rejected outright — no registry location means no
// promotion (fail-safe).
func TestRecordPromotion_EmptyUserDir(t *testing.T) {
	if err := RecordPromotion("", "my-skill", []byte("x")); err == nil {
		t.Fatal("expected error for empty userDir, got nil")
	}
}

// A corrupt registry JSON is discarded (fresh start), not fatal: the
// promotion proceeds and rewrites the registry.
func TestRecordPromotion_CorruptRegistryRecovers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, promotedRegistryFile), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecordPromotion(dir, "s1", []byte("body")); err != nil {
		t.Fatalf("RecordPromotion with corrupt registry: %v", err)
	}
	// Registry rewritten with only the new entry.
	data, err := os.ReadFile(filepath.Join(dir, promotedRegistryFile))
	if err != nil {
		t.Fatal(err)
	}
	reg := map[string]string{}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("registry not valid JSON after recovery: %v\n%s", err, data)
	}
	if len(reg) != 1 {
		t.Fatalf("registry has %d entries after corrupt recovery, want 1: %v", len(reg), reg)
	}
	if !isPromotedContent(dir, "s1", []byte("body")) {
		t.Fatal("s1 should be promoted after rewrite")
	}
}

// MkdirAll failure: userDir sits inside a read-only parent.
func TestRecordPromotion_MkdirFails(t *testing.T) {
	skipIfRootEdges(t)
	ro := t.TempDir()
	if err := os.Chmod(ro, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ro, 0755) //nolint:errcheck
	err := RecordPromotion(filepath.Join(ro, "user-skills"), "s1", []byte("body"))
	if err == nil {
		t.Fatal("expected mkdir error, got nil")
	}
}

// fsatomic write failure: userDir exists but is read-only.
func TestRecordPromotion_WriteFails(t *testing.T) {
	skipIfRootEdges(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755) //nolint:errcheck
	err := RecordPromotion(dir, "s1", []byte("body"))
	if err == nil {
		t.Fatal("expected write error, got nil")
	}
}

// An existing promotion persists across a second RecordPromotion call
// (registry merge, not overwrite).
func TestRecordPromotion_MergesEntries(t *testing.T) {
	dir := t.TempDir()
	if err := RecordPromotion(dir, "a", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := RecordPromotion(dir, "b", []byte("B")); err != nil {
		t.Fatal(err)
	}
	if !isPromotedContent(dir, "a", []byte("A")) {
		t.Fatal("a lost after promoting b")
	}
	if !isPromotedContent(dir, "b", []byte("B")) {
		t.Fatal("b not promoted")
	}
	if isPromotedContent(dir, "a", []byte("TAMPERED")) {
		t.Fatal("edited content must not count as promoted")
	}
}

// ── isPromotedContent corrupt-registry branch ────────────────────────────────

func TestIsPromotedContent_CorruptRegistryIsFalse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, promotedRegistryFile), []byte("]]not json[["), 0600); err != nil {
		t.Fatal(err)
	}
	if isPromotedContent(dir, "s", []byte("x")) {
		t.Fatal("corrupt registry must never report promoted")
	}
}

func TestIsPromotedContent_EmptyUserDirIsFalse(t *testing.T) {
	if isPromotedContent("", "s", []byte("x")) {
		t.Fatal("empty userDir must report not promoted")
	}
}

func TestIsPromotedContent_MissingRegistryIsFalse(t *testing.T) {
	if isPromotedContent(t.TempDir(), "s", []byte("x")) {
		t.Fatal("missing registry must report not promoted")
	}
}
