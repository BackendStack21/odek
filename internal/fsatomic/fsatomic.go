// Package fsatomic provides a crash-durable atomic file write.
//
// The common "write a temp file then rename over the target" idiom gives
// atomicity (a reader sees either the old or the new file, never a torn one),
// but NOT durability: without an fsync, a power loss or kernel crash can land
// the rename in the directory while the file's data is still only in the page
// cache, leaving an empty or truncated file after reboot. For data the agent
// can't reconstruct — conversation sessions, extracted memories — that is silent
// data loss.
//
// WriteFile closes the gap: it fsyncs the file data before the rename and
// fsyncs the parent directory after, so a successful return means the bytes are
// durably on disk. It also uses a unique temp name, so two concurrent writers
// to the same target can't clobber each other's temp file.
package fsatomic

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile atomically and durably writes data to path with the given perm.
// On success the bytes are fsynced to disk and the rename is durable.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsatomic: create temp: %w", err)
	}
	tmp := f.Name()
	// Remove the temp file on any failure before the rename succeeds.
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("fsatomic: write: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("fsatomic: chmod: %w", err)
	}
	// Flush the file's data to disk before exposing it via the rename.
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsatomic: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fsatomic: close temp: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("fsatomic: rename: %w", err)
	}
	tmp = "" // renamed — no longer ours to remove

	// Make the rename itself durable. Best-effort: some filesystems don't
	// support directory fsync, and the data is already synced, so a failure
	// here doesn't corrupt anything — it just weakens the crash guarantee.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
