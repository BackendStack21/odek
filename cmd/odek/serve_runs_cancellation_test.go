package main

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestCancelRunCountsResourcesUntilFinish(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	r := &serveRun{ID: "run-cancel", Status: "running", StartedAt: time.Now(), pending: map[string]*approvalRequest{}, resourcesActive: true}
	r.cond = sync.NewCond(&r.mu)
	r.cancel = func() {}
	cleanupStarted := make(chan struct{})
	release := make(chan struct{})
	r.cleanup = func() { close(cleanupStarted); <-release }
	registerRun(r)

	cancelRun(r)
	if got := activeRunCount(); got != 1 {
		t.Fatalf("active runs after cancellation = %d, want 1 while cleanup is pending", got)
	}
	if got := r.snapshot(false)["status"]; got != "cancelled" {
		t.Fatalf("public status = %v, want cancelled", got)
	}
	done := make(chan struct{})
	go func() { r.releaseResources(); close(done) }()
	<-cleanupStarted
	if got := activeRunCount(); got != 1 {
		t.Fatalf("active runs during cleanup = %d, want 1", got)
	}
	for i := 0; i < serveRunsCap+1; i++ {
		old := &serveRun{ID: "completed-" + strconv.Itoa(i), Status: "completed", EndedAt: time.Now().Add(time.Hour), pending: map[string]*approvalRequest{}}
		old.cond = sync.NewCond(&old.mu)
		registerRun(old)
	}
	if lookupRun(r.ID) == nil {
		t.Fatal("registry evicted a cancelled run whose cleanup was still pending")
	}
	close(release)
	<-done
	if got := activeRunCount(); got != 0 {
		t.Fatalf("active runs after finish = %d, want 0", got)
	}
}

func TestReleaseResourcesClearsActivityAfterPanic(t *testing.T) {
	r := &serveRun{resourcesActive: true, cleanup: func() { panic("cleanup failed") }}
	func() {
		defer func() { _ = recover() }()
		r.releaseResources()
	}()
	r.mu.Lock()
	active := r.resourcesActive
	r.mu.Unlock()
	if active {
		t.Fatal("resourcesActive remained set after cleanup panic")
	}
}
