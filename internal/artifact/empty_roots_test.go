package artifact

import "testing"

// SECURITY.md (artifact references): fail-closed validation — EMPTY roots
// reject every ref. A default-zero roots slice must never degrade into
// "allow". Unpinned until now (all existing tests pass non-empty roots).
func TestValidate_EmptyRootsRejectsEveryRef(t *testing.T) {
	ref := Ref{
		Schema:    "odek.artifact-ref/v1",
		ID:        "ref-1",
		URI:       "file:///tmp/some/file.bin",
		MediaType: "application/octet-stream",
	}
	if _, err := Validate(ref, nil); err == nil {
		t.Fatal("Validate(ref, nil) must reject — empty roots means refs are not allowed")
	}
	if _, err := Validate(ref, []string{}); err == nil {
		t.Fatal("Validate(ref, []) must reject — empty roots means refs are not allowed")
	}
}
