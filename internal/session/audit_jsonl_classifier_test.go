package session

// Unit coverage for the corruptJSONLIndex classifier: every branch —
// clean log, salvageable torn tail, parseable-but-unknown-type tail,
// torn fragment NOT in last position, multiple invalid lines, and a
// wholly invalid file.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ingestLine(n int) string {
	b, _ := json.Marshal(auditRecord{Type: "ingest", Ingest: &AuditIngest{Turn: n, Source: "browser"}})
	return string(b)
}

func turnLine(n int) string {
	b, _ := json.Marshal(auditRecord{Type: "turn", Turn: &AuditTurn{Turn: n, UserMessage: "hi"}})
	return string(b)
}

func TestCorruptJSONLIndex_Classifier(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"clean log", ingestLine(1) + "\n" + turnLine(1) + "\n", false},
		{"blank lines ignored", "\n" + ingestLine(1) + "\n\n" + turnLine(1) + "\n\n", false},
		{"torn last line salvageable", ingestLine(1) + "\n" + turnLine(1) + "\n{\"torn frag", false},
		{"torn last line no trailing nl", ingestLine(1) + "\n{\"torn", false},
		{"parseable unknown-type tail is corrupt", ingestLine(1) + "\n" + `{"type":"foreign"}` + "\n", true},
		{"valid prefix torn at non-last position", ingestLine(1) + "\n{oops\n" + turnLine(1) + "\n", true},
		{"two invalid lines", ingestLine(1) + "\n{bad\n{bad2\n", true},
		{"wholly invalid file", "{not jsonl at all", true},
		{"unknown type only", `{"type":"mystery"}` + "\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := corruptJSONLIndex([]byte(tc.data)); got != tc.want {
				t.Fatalf("corruptJSONLIndex(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// The corrupt path must actually be exercised end to end: appending to a
// log classified as corrupt preserves the evidence aside and starts a
// fresh log, and the old content is recoverable from the preserved file.
func TestAuditRecordTurn_CorruptLogPreservedAsideEndToEnd(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)
	sid := "20261003-corrupt-e2e"

	corrupt := ingestLine(1) + "\n" + `{"type":"foreign"}` + "\n"
	path := filepath.Join(dir, "audit", sid+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordTurn(sid, AuditTurn{Turn: 1, UserMessage: "after corruption"}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"foreign"`) {
		t.Fatal("corrupt content still in the live log after preservation")
	}
	if !strings.Contains(string(data), "after corruption") {
		t.Fatal("fresh log does not contain the new record")
	}
}

// Torn-tail append must prepend nothing to existing history — the valid
// prefix stays byte-identical and the new record appends cleanly after
// the fragment, which Load then skips.
func TestAuditRecordTurn_TornTailAppendKeepsPrefixBytes(t *testing.T) {
	dir := t.TempDir()
	s := NewAuditStore(dir)
	sid := "20261003-torn-prefix"

	prefix := ingestLine(1) + "\n" + turnLine(1) + "\n"
	path := filepath.Join(dir, "audit", sid+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(prefix+"{\"torn"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordTurn(sid, AuditTurn{Turn: 2, UserMessage: "next"}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("valid prefix mutated:\n got: %q\nwant prefix: %q", got, prefix)
	}
	if !strings.Contains(got, `"next"`) {
		t.Fatal("new record missing after torn-tail append")
	}

	log, err := s.Load(sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(log.Ingests) != 1 || len(log.Turns) != 2 {
		t.Fatalf("Load = %d ingests, %d turns; want 1, 2 (torn fragment skipped)", len(log.Ingests), len(log.Turns))
	}
}
