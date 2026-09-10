package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
)

// serveSandboxLease retains an agent's container until both its owner and
// every background job have released it. A job keeps its original routing
// across session switches, reconnects, and headless-run completion.
type serveSandboxLease struct {
	mu        sync.Mutex
	container string
	cleanup   func() error
	closed    bool
	jobs      int
}

func (l *serveSandboxLease) acquire() (bgproc.SpawnOptions, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.container == "" {
		return bgproc.SpawnOptions{}, fmt.Errorf("background sandbox is unavailable")
	}
	l.jobs++
	name := l.container
	var once sync.Once
	return bgproc.SpawnOptions{
		Wrap: func(command string) ([]string, func(), error) {
			argv, followUp := wrapBackgroundSandboxCommand(name, command)
			return append([]string{"docker"}, argv...), followUp, nil
		},
		Release: func() { once.Do(l.release) },
	}, nil
}

// cleanupClosed serializes attempts and retains ownership after failure.
// Cleanup callbacks must be bounded; production uses a Docker command deadline.
func (l *serveSandboxLease) cleanupClosed() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed || l.jobs != 0 || l.cleanup == nil {
		return nil
	}
	// Publish before any blocking removal so shutdown can always find us.
	pendingSandboxCleanup.Store(l, struct{}{})
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = l.cleanup(); err == nil {
			l.cleanup = nil
			pendingSandboxCleanup.Delete(l)
			return nil
		}
		if attempt < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	pendingSandboxCleanup.Store(l, struct{}{})
	return err
}

func (l *serveSandboxLease) release() {
	l.mu.Lock()
	l.jobs--
	l.mu.Unlock()
	if err := l.cleanupClosed(); err != nil {
		fmt.Fprintf(os.Stderr, "odek: background sandbox cleanup pending: %v\n", err)
	}
}

func (l *serveSandboxLease) close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return l.cleanupClosed()
}

// Failed removals remain discoverable for maintenance and final shutdown.
var pendingSandboxCleanup sync.Map

func retrySandboxCleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	pendingSandboxCleanup.Range(func(key, _ any) bool {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		wg.Add(1)
		go func(l *serveSandboxLease) { defer wg.Done(); defer func() { <-slots }(); _ = l.cleanupClosed() }(key.(*serveSandboxLease))
		return true
	})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "odek: sandbox cleanup retry deadline exceeded")
	}
}
