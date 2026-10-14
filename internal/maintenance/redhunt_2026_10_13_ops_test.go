package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// ~/.odek/media may be a symlink to a bigger disk (the Telegram media path
// resolver follows it: mediaBaseDirs resolves symlinks). filepath.WalkDir
// does not follow a symlinked root: it calls the walk function once with the
// root as a non-directory entry, and sweepMedia — "subdirectories themselves
// are never removed" — then os.Remove()s the root symlink itself once the
// link is older than an hour. The operator's link is deleted, the expired
// media behind it is never swept, and the next download recreates a plain
// directory on the small disk.
func TestRED_SweepMediaDeletesSymlinkedMediaRoot(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "bigdisk-media")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(target, "old.jpg")
	if err := os.WriteFile(stale, []byte("jpegdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "media")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// The link itself was created long ago.
	if err := lchtimes(link, old); err != nil {
		t.Skipf("cannot set symlink mtime: %v", err)
	}

	if _, err := sweepMedia(home); err != nil {
		t.Fatalf("sweepMedia: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("media root symlink was removed by the sweep (lstat err=%v)", err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("expired media behind the symlinked root was not swept")
	}
}

func lchtimes(path string, t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.Lutimes(path, []unix.Timeval{tv, tv})
}
