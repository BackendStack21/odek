package session

// Regression tests for the 2026-08 audit durability findings: audit-log
// writes were non-atomic, followed symlinks, and a corrupt log was
// silently replaced on the next record (destroying the only forensic
// evidence of prompt injection).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
)

// TestAudit_WritesAreSymlinkSafe: a symlink planted at the audit-log path
// used to be followed by os.WriteFile — truncating whatever it pointed at
// (attacker-influenceable Source/UserMessage fields included).
func TestAudit_WritesAreSymlinkSafe(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)

	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim-config.json")
	if err := os.WriteFile(victim, []byte("precious"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(auditDir, "20260821-audit-1.json")); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordIngest("20260821-audit-1", 1, "browser", "x"); err != nil {
		t.Fatalf("RecordIngest: %v", err)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "precious" {
		t.Fatalf("audit write followed a symlink and overwrote the victim with: %s", got)
	}
}

// TestAudit_CorruptLogPreserved: a torn/corrupt audit file must be moved
// aside (forensic evidence) and a fresh log started, never silently
// replaced.
func TestAudit_CorruptLogPreserved(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)

	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(auditDir, "20260821-audit-2.json")
	if err := os.WriteFile(path, []byte("{torn json"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordTurn("20260821-audit-2", AuditTurn{Turn: 1, UserMessage: "hi"}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) == 0 {
		t.Fatal("corrupt audit log was silently destroyed — no .corrupt-* sidecar")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "{torn json") {
		t.Fatalf(".corrupt sidecar does not preserve the original bytes: %q", data)
	}
	// The live log is fresh and usable.
	log, err := s.Load("20260821-audit-2")
	if err != nil || len(log.Turns) != 1 {
		t.Fatalf("fresh log not started after preservation: %+v err=%v", log, err)
	}
}

// TestAudit_RedactBoundaryInvalidatedByTrim pins the 2026-08 fix:
// RedactBoundary is an INDEX into the message list. Mid-run context
// trimming drops front groups and later turns re-grow past the stale
// boundary, so never-redacted messages ended up below it and were persisted
// unredacted (tool *error* text is never redacted in memory — the save-time
// scan is the only layer for it).
func TestAudit_RedactBoundaryInvalidatedByTrim(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const secret = "gsk_abcdefghijklmnopqrstuvwxyz1234567890" // redact-covered Groq form

	sess, err := store.Create([]llm.Message{
		{Role: "user", Content: "first turn " + secret},
		{Role: "assistant", Content: "ok"},
	}, "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RedactBoundary != 2 {
		t.Fatalf("setup: RedactBoundary = %d, want 2", loaded.RedactBoundary)
	}
	if strings.Contains(loaded.Messages[0].Content, secret) {
		t.Fatalf("setup: first save did not redact the secret")
	}

	// Simulate the loop trimming the head, then the conversation regrowing
	// past the stale boundary: index 0 now holds a NEW, never-redacted
	// message carrying a fresh secret.
	loaded.Messages = []llm.Message{
		{Role: "user", Content: "regrown turn " + secret},
		{Role: "assistant", Content: "old tail"},
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}

	final, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(final.Messages[0].Content, secret) {
		t.Fatal("message added after a mid-run trim persisted unredacted (stale RedactBoundary)")
	}
}
