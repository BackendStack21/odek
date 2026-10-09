package runtimelog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if !preview {
		// Nothing expired is the common case: find that out read-only so the
		// retained log is never rewritten for no reason.
		first, err := pruneStream(ctx, f, nil, cutoff, true)
		if err != nil || first == 0 {
			return 0, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		return rewritePruned(ctx, f, path, cutoff)
	}
	return pruneStream(ctx, f, nil, cutoff, false)
}

// rewritePruned streams src into a temp file without the expired records and
// atomically replaces path with it.
func rewritePruned(ctx context.Context, src *os.File, path string, cutoff time.Time) (int, error) {
	tmp, err := createPruneTemp(filepath.Dir(path), ".runtime-prune-*")
	if err != nil {
		return 0, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	bw := bufio.NewWriterSize(tmp, 64<<10)
	removed, err := pruneStream(ctx, src, bw, cutoff, false)
	if err != nil {
		return 0, err
	}
	if removed == 0 {
		return 0, nil
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, err
	}
	return removed, nil
}

// pruneStream counts expired records in src, copying retained ones to out when
// non-nil. With stopAtFirst it returns as soon as one expired record is seen.
func pruneStream(ctx context.Context, src io.Reader, out io.Writer, cutoff time.Time, stopAtFirst bool) (int, error) {
	removed := 0
	reader := bufio.NewReaderSize(src, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		line, oversized, done, err := readPruneLine(reader, out)
		if err != nil {
			return 0, err
		}
		if done {
			break
		}
		if oversized {
			// Already streamed to out verbatim: one malformed record.
			continue
		}
		var rec struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(line, &rec) == nil && !rec.Timestamp.IsZero() && rec.Timestamp.Before(cutoff) {
			removed++
			if stopAtFirst {
				return removed, nil
			}
			continue
		}
		if out != nil {
			if _, err := out.Write(line); err != nil {
				return 0, err
			}
			if _, err := out.Write([]byte{'\n'}); err != nil {
				return 0, err
			}
		}
	}
	return removed, nil
}

// createPruneTemp creates the replacement file; a variable so tests can observe
// whether a rewrite was started.
var createPruneTemp = os.CreateTemp

// maxPruneLine bounds the bytes held in memory for one record.
const maxPruneLine = 1 << 20

// readPruneLine reads one newline-terminated record. A record longer than
// maxPruneLine is treated as a single malformed record: it is never buffered,
// and when tmp is non-nil it is streamed through unchanged so it is kept like
// any other unparseable line. done reports end of input with no further record.
func readPruneLine(r *bufio.Reader, tmp io.Writer) (line []byte, oversized, done bool, err error) {
	var buf []byte
	total := 0
	for {
		part, rerr := r.ReadSlice('\n')
		if rerr != nil && rerr != bufio.ErrBufferFull && rerr != io.EOF {
			return nil, false, false, rerr
		}
		total += len(part)
		if !oversized && total > maxPruneLine {
			oversized = true
			if tmp != nil {
				if _, werr := tmp.Write(buf); werr != nil {
					return nil, false, false, werr
				}
			}
			buf = nil
		}
		if oversized {
			if tmp != nil {
				if _, werr := tmp.Write(part); werr != nil {
					return nil, false, false, werr
				}
			}
		} else {
			buf = append(buf, part...)
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if rerr == io.EOF && total == 0 {
			return nil, false, true, nil
		}
		if oversized {
			if tmp != nil && (len(part) == 0 || part[len(part)-1] != '\n') {
				if _, werr := tmp.Write([]byte{'\n'}); werr != nil {
					return nil, false, false, werr
				}
			}
			return nil, true, false, nil
		}
		return bytes.TrimSuffix(buf, []byte{'\n'}), false, false, nil
	}
}
