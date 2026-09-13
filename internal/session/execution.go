package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/flock"
)

// AcquireExecution owns a session's execution until release. Acquire before
// loading the run snapshot, and hold through its final save. The kernel lock
// coordinates independent stores and processes while unrelated sessions remain
// concurrent. Lock files stay in place so queued owners cannot lock a stale
// unlinked inode. Waiting observes cancellation before any model/tool dispatch.
func (s *Store) AcquireExecution(ctx context.Context, id string) (func(), error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := flock.TryLock(filepath.Join(s.dir, "."+id+".run.lock"))
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
