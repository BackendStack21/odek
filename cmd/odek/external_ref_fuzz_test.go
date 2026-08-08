package main

import (
	"strings"
	"testing"
	"unicode"
)

// FuzzParseExternalRefFlag feeds arbitrary --external-ref values into the
// CLI parser and asserts it never panics, never accepts a spec whose parsed
// ref would fail session.ExternalRef.Validate (the parser validates
// internally), and that the two accepted forms round-trip sanely.
func FuzzParseExternalRefFlag(f *testing.F) {
	seeds := []string{
		"ci-run=https://ci.example.test/runs/4821",
		"kind=ci-run,uri=https://ci.example.test/runs/4821,created_by=example-app",
		"kind=ci-run,uri=https://ci.example.test/runs/4821,created_by=example-app,read_only=true",
		"kind=ci-run,uri=https://ci.example.test/runs/4821,created_by=example-app,read_only=false",
		"kind=a,uri=b,created_by=c,read_only=TRUE",
		"kind=a,uri=b,created_by=c,read_only=1",
		"kind=a,uri=b,created_by=c,read_only=maybe",
		"kind=,uri=,created_by=",
		"kind=a",
		"=uri-only",
		"kind=a,uri=b",
		"kind=a,uri=b,created_by=c,unknown=d",
		"kind=a,,uri=b,created_by=c",
		"kind=a,uri=b=c=d,created_by=e",
		"a=b,c",
		"",
		"=",
		",",
		"kind=a,uri=with\x00nul,created_by=c",
		"kind=a,uri=with\nnewline,created_by=c",
		"Kind=Upper,uri=u,created_by=c",
		"kind=a,uri=" + strings.Repeat("u", 3000) + ",created_by=c",
		"no-equals-sign",
		"kind = a,uri = b,created_by = c",
		"KIND=a,URI=b,CREATED_BY=c",
		"kind=a,uri=b,created_by=c,read_only=true,read_only=false",
		"kind=a,uri=b,kind=c,created_by=d",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		ref, err := parseExternalRefFlag(spec)
		if err != nil {
			return
		}
		// Accepted — the ref must satisfy the session-level validation.
		if verr := ref.Validate(); verr != nil {
			t.Fatalf("parser accepted spec %q producing invalid ref %+v: %v", spec, ref, verr)
		}
		// And every documented invariant directly.
		if len(ref.Kind) < 1 || len(ref.Kind) > 64 {
			t.Fatalf("spec %q accepted with kind length %d", spec, len(ref.Kind))
		}
		for _, c := range ref.URI {
			if unicode.IsControl(c) {
				t.Fatalf("spec %q accepted with control character U+%04X in uri", spec, c)
			}
		}
		if len(ref.CreatedBy) < 1 || len(ref.CreatedBy) > 128 {
			t.Fatalf("spec %q accepted with created_by length %d", spec, len(ref.CreatedBy))
		}
	})
}
