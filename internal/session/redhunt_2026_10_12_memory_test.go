package session

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/redact"
)

// TestRED_AuditIngestResourcesLeakSecretFragments: RecordIngest extracts
// resource-like tokens from the RAW ingested body and only then redacts each
// token on its own. A secret whose redaction depends on its surrounding
// context (an AWS secret access key after "aws_secret_access_key=") is
// redacted in the session file, but the resource regex splits it at '/'
// into a path-like fragment that, stripped of its context, no redaction
// pattern recognises — so a fragment of the secret lands in the audit log.
func TestRED_AuditIngestResourcesLeakSecretFragments(t *testing.T) {
	const secretLine = "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// Precondition: the redactor does recognise the secret in context, so
	// session saves never persist it.
	if strings.Contains(redact.RedactSecrets(secretLine), "K7MDENG") {
		t.Skip("redactor does not cover this secret in context; finding does not apply")
	}

	dir := t.TempDir()
	a := NewAuditStore(dir)
	const id = "20261012-auditfrag"
	body := "fetched config:\n" + secretLine + "\n"
	if err := a.RecordIngest(id, 1, "/workspace/.env.example", body); err != nil {
		t.Fatal(err)
	}
	log, err := a.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ing := range log.Ingests {
		for _, r := range ing.Resources {
			if strings.Contains(r, "K7MDENG") || strings.Contains(r, "bPxRfiCYEXAMPLEKEY") {
				t.Fatalf("audit ingest resource carries a fragment of a redactable secret: %q (resources=%q)", r, ing.Resources)
			}
		}
	}
}

// TestRED_ExecutionLockFilesOutliveDeletedSessions: every run of a session
// (CLI run/continue, REPL, serve, Telegram, supervision) goes through
// AcquireExecution, which creates "<dir>/.<id>.run.lock" and never removes
// it. Delete and Cleanup (the retention janitor's path) remove the session
// file, audit log and artifacts, but not the lock file, so the sessions
// directory grows one orphan file per session ever run, without bound.
func TestRED_ExecutionLockFilesOutliveDeletedSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"20261012-lockorphan1", "20261012-lockorphan2"}
	for _, id := range ids {
		if err := store.Save(&Session{ID: id, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
		release, err := store.AcquireExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if err := store.Delete(ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var orphans []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".run.lock") {
			orphans = append(orphans, e.Name())
		}
	}
	if len(orphans) > 0 {
		t.Fatalf("deleted sessions left execution lock files behind (unbounded growth): %v", orphans)
	}
}

// TestRED_SessionBufferPersistedUnredacted: every session save redacts the
// task, message content, reasoning, tool-call arguments, principal prompts,
// decisions and external refs — but not Session.Buffer, the memory tier-2
// turn summaries ("user: <first 200 runes of the prompt>", "agent: ...") that
// the CLI/REPL/serve persist on every end-of-turn save. A token pasted into a
// prompt is redacted in the message but lands verbatim in the buffer line of
// the same session file.
func TestRED_SessionBufferPersistedUnredacted(t *testing.T) {
	secret := "ghp_" + "abcdefghijklmnopqrstuvwxyz1234567890"
	if strings.Contains(redact.RedactSecrets("token "+secret), secret) {
		t.Skip("redactor does not cover this token")
	}
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prompt := "push with my token " + secret
	sess := &Session{
		ID:       "20261012-bufferleak",
		Task:     prompt,
		Messages: []Message{{Role: "user", Content: prompt}},
		Buffer:   []string{"user: " + prompt},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.Path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("session file persists a redactable secret verbatim (buffer line):\n%.600s", raw)
	}
}
