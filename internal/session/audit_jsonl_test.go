package session

// Tests for the append-only JSONL audit-log format. RecordIngest and
// RecordTurn must append without rewriting existing history; legacy
// pretty-printed JSON files written by older versions must still load
// and migrate on the next write; a torn trailing line (crash mid-append)
// must be salvaged rather than destroying the valid prefix.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func auditPath(t *testing.T, dir, sessionID string) string {
	t.Helper()
	auditDir := filepath.Join(dir, "audit")
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(auditDir, sessionID+".json")
}

func writeLegacyAudit(t *testing.T, dir, sessionID string, log AuditLog) {
	t.Helper()
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath(t, dir, sessionID), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestAuditStore_LegacyJSONMigratesToJSONL: a legacy pretty-printed audit
// file must remain readable and, after a new record, be migrated to the
// append-only JSONL format without losing any entries.
func TestAuditStore_LegacyJSONMigratesToJSONL(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)
	sid := "20261001-audit-j1"

	writeLegacyAudit(t, dir, sid, AuditLog{
		SessionID: sid,
		Ingests:   []AuditIngest{{Turn: 1, Source: "legacy", ContentHash: "abc"}},
	})

	if err := s.RecordIngest(sid, 2, "https://new.example/x", "body"); err != nil {
		t.Fatalf("RecordIngest on legacy file: %v", err)
	}

	log, err := s.Load(sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(log.Ingests) != 2 {
		t.Fatalf("Ingests = %d, want 2 (legacy + appended)", len(log.Ingests))
	}
	if log.Ingests[0].Source != "legacy" || log.Ingests[1].Source != "https://new.example/x" {
		t.Fatalf("unexpected ingest order: %+v", log.Ingests)
	}

	// The file must now be line-oriented JSON: every non-empty line parses
	// as a standalone record, and the legacy whole-file form no longer fits.
	data, err := os.ReadFile(auditPath(t, dir, sid))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected migrated JSONL file with >=2 lines, got %d", len(lines))
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not standalone JSON: %v (%q)", i, err, line)
		}
	}

	// And the next append must not rewrite the earlier lines.
	prefix := string(data)
	if err := s.RecordIngest(sid, 3, "/tmp/third", "more"); err != nil {
		t.Fatalf("RecordIngest #2: %v", err)
	}
	data2, _ := os.ReadFile(auditPath(t, dir, sid))
	if !strings.HasPrefix(string(data2), prefix) {
		t.Fatal("append rewrote previously written JSONL lines")
	}
}

// TestAuditStore_TornJSONLTailSalvaged: a crash mid-append leaves a partial
// final line. The valid prefix must survive and the torn record be dropped
// — not the whole log treated as corrupt.
func TestAuditStore_TornJSONLTailSalvaged(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)
	sid := "20261001-audit-j2"

	// Seed via the API so the file is canonical JSONL with two ingests.
	if err := s.RecordIngest(sid, 1, "https://a.example/x", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIngest(sid, 1, "/etc/b", "b"); err != nil {
		t.Fatal(err)
	}

	// Simulate a torn append of a turn record.
	path := auditPath(t, dir, sid)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"tur`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := s.RecordTurn(sid, AuditTurn{Turn: 2, UserMessage: "after crash"}); err != nil {
		t.Fatalf("RecordTurn on torn log: %v", err)
	}
	log, err := s.Load(sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(log.Ingests) != 2 {
		t.Fatalf("Ingests = %d, want 2 — torn append destroyed valid history", len(log.Ingests))
	}
	if len(log.Turns) != 1 || log.Turns[0].UserMessage != "after crash" {
		t.Fatalf("Turns = %+v, want the post-crash turn", log.Turns)
	}
}
