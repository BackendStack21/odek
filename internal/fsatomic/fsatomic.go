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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile atomically and durably writes data to path with the given perm.
// On success the bytes are fsynced to disk and the rename is durable.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)

	// Create the temp file with the exact requested permissions immediately,
	// closing the window where umask could leave it more permissive than
	// intended. Use O_WRONLY|O_CREATE|O_EXCL so a pre-created symlink cannot
	// be followed; include a random suffix so concurrent writers don't collide.
	var randSuffix [8]byte
	if _, rerr := rand.Read(randSuffix[:]); rerr != nil {
		return fmt.Errorf("fsatomic: rand suffix: %w", rerr)
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(randSuffix[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("fsatomic: create temp: %w", err)
	}
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

	// Make the rename itself durable by fsyncing the parent directory.
	if d, derr := os.Open(dir); derr == nil {
		defer d.Close()
		if err := d.Sync(); err != nil {
			return fmt.Errorf("fsatomic: fsync dir: %w", err)
		}
	}
	return nil
}
