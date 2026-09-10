package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// newSandboxCleanup is shared by every execution surface. Successful cleanup
// is idempotent; failure remains retryable and is reported even when a deferred
// Agent.Close caller cannot return the error to its caller.
func newSandboxCleanup(container string, remove func(context.Context) error) func() error {
	var mu sync.Mutex
	var removed bool
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		if removed {
			return nil
		}
		fmt.Fprintf(os.Stderr, "odek: destroying sandbox container %s...\n", container)
		err := retrySandboxRemoval(remove, 5*time.Second)
		if err != nil {
			err = fmt.Errorf("sandbox cleanup failed for %s after 3 attempts: %w", container, err)
			fmt.Fprintf(os.Stderr, "odek: %v; container may still be running. Remove it with: docker rm -f %s\n", err, container)
			return err
		}
		removed = true
		return nil
	}
}

// Each attempt has its own deadline so a timed-out Docker client can recover
// on the next attempt. Callers must honor ctx; production uses CommandContext.
func retrySandboxRemoval(remove func(context.Context) error, timeout time.Duration) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err = remove(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if attempt < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return err
}
