package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

// FileURI must percent-encode the path so a filename containing URI-reserved
// characters ('#', '?', '%', space) round-trips through Validate to the same
// file instead of being read as a fragment, query or escape.
func TestRED_FileURIRoundTripsReservedCharacters(t *testing.T) {
	for _, name := range []string{"release-notes#v2.md", "coverage 100%.txt", "faq?.md", "plain.txt", "a%2e%2e.txt"} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		size := int64(6)
		ref := Ref{Schema: SchemaArtifactRef, ID: "a", URI: FileURI(path), MediaType: "text/plain",
			SHA256: "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", SizeBytes: &size}
		got, err := Validate(ref, []string{dir})
		if err != nil {
			t.Errorf("%q: FileURI(%q) = %q rejected: %v", name, path, ref.URI, err)
			continue
		}
		if got != path {
			t.Errorf("%q: Validate resolved %q, want %q", name, got, path)
		}
	}
}
