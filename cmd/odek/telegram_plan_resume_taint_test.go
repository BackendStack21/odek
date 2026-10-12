package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The persisted history of /plan_resume carries the untrusted wrapper, which
// the session store also detects on save, so a successful resume taints the
// session by two routes. The explicit MarkUntrustedIngested call is what
// taints the live chat session when the save cannot happen; pin it by making
// the save fail.
func TestPlanResumeMarksLiveSessionUntrusted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	const chatID = int64(88120016)
	sm, home := tgrh12Env(t, chatID)
	dir := filepath.Join(home, ".odek", "plans", fmt.Sprintf("chat%d", chatID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p1.md"), []byte("# Plan\nstep one"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if live.UntrustedIngested() {
		t.Fatal("precondition: fresh chat session is already tainted")
	}
	sessions := filepath.Join(home, ".odek", "sessions")
	if err := os.Chmod(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessions, 0o700) })

	telegramPlanResume(chatID, sm) // the save fails; the in-memory mark must remain

	live, err = sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !live.UntrustedIngested() {
		t.Error("live chat session not marked UntrustedIngested after /plan_resume")
	}
}
