package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionOwnershipAcrossStoresAndCancellation(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewStoreWithDir(dir)
	b, _ := NewStoreWithDir(dir)
	release, err := a.AcquireExecution(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if unlocked, err := b.AcquireExecution(ctx, "shared"); !errors.Is(err, context.DeadlineExceeded) {
		if unlocked != nil {
			unlocked()
		}
		t.Fatalf("second owner bypassed first: %v", err)
	}
	unrelated, err := b.AcquireExecution(context.Background(), "independent")
	if err != nil {
		t.Fatal(err)
	}
	unrelated()
	release()
	next, err := b.AcquireExecution(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestStoreStaleSaveRejectedAcrossProcesses(t *testing.T) {
	const childEnv = "ODEK_SESSION_CONFLICT_PROBE"
	if dir := os.Getenv(childEnv); dir != "" {
		store, err := NewStoreWithDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		stale, err := store.Load("shared")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "loaded"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, filepath.Join(dir, "continue"))
		stale.Messages = append(stale.Messages, Message{Role: "assistant", Content: "stale"})
		if err := store.SaveNoIndex(stale); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale save: %v", err)
		}
		return
	}
	dir := t.TempDir()
	store, _ := NewStoreWithDir(dir)
	sess := &Session{ID: "shared", Messages: []Message{{Role: "user", Content: "seed"}}}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreStaleSaveRejectedAcrossProcesses$")
	cmd.Env = append(os.Environ(), childEnv+"="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitForFile(t, filepath.Join(dir, "loaded"))
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "committed"})
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "continue"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 2 || len(persisted.Messages) != 2 || persisted.Messages[1].Content != "committed" {
		t.Fatalf("newer checkpoint lost: %+v", persisted)
	}
}

func TestStoreStaleSaveRejectedAfterIDRecreated(t *testing.T) {
	store, _ := NewStoreWithDir(t.TempDir())
	old := &Session{ID: "fixed", Messages: []Message{{Role: "user", Content: "old"}}}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(old.ID); err != nil {
		t.Fatal(err)
	}
	fresh := &Session{ID: old.ID, Messages: []Message{{Role: "user", Content: "new"}}}
	if err := store.Save(fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Revision != old.Revision {
		t.Fatal("fixture must reproduce equal counters")
	}
	if err := store.Save(old); !errors.Is(err, ErrConflict) {
		t.Fatalf("recreated session accepted old generation: %v", err)
	}
	stored, err := store.Load(fresh.ID)
	if err != nil || stored.Messages[0].Content != "new" {
		t.Fatal("recreated session overwritten")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("subprocess synchronization timed out")
}
