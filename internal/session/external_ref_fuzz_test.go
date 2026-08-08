package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzExternalRefValidate feeds arbitrary kind/uri/created_by strings into
// ExternalRef.Validate and asserts it never panics and never accepts a ref
// that violates the documented bounds (kind charset/length, uri length and
// control characters, created_by length).
func FuzzExternalRefValidate(f *testing.F) {
	type seed struct {
		kind, uri, createdBy string
	}
	seeds := []seed{
		{"ci-run", "https://ci.example.test/runs/4821", "example-app"},
		{"opaque_application_state", "app://workflow/123", "cli"},
		{"a", "x", "y"},
		{"", "https://example.test", "cli"},
		{"kind", "", "cli"},
		{"kind", "https://example.test", ""},
		{"Kind-Upper", "https://example.test", "cli"},
		{"kind with space", "https://example.test", "cli"},
		{"kind\twith\ttab", "https://example.test", "cli"},
		{"kind", "https://example.test/\x00nul", "cli"},
		{"kind", "https://example.test/\nnewline", "cli"},
		{"kind", "https://example.test/\x7f", "cli"},
		{"kind", "https://example.test/日本語", "cli"},
		{"émoji-kind", "https://example.test", "cli"},
		{strings.Repeat("k", 64), "https://example.test", "cli"},
		{strings.Repeat("k", 65), "https://example.test", "cli"},
		{"kind", strings.Repeat("u", 2048), "cli"},
		{"kind", strings.Repeat("u", 2049), "cli"},
		{"kind", "https://example.test", strings.Repeat("c", 128)},
		{"kind", "https://example.test", strings.Repeat("c", 129)},
		{"kind", "file:///etc/passwd", "cli"},
		{"kind", "../../etc/passwd", "cli"},
		{"k", "\xf0\x28\x8c\x28", "c"}, // invalid UTF-8
	}
	for _, s := range seeds {
		f.Add(s.kind, s.uri, s.createdBy)
	}

	f.Fuzz(func(t *testing.T, kind, uri, createdBy string) {
		ref := ExternalRef{Kind: kind, URI: uri, CreatedBy: createdBy}
		err := ref.Validate()
		if err != nil {
			return
		}
		// Accepted — every documented invariant must hold.
		if len(ref.Kind) < 1 || len(ref.Kind) > 64 {
			t.Fatalf("accepted kind of length %d", len(ref.Kind))
		}
		for _, c := range ref.Kind {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				t.Fatalf("accepted kind %q containing %q", ref.Kind, c)
			}
		}
		if len(ref.URI) < 1 || len(ref.URI) > 2048 {
			t.Fatalf("accepted uri of length %d", len(ref.URI))
		}
		for _, c := range ref.URI {
			if unicode.IsControl(c) {
				t.Fatalf("accepted uri containing control character U+%04X", c)
			}
		}
		if len(ref.CreatedBy) < 1 || len(ref.CreatedBy) > 128 {
			t.Fatalf("accepted created_by of length %d", len(ref.CreatedBy))
		}
		// A validated ref must survive a JSON round-trip unchanged.
		// (encoding/json replaces invalid UTF-8 with U+FFFD on marshal, so
		// the equality assertion only holds for valid-UTF-8 strings; invalid
		// UTF-8 in an opaque, never-dereferenced URI is not a validation
		// bypass, so it is not rejected.)
		if !utf8.ValidString(ref.Kind) || !utf8.ValidString(ref.URI) || !utf8.ValidString(ref.CreatedBy) {
			return
		}
		data, merr := json.Marshal(ref)
		if merr != nil {
			t.Fatalf("marshal of validated ref failed: %v", merr)
		}
		var back ExternalRef
		if uerr := json.Unmarshal(data, &back); uerr != nil {
			t.Fatalf("unmarshal of validated ref failed: %v", uerr)
		}
		if back.Kind != ref.Kind || back.URI != ref.URI || back.CreatedBy != ref.CreatedBy {
			t.Fatalf("JSON round-trip changed validated ref: %+v → %+v", ref, back)
		}
	})
}
