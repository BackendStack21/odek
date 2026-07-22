package skills

import (
	"strings"
	"testing"
)

// FuzzParseSkillContent feeds random/mutated SKILL.md content into the
// frontmatter/body parser and asserts it never panics. When a skill is
// returned, its invariants must hold: the name passed validation, the body
// is non-empty, and the body hash matches the body.
func FuzzParseSkillContent(f *testing.F) {
	seeds := []string{
		"---\nname: my-skill\ndescription: does things\n---\n\nDo the thing.\n",
		"---\nname: a\nodek:\n  auto_load: true\n  trigger:\n    topic: go test\n    action: run build\n---\nbody here",
		"---\nname: a\nodek:\n  provenance:\n    untrusted: true\n    needs_review: true\n    sources: browser mcp\n---\nbody",
		"---\nname: no-closing-fence\n---\nbody but no end",
		"---\n---\nempty frontmatter",
		"---\nname: a\n---\n",                // empty body
		"---\nname: a\n---\n   \n  \n",       // whitespace-only body
		"no frontmatter at all",              // missing opening fence
		"--\nname: a\n--\nshort fences",      // wrong fence length
		"---\nname: ../traversal\n---\nbody", // path traversal name
		"---\nname: a/b\n---\nbody",          // separator in name
		"---\nname: \"quoted\"\nversion: 1.2\nauthor: x\n---\nbody",
		"---\nname: a\nlist:\n  - one\n  - two\n---\nbody", // unsupported array syntax
		"---\nname: a\nkey: value: extra: colons\n---\nbody",
		"---\nname: a\n\t tab-indented: true\n---\nbody",
		"---\nname: a\nodek:\n  quality: bogus-quality\n---\nbody",
		"---\nname: " + strings.Repeat("n", 10000) + "\n---\nbody",
		"---\n" + strings.Repeat("key: value\n", 5000) + "name: a\n---\nbody",
		"---\nname: a\n---\n" + strings.Repeat("body line\n", 5000),
		"---\nname: a\n---\nbody with --- and ``` fences --- inside",
		"\x00binary\x00garbage",
		"---\nname: a\n---\n\x00\xff\xfe binary body",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		skill := parseSkillContent(content, "fuzz/SKILL.md")
		if skill == nil {
			return
		}
		if err := ValidateSkillName(skill.Name); err != nil {
			t.Fatalf("parsed skill with invalid name %q: %v", skill.Name, err)
		}
		if strings.TrimSpace(skill.Body) == "" {
			t.Fatalf("parsed skill %q with empty body", skill.Name)
		}
		if skill.BodyHash != HashBody(skill.Body) {
			t.Fatalf("parsed skill %q with mismatched body hash", skill.Name)
		}
	})
}

// FuzzParseYAMLMap feeds random frontmatter blocks into the YAML subset
// parser and asserts it never panics.
func FuzzParseYAMLMap(f *testing.F) {
	seeds := []string{
		"name: my-skill\ndescription: does things",
		"odek:\n  auto_load: true\n  trigger:\n    topic: go test",
		"a:\n  b:\n    c:\n      d: deep",
		"key: value: with: colons",
		"key:",
		":",
		"",
		"   \n\t\n",
		"num: 42\nfloat: 3.14\nbool: yes\noff: no",
		"quoted: \"value\"\nsingle: 'value'",
		"# comment only",
		"key: value\n# comment\nother: value2",
		"deep:\n" + strings.Repeat("  ", 100) + "x: y",
		strings.Repeat("key: value\n", 5000),
		"\x00null\x00: bytes",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		m := parseYAMLMap(input)
		if m == nil {
			t.Fatal("parseYAMLMap returned nil map")
		}
	})
}
