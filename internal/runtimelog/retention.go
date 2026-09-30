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
	return PruneWithOptions(ctx, path, cutoff, preview, 2)
}

// PruneWithOptions removes expired records from the active file and every
// configured backup generation under the same lock used by append/rotation.
// maxFiles counts the active file.
func PruneWithOptions(ctx context.Context, path string, cutoff time.Time, preview bool, maxFiles int) (int, error) {
	if maxFiles < 1 {
		maxFiles = 1
	}
	if maxFiles > 32 {
		maxFiles = 32
	}
	if _, err := os.Lstat(filepath.Dir(path)); os.IsNotExist(err) {
		return 0, nil
	}
	any := false
	for i := 0; i < maxFiles; i++ {
		name := path
		if i > 0 {
			name += fmt.Sprintf(".%d", i)
		}
		if _, err := os.Lstat(name); err == nil {
			any = true
		} else if !os.IsNotExist(err) {
			return 0, err
		}
	}
	if !any {
		return 0, nil
	}
	if !preview {
		release, err := lock(path)
		if err != nil {
			return 0, err
		}
		defer release()
		if err := removeExcessBackupsLocked(path, maxFiles); err != nil {
			return 0, err
		}
	}
	total := 0
	for i := 0; i < maxFiles; i++ {
		name := path
		if i > 0 {
			name += fmt.Sprintf(".%d", i)
		}
		n, err := pruneFile(ctx, name, cutoff, preview)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// PruneAtStartup exposes synchronous startup retention to the process owner.
// The caller decides whether a pruning error is fatal and can report the
// removed-record count without coupling logger construction to maintenance.
func PruneAtStartup(ctx context.Context, opts Options) (int, error) {
	if opts.Path == "" {
		return 0, nil
	}
	if opts.MaxFiles < 1 {
		opts.MaxFiles = 1
	}
	if opts.MaxFiles > 32 {
		opts.MaxFiles = 32
	}
	if opts.MaxAgeHours < 0 {
		opts.MaxAgeHours = 0
	}
	if opts.MaxAgeHours > 87600 {
		opts.MaxAgeHours = 87600
	}
	removed := 0
	if opts.MaxAgeHours > 0 {
		n, err := PruneWithOptions(ctx, opts.Path, time.Now().Add(-time.Duration(opts.MaxAgeHours)*time.Hour), false, opts.MaxFiles)
		removed += n
		if err != nil {
			return removed, err
		}
	} else {
		release, err := lock(opts.Path)
		if err != nil {
			return 0, err
		}
		defer release()
		if err := removeExcessBackupsLocked(opts.Path, opts.MaxFiles); err != nil {
			return 0, err
		}
	}
	return removed, nil
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
