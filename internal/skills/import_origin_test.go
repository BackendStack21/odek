package skills

import (
	"strings"
	"testing"
)

// Imported skills are untrusted by origin; docs require `promote --force`
// (plain promote refuses when Untrusted or Sources set). The importer must
// therefore record the origin so plain promote cannot clear the pin.
func TestRED_ImportRecordsUntrustedOrigin(t *testing.T) {
	uri := writeImportFixture(t, "name: orig-check\ndescription: imported skill\n")
	dir := t.TempDir()
	res, err := ImportSkill(ImportOptions{URI: uri, UserDir: dir, BasicOnly: true, AutoYes: true, MaxBytes: 1 << 20, Timeout: 5}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := res.Skill.Provenance
	if !p.Untrusted && len(p.Sources) == 0 {
		t.Fatalf("imported skill carries no untrusted/sources marker; plain `odek skill promote` would clear it without --force: %+v", p)
	}
	s := parseSkillFile(res.Path)
	if s == nil || (!s.Provenance.Untrusted && len(s.Provenance.Sources) == 0) {
		t.Fatalf("on-disk imported skill lacks origin marker: %+v", s)
	}
}

func TestImportDiscardsRemoteProvenanceAndRecordsURI(t *testing.T) {
	uri := writeImportFixture(t, "name: prov-check\ndescription: imported skill\nodek:\n  provenance:\n    untrusted: false\n    needs_review: false\n    sources: trusted-looking\n")
	dir := t.TempDir()
	res, err := ImportSkill(ImportOptions{URI: uri, UserDir: dir, BasicOnly: true, AutoYes: true, MaxBytes: 1 << 20, Timeout: 5}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := parseSkillFile(res.Path)
	if s == nil {
		t.Fatal("reparse failed")
	}
	if !s.Provenance.Untrusted || !s.Provenance.NeedsReview {
		t.Fatalf("remote frontmatter cleared provenance: %+v", s.Provenance)
	}
	if len(s.Provenance.Sources) != 1 || strings.Contains(s.Provenance.Sources[0], "trusted-looking") {
		t.Fatalf("sources not replaced by the import URI: %v", s.Provenance.Sources)
	}
}

func TestImportSourceMarker(t *testing.T) {
	if got := importSourceMarker(" https://x/y z "); got != "https://x/yz" {
		t.Errorf("whitespace not stripped: %q", got)
	}
	if got := importSourceMarker(""); got != "import" {
		t.Errorf("empty uri: %q", got)
	}
	if got := importSourceMarker(strings.Repeat("a", 600)); len(got) != 512 {
		t.Errorf("not bounded: %d", len(got))
	}
}
