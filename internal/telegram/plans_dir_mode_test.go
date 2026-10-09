package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

// The daily budget tracker may create ~/.odek; it must be private too.
func TestCheckDailyBudget_CreatesPrivateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	b := NewBot("t")
	b.DailyTokenBudget = 1000
	if err := b.CheckDailyBudget(10); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, ".odek"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf(".odek created with mode %o", fi.Mode().Perm())
	}
}

// Per-chat plans can contain sensitive task descriptions; the directories
// holding them (and the ~/.odek parent that may be created here) must not be
// world-traversable.
func TestRED_PlansDirWorldReadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := ensurePlansDir(42)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(home, ".odek")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s created with mode %o, want no group/other access", d, fi.Mode().Perm())
		}
	}
}
