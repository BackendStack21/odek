package session

import (
	"testing"
)

func TestAuditStore_RecordIngestAndLoad(t *testing.T) {
	s := NewAuditStore(t.TempDir())
	sid := "20260101-test01"

	if err := s.RecordIngest(sid, 1, "https://evil.example/x", "page content"); err != nil {
		t.Fatalf("RecordIngest: %v", err)
	}
	if err := s.RecordIngest(sid, 1, "/etc/passwd", "root:x:0:0"); err != nil {
		t.Fatalf("RecordIngest: %v", err)
	}

	log, err := s.Load(sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(log.Ingests) != 2 {
		t.Fatalf("Ingests = %d, want 2", len(log.Ingests))
	}
	if log.Ingests[0].Source != "https://evil.example/x" {
		t.Errorf("first ingest source = %q", log.Ingests[0].Source)
	}
	if log.Ingests[0].ContentHash == "" {
		t.Error("first ingest hash is empty")
	}
}

func TestAuditStore_RejectsInvalidSessionID(t *testing.T) {
	s := NewAuditStore(t.TempDir())
	if err := s.RecordIngest("../../etc/passwd", 1, "x", "y"); err == nil {
		t.Error("expected validation error for path-traversal session ID")
	}
}

func TestResourcesIn_FindsURLsPathsAndExtensions(t *testing.T) {
	text := "please fetch https://x.com/api and read /tmp/foo.txt and main.go"
	got := ResourcesIn(text)
	want := map[string]bool{
		"https://x.com/api": true,
		"/tmp/foo.txt":      true,
		"main.go":           true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing expected matches: %v\nfull got: %v", want, got)
	}
}

func TestResourcesIn_FindsQuotedJSONArguments(t *testing.T) {
	// Tool arguments are JSON-encoded, so paths/URLs appear inside quotes.
	text := `{"path":"README.md","url":"https://x.com/blog"}`
	got := ResourcesIn(text)
	want := map[string]bool{
		"README.md":         true,
		"https://x.com/blog": true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing expected matches: %v\nfull got: %v", want, got)
	}
}

func TestNovelResources_FlagsToolReferencesNotInUserMessage(t *testing.T) {
	user := "summarize the README and the LICENSE"
	tool := "fetched https://evil.example/x and /etc/passwd"
	novel := NovelResources(user, tool)

	wantContains := []string{"https://evil.example/x", "/etc/passwd"}
	for _, w := range wantContains {
		var found bool
		for _, n := range novel {
			if n == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("NovelResources missing %q\ngot: %v", w, novel)
		}
	}
}

func TestNovelResources_DoesNotFlagUserMentionedURLs(t *testing.T) {
	user := "summarise https://x.com/blog"
	tool := "I fetched https://x.com/blog and parsed it"
	novel := NovelResources(user, tool)
	for _, n := range novel {
		if n == "https://x.com/blog" {
			t.Errorf("user-mentioned URL was wrongly flagged as novel: %v", novel)
		}
	}
}

func TestAuditTurn_RoundtripsSuspiciousFlag(t *testing.T) {
	s := NewAuditStore(t.TempDir())
	sid := "20260102-test02"

	turn := AuditTurn{
		Turn:                 1,
		UserMessage:          "summarise README",
		ToolCalls:            []string{"browser", "shell"},
		NovelResources:       []string{"https://attacker.example/x"},
		UntrustedResources:   []string{"README.md"},
		IngestedUntrusted:    true,
		SuspiciousDivergence: true,
	}
	if err := s.RecordTurn(sid, turn); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}
	log, _ := s.Load(sid)
	if len(log.Turns) != 1 || !log.Turns[0].SuspiciousDivergence {
		t.Errorf("turn divergence not persisted: %+v", log.Turns)
	}
	if len(log.Turns[0].UntrustedResources) != 1 || log.Turns[0].UntrustedResources[0] != "README.md" {
		t.Errorf("untrusted resources not persisted: %+v", log.Turns[0].UntrustedResources)
	}
}
