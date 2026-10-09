package main

import "testing"

func TestRED_ExternalRefShorthandURIWithComma(t *testing.T) {
	ref, err := parseExternalRefFlag("ci-run=https://ci.example.test/runs?ids=1,2")
	if err != nil {
		t.Fatalf("shorthand kind=uri with a comma in the URI rejected: %v", err)
	}
	if ref.URI != "https://ci.example.test/runs?ids=1,2" {
		t.Fatalf("uri = %q", ref.URI)
	}
}
