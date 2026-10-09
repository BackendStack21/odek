package session

import "testing"

// Re-adding a reference whose URI carries a secret must not create a
// duplicate: persistence stores the redacted URI, so the comparison has to
// run on the redacted form of both sides.
func TestRED_AddExternalRefsDedupesRedactedURI(t *testing.T) {
	raw := ExternalRef{Kind: "ci-run", URI: "https://ci.example.test/run?token=ghp_abcdefghijklmnopqrstuvwxyz1234567890", CreatedBy: "cli"}
	s := &Session{ID: "20260101-dedupe"}
	if n, err := s.AddExternalRefs(raw); err != nil || n != 1 {
		t.Fatalf("first add: n=%d err=%v", n, err)
	}
	// Simulate what saveLocked persists: the redacted URI.
	s.ExternalRefs = redactExternalRefs(s.ExternalRefs)
	if n, err := s.AddExternalRefs(raw); err != nil || n != 0 {
		t.Fatalf("re-add after save must dedupe: n=%d err=%v refs=%+v", n, err, s.ExternalRefs)
	}
	if len(s.ExternalRefs) != 1 {
		t.Fatalf("refs = %d, want 1", len(s.ExternalRefs))
	}
}
