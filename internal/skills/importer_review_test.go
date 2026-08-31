package skills

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bug-sweep 2026-08-31 findings (verified against importer.go / loader.go):
//
//  1. ImportSkill never pinned NeedsReview — a URI-imported skill became
//     trigger-matchable immediately, with DeriveKeywords building triggers
//     from the attacker-controlled body. The documented invariant is that
//     untrusted-source skills stay pinned until `odek skill promote --force`.
//  2. fetchHTTP checked isPrivateHost only on redirects — the INITIAL fetch
//     was unchecked, so `odek skill import http://169.254.169.254/...` is a
//     direct SSRF (cloud metadata, internal services).

func serveSkill(t *testing.T, frontmatter string) *httptest.Server {
	t.Helper()
	body := "---\n" + frontmatter + "---\n\n## Overview\n\nimported body\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func writeImportFixture(t *testing.T, frontmatter string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skill.md")
	body := "---\n" + frontmatter + "---\n\n## Overview\n\nimported body\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return "file://" + path
}

func TestImportSkill_PinsNeedsReview(t *testing.T) {
	uri := writeImportFixture(t, `name: pin-review
description: imported skill
`)
	dir := t.TempDir()
	res, err := ImportSkill(ImportOptions{
		URI:      uri,
		MaxBytes: 1 << 20,
		Timeout:  5,
		UserDir:  dir,
		AutoYes:  true,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skill.Provenance.NeedsReview {
		t.Fatal("imported skill must be pinned NeedsReview (untrusted source)")
	}
	// The pin must survive save: frontmatter carries needs_review: true.
	data, err := os.ReadFile(filepath.Join(dir, "pin-review", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "needs_review: true") {
		t.Errorf("saved SKILL.md missing needs_review: true:\n%s", data)
	}
}

func TestImportSkill_AttackerCannotClearNeedsReview(t *testing.T) {
	// A remote author setting needs_review: false in their own frontmatter
	// must not clear the pin applied by the importer.
	uri := writeImportFixture(t, `name: sneaky
description: imported skill
odek:
  provenance:
    needs_review: false
`)
	dir := t.TempDir()
	res, err := ImportSkill(ImportOptions{
		URI:      uri,
		MaxBytes: 1 << 20,
		Timeout:  5,
		UserDir:  dir,
		AutoYes:  true,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skill.Provenance.NeedsReview {
		t.Fatal("remote needs_review: false must not clear the import pin")
	}
}

func TestFetchHTTP_RefusesPrivateHostOnInitialFetch(t *testing.T) {
	// httptest listens on the loopback interface — exactly the class of
	// address the initial-fetch check must refuse (redirects were already
	// checked; the direct fetch was the SSRF hole).
	_, err := fetchHTTP("http://127.0.0.1:1/skill.md", 1<<20, 2)
	if err == nil {
		t.Fatal("fetchHTTP must refuse private/loopback hosts on the initial fetch")
	}
	if !strings.Contains(err.Error(), "private") {
		t.Errorf("error should name the private-host refusal, got: %v", err)
	}
}

func TestFetchHTTPAllow_PrivateHostPermittedForExplicitCallers(t *testing.T) {
	// The internal escape hatch (tests, explicit operator tooling) still
	// reaches loopback servers.
	server := serveSkill(t, `name: local-dev
description: loopback import
`)
	result, err := fetchHTTPAllow(server.URL+"/skill.md", 1<<20, 5, true)
	if err != nil {
		t.Fatalf("explicit allow-private fetch failed: %v", err)
	}
	if !strings.Contains(result.Content, "local-dev") {
		t.Errorf("unexpected content: %q", result.Content)
	}
}
