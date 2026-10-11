package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRED_AuditPersistsSecretsUnredacted: the per-session audit log lives in
// the session directory next to the (redacted) session files, but
// RecordTurn/RecordIngest write the raw user prompt, ingest source and
// resource list verbatim. A user pasting a token into the prompt, or a
// fetched URL carrying one in its query string, leaves the secret in
// ~/.odek/sessions/audit/<id>.json even though the session file redacts it.
func TestRED_AuditPersistsSecretsUnredacted(t *testing.T) {
	dir := t.TempDir()
	a := NewAuditStore(dir)
	const id = "20261011-aaaaaaaa"
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"

	if err := a.RecordIngest(id, 1, "https://api.example.com/x?token="+secret, "body mentions https://api.example.com/y?key="+secret); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordTurn(id, AuditTurn{
		Turn:               1,
		UserMessage:        "use my token " + secret + " to push",
		NovelResources:     []string{"https://evil.example/?k=" + secret},
		UntrustedResources: []string{"https://evil.example/?k=" + secret},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("audit log persisted the secret verbatim:\n%s", data)
	}
}
