package main

import "testing"

func TestParseExternalRefFlag_ShorthandWithCommasAndEquals(t *testing.T) {
	for _, tc := range []struct{ spec, kind, uri string }{
		{"ci-run=https://ci.example.test/runs?ids=1,2", "ci-run", "https://ci.example.test/runs?ids=1,2"},
		{"ci-run=https://ci.example.test/runs?a=1,b=2", "ci-run", "https://ci.example.test/runs?a=1,b=2"},
		{"ci-run=https://ci.example.test/runs?a=1", "ci-run", "https://ci.example.test/runs?a=1"},
	} {
		ref, err := parseExternalRefFlag(tc.spec)
		if err != nil {
			t.Errorf("%q: %v", tc.spec, err)
			continue
		}
		if ref.Kind != tc.kind || ref.URI != tc.uri || ref.CreatedBy != "cli" {
			t.Errorf("%q: got %+v", tc.spec, ref)
		}
	}
}

func TestParseExternalRefFlag_LongFormStillStrict(t *testing.T) {
	ref, err := parseExternalRefFlag("kind=ci-run,uri=https://ci.example.test/runs/1,created_by=me,read_only=true")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != "ci-run" || ref.CreatedBy != "me" || !ref.ReadOnly {
		t.Fatalf("got %+v", ref)
	}
	for _, bad := range []string{
		"kind=ci-run,uri",
		"kind=ci-run,uri=https://x.test/1,bogus=1",
		"kind=ci-run,uri=https://x.test/1,read_only=maybe",
	} {
		if _, err := parseExternalRefFlag(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
