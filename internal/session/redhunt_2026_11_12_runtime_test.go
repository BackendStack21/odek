package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An escalation record is a valid audit record: a later append from a fresh
// store (a new process: `odek continue`, a restarted serve/Telegram) must
// keep the log, not quarantine it as corrupt and start an empty one. The
// audit log is the post-hoc evidence of a prompt injection; an escalation is
// recorded exactly when untrusted content steered a tool call.
func TestRED_AuditEscalationRecordQuarantinesWholeLogOnNextProcess(t *testing.T) {
	dir := t.TempDir()
	const id = "redhunt-runtime-audit"

	first := NewAuditStore(dir)
	if err := first.RecordIngest(id, 1, "https://news.example.com/page", "see https://collector.example.net/x"); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordEscalation(id, AuditEscalation{
		Turn: 1, Tool: "shell", Kind: "url",
		Span:     "https://collector.example.net/x",
		Source:   "https://news.example.com/page",
		Resource: "curl https://collector.example.net/x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordTurn(id, AuditTurn{Turn: 1, UserMessage: "summarise the page", ToolCalls: []string{"browser", "shell"}, IngestedUntrusted: true, SuspiciousDivergence: true}); err != nil {
		t.Fatal(err)
	}

	// A new process: a fresh AuditStore with no validated-state memo.
	second := NewAuditStore(dir)
	if err := second.RecordIngest(id, 2, "https://other.example.org/", "body"); err != nil {
		t.Fatal(err)
	}

	log, err := second.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "audit"))
	var quarantined []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			quarantined = append(quarantined, e.Name())
		}
	}
	if len(log.Ingests) != 2 || len(log.Turns) != 1 || len(log.Escalations) != 1 || len(quarantined) != 0 {
		t.Fatalf("audit history lost after a valid escalation record: ingests=%d (want 2) turns=%d (want 1) escalations=%d (want 1), quarantined=%v",
			len(log.Ingests), len(log.Turns), len(log.Escalations), quarantined)
	}
}

// Every record type the package can write must survive a cold reopen, and a
// record of an unknown type must still count as corruption.
func TestRED_AuditEveryRecordTypeSurvivesColdReopen(t *testing.T) {
	for _, typ := range auditRecordTypes {
		line := []byte(`{"type":"` + typ + `"}` + "\n")
		if corruptJSONLIndex(append(append([]byte{}, line...), line...)) {
			t.Errorf("record type %q treated as corrupt", typ)
		}
		if isLegacyAuditJSON(line) {
			t.Errorf("record type %q mistaken for legacy format", typ)
		}
	}
	unknown := []byte(`{"type":"bogus"}` + "\n" + `{"type":"bogus"}` + "\n")
	if !corruptJSONLIndex(unknown) {
		t.Error("unknown record type accepted")
	}
}
