package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStdioAliases_DoNotCleanSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	link := filepath.Join(root, "link")
	parents := len(strings.Split(strings.Trim(link, string(filepath.Separator)), string(filepath.Separator)))
	protected := filepath.Join(home, ".ssh", strings.Repeat("nested/", parents+2))
	if err := os.MkdirAll(protected, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, link); err != nil {
		t.Fatal(err)
	}
	path := link + "/" + strings.Repeat("../", parents) + "dev/stderr"
	if filepath.Clean(path) != "/dev/stderr" {
		t.Fatalf("invalid regression setup: %s", path)
	}
	if isDirectBenignDevice(path) {
		t.Fatal("unresolved traversal accepted as stdio")
	}
	if ClassifyPathWrite(path) != SystemWrite {
		t.Fatalf("protected symlink traversal classified %s", ClassifyPathWrite(path))
	}
	state := shellAnalysisState{cwd: root}
	for _, target := range []string{path, strings.TrimPrefix(path, root+"/")} {
		if risk := state.targetRisk(target, root, true); risk != SystemWrite {
			t.Errorf("target %q risk=%s", target, risk)
		}
	}
}
