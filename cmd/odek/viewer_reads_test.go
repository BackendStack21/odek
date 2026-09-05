package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Review fixes for H-6 licensing holes ────────────────────────────────

func TestRecordViewerReads_PlainViewRecords(t *testing.T) {
	danger.ResetReadLedgerForTest()
	t.Cleanup(danger.ResetReadLedgerForTest)
	dir := t.TempDir()
	f := filepath.Join(dir, "notes.txt")
	os.WriteFile(f, []byte("x"), 0644)

	recordViewerReads(context.Background(), "cat "+f)
	if !danger.WasRead(f) {
		t.Error("plain `cat file` should record the read")
	}
}

func TestRecordViewerReads_RedirectTargetNeverRecorded(t *testing.T) {
	// CRIT-001: `cat payload.sh > run.sh` copies bytes the model never saw
	// into run.sh — recording either operand must not license executing it.
	danger.ResetReadLedgerForTest()
	t.Cleanup(danger.ResetReadLedgerForTest)
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.sh")
	run := filepath.Join(dir, "run.sh")
	os.WriteFile(payload, []byte("#!/bin/sh\nevil\n"), 0755)
	os.WriteFile(run, []byte("#!/bin/sh\nevil\n"), 0755)

	recordViewerReads(context.Background(), "cat "+payload+" > "+run)
	if danger.WasRead(payload) {
		t.Error("redirect-bearing viewer run must not license the source")
	}
	if danger.WasRead(run) {
		t.Error("redirect write target was never displayed — must not be licensed")
	}
	// The gate must therefore still fire for executing run.sh.
	if targets := danger.UnreadScriptTargets("bash " + run); len(targets) == 0 {
		t.Error("executing the never-seen copy must stay gated")
	}
}

func TestRecordViewerReads_PipedPartialReadNotRecorded(t *testing.T) {
	// CRIT-001 companion: `cat big.sh | head -1` shows a prefix only.
	danger.ResetReadLedgerForTest()
	t.Cleanup(danger.ResetReadLedgerForTest)
	dir := t.TempDir()
	big := filepath.Join(dir, "big.sh")
	os.WriteFile(big, []byte("#!/bin/sh\nline2\nline3\n"), 0755)

	recordViewerReads(context.Background(), "cat "+big+" | head -1")
	if danger.WasRead(big) {
		t.Error("piped partial view must not license the whole file")
	}
}

func TestRecordViewerReads_AppendAndDevNullNotRecorded(t *testing.T) {
	danger.ResetReadLedgerForTest()
	t.Cleanup(danger.ResetReadLedgerForTest)
	dir := t.TempDir()
	f := filepath.Join(dir, "a.log")
	os.WriteFile(f, []byte("x"), 0644)

	recordViewerReads(context.Background(), "cat "+f+" >> /dev/null")
	if danger.WasRead(f) {
		t.Error("`cat f >> sink` never displayed f — must not be licensed")
	}
	recordViewerReads(context.Background(), "cat "+f+" 2>/dev/null")
	if danger.WasRead(f) {
		t.Error("stderr-redirected viewer run must not be licensed")
	}
}
