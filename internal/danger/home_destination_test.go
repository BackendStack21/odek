package danger

import (
	"path/filepath"
	"testing"
)

// A bare `~` or $HOME destination is a directory even when the home path does
// not exist, so the rc file copied into it lands as <home>/<name>.
func TestHomeIsADirectoryDestination(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent-home")
	t.Setenv("HOME", home)
	for _, cmd := range []string{
		"cp evil/.bashrc ~",
		"cp evil/.zshrc $HOME",
		"mv .profile ${HOME}",
		"install .bash_profile ~",
	} {
		if got := Classify(cmd); Rank(got) < Rank(Persistence) {
			t.Errorf("Classify(%q) = %s, want at least persistence", cmd, got)
		}
	}
	if !isDirectoryDestination("~", home) || isDirectoryDestination("x", filepath.Join(home, "x")) {
		t.Error("only the home itself is assumed to be a directory")
	}
	if got := Classify("cp notes.txt ~"); Rank(got) >= Rank(SystemWrite) {
		t.Errorf("an ordinary file into the home = %s, want below system_write", got)
	}
}
