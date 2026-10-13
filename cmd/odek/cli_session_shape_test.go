package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// Deleting a session that does not exist is an error, not "Deleted session".
func TestRED_SessionDelete_UnknownIDIsAnError(t *testing.T) {
	home := setupTestHome(t)
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		err = deleteSession(store, []string{"20260101-doesnotexist"})
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("delete unknown session: err=%v out=%q; want a not-found error", err, out)
	}
	if strings.Contains(out, "Deleted") {
		t.Fatalf("reported success for a missing session: %q", out)
	}
	// A session whose file is gone but whose audit log remains is purged
	// (the audit log must not outlive a deleted session).
	auditDir := filepath.Join(home, ".odek", "sessions", "audit")
	os.MkdirAll(auditDir, 0700)
	os.WriteFile(filepath.Join(auditDir, "20260101-leftover.json"), []byte("{}"), 0600)
	out = captureStdout(func() { err = deleteSession(store, []string{"20260101-leftover"}) })
	if err != nil || !strings.Contains(out, "leftovers") {
		t.Fatalf("purge leftovers: err=%v out=%q", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(auditDir, "20260101-leftover.json")); !os.IsNotExist(statErr) {
		t.Fatal("audit log of the deleted session still exists")
	}
}

// Loading a missing session says "not found" instead of leaking the store
// path through a raw open error; os.ErrNotExist is still reachable for
// callers that test for it.
func TestRED_SessionLoad_MissingIsPlainNotFound(t *testing.T) {
	setupTestHome(t)
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("20260101-doesnotexist")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), string(os.PathSeparator)+"sessions") {
		t.Fatalf("load error = %q; want a plain not-found message without the store path", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("not-found error no longer matches os.ErrNotExist")
	}
	// The CLI surfaces the same message.
	if err := showSession(store, []string{"20260101-doesnotexist"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("session show: %v", err)
	}
}

// `--max-iter 0` (or negative) is an error like every other numeric budget
// flag; it was silently ignored.
func TestRED_RunFlags_MaxIterMustBePositive(t *testing.T) {
	for _, v := range []string{"0", "-3"} {
		if _, err := parseRunFlags([]string{"--max-iter", v, "task"}); err == nil || !strings.Contains(err.Error(), "positive") {
			t.Errorf("--max-iter %s: %v; want a positive-integer error", v, err)
		}
	}
}

// Fixed-shape listing commands reject stray arguments instead of ignoring
// them, and the session usage names every subcommand.
func TestRED_ListingsRejectStrayArguments(t *testing.T) {
	home := setupTestHome(t)
	os.MkdirAll(filepath.Join(home, ".odek"), 0700)
	for _, args := range [][]string{{"list", "extra"}, {"show", "a", "b"}, {"delete", "a", "b"}, {"trim", "a", "5", "c"}, {"cleanup", "5", "x"}} {
		if err := sessionCmd(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("session %s: %v; want a usage error", strings.Join(args, " "), err)
		}
	}
	for _, args := range [][]string{{"list", "extra"}, {"rm", "a", "b"}, {"enable", "a", "b"}, {"disable", "a", "b"}, {"next", "a", "b"}, {"run", "a", "b"}, {"daemon", "--bogus"}} {
		if err := scheduleCmd(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("schedule %s: %v; want a usage error", strings.Join(args, " "), err)
		}
	}
	for _, args := range [][]string{{"list", "extra"}, {"view", "a", "b"}, {"delete", "a", "b"}, {"promote", "a", "--froce"}, {"import", "file:///x.md", "--bogus"}} {
		if err := skillCmd(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("skill %s: %v; want a usage error", strings.Join(args, " "), err)
		}
	}
	flush := captureStderr(t)
	_ = sessionCmd(nil)
	usage := flush()
	for _, want := range []string{"trim", "cleanup"} {
		if !strings.Contains(usage, want) {
			t.Errorf("session usage lacks %q:\n%s", want, usage)
		}
	}
}
