package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/flock"
)

// AcquireExecution owns a session's execution until release. Acquire before
// loading the run snapshot, and hold through its final save. The kernel lock
// coordinates independent stores and processes while unrelated sessions remain
// concurrent. Deleting a session unlinks its lock file, so after locking the
// opened file the owner verifies the path still names that same inode and
// retries on a fresh file otherwise: nobody ends up holding a stale unlinked
// inode while another run locks the recreated file. Waiting observes cancellation before any model/tool dispatch.
func (s *Store) AcquireExecution(ctx context.Context, id string) (func(), error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := s.tryLockRunFile(filepath.Join(s.dir, "."+id+".run.lock"))
		if err == nil {
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			var once sync.Once
			return func() { once.Do(release) }, nil
		}
		if !errors.Is(err, flock.ErrLocked) {
			return nil, fmt.Errorf("session: execution lock: %w", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// tryLockRunFile opens (creating) the lock file at path and locks it, then
// checks the path still refers to the locked inode. A file unlinked between
// open and lock is released and reopened, a bounded number of times.
func (s *Store) tryLockRunFile(path string) (func(), error) {
	for attempt := 0; attempt < 8; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("flock: open: %w", err)
		}
		unlock, err := flock.TryLockFile(f)
		if err != nil {
			f.Close()
			if errors.Is(err, flock.ErrLocked) {
				return nil, flock.ErrLocked
			}
			return nil, err
		}
		fi, ferr := f.Stat()
		pi, perr := os.Stat(path)
		if ferr == nil && perr == nil && os.SameFile(fi, pi) {
			return func() { unlock(); f.Close() }, nil
		}
		unlock()
		f.Close()
	}
	return nil, flock.ErrLocked
}
