package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRED_EmptyRootStringIsNotCWD(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	os.WriteFile(f, []byte("x"), 0o600)
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)
	real, _ := filepath.EvalSymlinks(f)
	ref := Ref{Schema: SchemaArtifactRef, ID: "a", URI: "file://" + real, MediaType: "text/plain"}
	if _, err := Validate(ref, []string{""}); err == nil {
		t.Fatal("empty-string root accepted as the process cwd")
	}
}

func TestInsideRoots_BlankEntryIgnoredRealRootStillWorks(t *testing.T) {
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	if ok, _ := insideRoots(filepath.Join(real, "a"), []string{"  ", dir}); !ok {
		t.Fatal("real root must still confine after a blank entry")
	}
	if ok, _ := insideRoots(filepath.Join(real, "a"), []string{"", "  "}); ok {
		t.Fatal("blank-only roots must confine nothing")
	}
}
