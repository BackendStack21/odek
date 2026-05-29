package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// captureStderrDuring uses the existing captureStderr helper (from
// sandbox_test.go, which returns a flush closure) and runs fn inside.
func captureStderrDuring(t *testing.T, fn func()) string {
	t.Helper()
	flush := captureStderr(t)
	fn()
	return flush()
}

// withTempHome redirects HOME to a fresh tempdir so session.NewStore
// writes under a sandbox path.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestPrintAuditUsage_OutputsKeyTokens(t *testing.T) {
	out := captureStdout(printAuditUsage)
	for _, want := range []string{
		"odek audit",
		"<session-id>",
		"--list",
		"audit log",
		"suspicious",
		"JSON",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestAuditCmd_NoArgs_PrintsUsageAndErrors(t *testing.T) {
	withTempHome(t)
	out := captureStdout(func() {
		err := auditCmd(nil)
		if err == nil {
			t.Error("expected error when called with no args")
		} else if !strings.Contains(err.Error(), "argument required") {
			t.Errorf("error = %v, want 'argument required'", err)
		}
	})
	if !strings.Contains(out, "odek audit") {
		t.Errorf("usage should have been printed, got:\n%s", out)
	}
}

func TestAuditCmd_Help_PrintsUsage(t *testing.T) {
	withTempHome(t)
	for _, flag := range []string{"--help", "-h", "help"} {
		t.Run(flag, func(t *testing.T) {
			out := captureStdout(func() {
				if err := auditCmd([]string{flag}); err != nil {
					t.Fatalf("auditCmd(%q): %v", flag, err)
				}
			})
			if !strings.Contains(out, "odek audit") {
				t.Errorf("usage missing from %q output:\n%s", flag, out)
			}
		})
	}
}

func TestAuditCmd_LoadByID_NoSuchSession(t *testing.T) {
	withTempHome(t)
	// AuditStore.Load returns empty AuditLog when the file is missing,
	// not an error — so auditCmd should succeed and print an empty log.
	out := captureStdout(func() {
		if err := auditCmd([]string{"20260529-deadbe"}); err != nil {
			t.Fatalf("auditCmd: %v", err)
		}
	})
	// Empty log marshals with a session_id field set but ingests/turns null.
	if !strings.Contains(out, "\"session_id\"") {
		t.Errorf("expected JSON with session_id key, got:\n%s", out)
	}
	if !strings.Contains(out, "20260529-deadbe") {
		t.Errorf("expected the session ID echoed in the JSON, got:\n%s", out)
	}
}

func TestAuditCmd_LoadByID_InvalidID(t *testing.T) {
	withTempHome(t)
	err := auditCmd([]string{"../etc/passwd"})
	if err == nil {
		t.Fatal("expected error for path-traversal ID")
	}
	if !strings.Contains(err.Error(), "audit:") {
		t.Errorf("error should be wrapped with 'audit:', got: %v", err)
	}
}

func TestAuditCmd_List_EmptyStore(t *testing.T) {
	withTempHome(t)
	// No sessions yet → header on stderr, no rows on stdout, no error.
	stderr := captureStderrDuring(t, func() {
		if err := auditCmd([]string{"--list"}); err != nil {
			t.Fatalf("auditCmd --list: %v", err)
		}
	})
	if !strings.Contains(stderr, "Session") || !strings.Contains(stderr, "Ingests") {
		t.Errorf("expected header on stderr, got:\n%s", stderr)
	}
}

func TestAuditCmd_LoadByID_RoundtripWithRecorded(t *testing.T) {
	withTempHome(t)

	// Stand up a real session + audit log so auditCmd has something to load.
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	auditStore := session.NewAuditStore(store.Dir())

	const sid = "20260529-abc001"
	if err := auditStore.RecordIngest(sid, 1, "https://example.com", "hello"); err != nil {
		t.Fatalf("RecordIngest: %v", err)
	}
	if err := auditStore.RecordTurn(sid, session.AuditTurn{
		Turn:                 1,
		UserMessage:          "do thing",
		ToolCalls:            []string{"shell"},
		IngestedUntrusted:    true,
		SuspiciousDivergence: false,
	}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	out := captureStdout(func() {
		if err := auditCmd([]string{sid}); err != nil {
			t.Fatalf("auditCmd: %v", err)
		}
	})
	for _, want := range []string{
		sid,
		"https://example.com",
		"\"ingested_untrusted\": true",
		"\"tool_calls\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit dump missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestAuditList_PopulatedSession(t *testing.T) {
	withTempHome(t)

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Save a real session so store.List returns something.
	sess := session.Session{
		ID:    "20260529-listme",
		Task:  "test",
		Turns: 1,
	}
	if err := store.Save(&sess); err != nil {
		t.Fatalf("Save session: %v", err)
	}

	auditStore := session.NewAuditStore(store.Dir())
	// Long source string to exercise the truncation branch.
	longSource := strings.Repeat("a", 60)
	if err := auditStore.RecordIngest(sess.ID, 1, longSource, "data"); err != nil {
		t.Fatalf("RecordIngest: %v", err)
	}
	if err := auditStore.RecordTurn(sess.ID, session.AuditTurn{
		Turn:                 1,
		IngestedUntrusted:    true,
		SuspiciousDivergence: true,
	}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	// auditList writes the header to stderr and the rows to stdout, so
	// capture both. The order is: open stderr capture, then run the
	// stdout capture (which executes the function).
	flushStderr := captureStderr(t)
	stdout := captureStdout(func() {
		if err := auditList(store, auditStore); err != nil {
			t.Fatalf("auditList: %v", err)
		}
	})
	stderr := flushStderr()
	combined := stderr + stdout

	if !strings.Contains(combined, sess.ID) {
		t.Errorf("auditList should list session %q\nstderr:\n%s\nstdout:\n%s", sess.ID, stderr, stdout)
	}
	if !strings.Contains(combined, "...") {
		t.Errorf("long source should have been truncated with '...'\nstderr:\n%s\nstdout:\n%s", stderr, stdout)
	}
}
