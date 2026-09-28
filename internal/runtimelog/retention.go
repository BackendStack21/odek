package runtimelog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Prune removes timestamp-expired records from the current log and backup.
// Each replacement is atomic under the writers' lock. Malformed/undated records
// are retained; a scan error leaves the original file intact. Preview is read-only.
func Prune(ctx context.Context, path string, cutoff time.Time, preview bool) (int, error) {
	if _, err := os.Lstat(filepath.Dir(path)); os.IsNotExist(err) {
		return 0, nil
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if _, err := os.Lstat(path + ".1"); os.IsNotExist(err) {
			return 0, nil
		}
	}
	if !preview {
		release, err := lock(path)
		if err != nil {
			return 0, err
		}
		defer release()
	}
	total := 0
	for _, name := range []string{path, path + ".1"} {
		n, err := pruneFile(ctx, name, cutoff, preview)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
func pruneFile(ctx context.Context, path string, cutoff time.Time, preview bool) (int, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !st.Mode().IsRegular() {
		return 0, fmt.Errorf("runtime log must be a regular file")
	}
	var tmp *os.File
	if !preview {
		tmp, err = os.CreateTemp(filepath.Dir(path), ".runtime-prune-*")
		if err != nil {
			return 0, err
		}
		defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	}
	removed := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		line := scanner.Bytes()
		var rec struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(line, &rec) == nil && !rec.Timestamp.IsZero() && rec.Timestamp.Before(cutoff) {
			removed++
			continue
		}
		if tmp != nil {
			if _, err := tmp.Write(append(line, '\n')); err != nil {
				return 0, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if tmp != nil && removed > 0 {
		if err := tmp.Sync(); err != nil {
			return 0, err
		}
		if err := tmp.Close(); err != nil {
			return 0, err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			return 0, err
		}
	}
	return removed, nil
}
