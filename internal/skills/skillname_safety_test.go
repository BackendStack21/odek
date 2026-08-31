package skills

import (
	"strings"
	"testing"
)

// RED: ValidateSkillName missed control characters — a newline in a name
// injects frontmatter lines when the name is serialized into SKILL.md
// ("evil\ninjected: true" materializes a fake key on reload) — and
// YAML-special leading characters that corrupt frontmatter or collide with
// CLI flag parsing ("-x" reads as a flag to `odek skill promote -x`).
func TestRED_ValidateSkillName_RejectsNewlinesAndYAMLSpecials(t *testing.T) {
	rejected := []string{
		"line one\ninjected: true", // newline → frontmatter key injection
		"carriage\rreturn",
		"tab\tname",
		"nul\x00byte",
		"del\x7fname",
		"-leading-dash",
		":leading-colon",
		"#leading-hash",
		"\"leading-quote",
		"'leading-quote",
		"[leading-bracket",
		"{leading-brace",
		"trailing space ",
		"double  space", // would silently collapse on the marshal round-trip
	}
	for _, name := range rejected {
		if err := ValidateSkillName(name); err == nil {
			t.Errorf("ValidateSkillName(%q) = nil, want error", name)
		}
	}

	valid := []string{"my-skill", "deploy_script", "skill.v2", "My Skill", "123-start", "k8s:deploy"}
	for _, name := range valid {
		if err := ValidateSkillName(name); err != nil {
			t.Errorf("ValidateSkillName(%q) = %v, want nil", name, err)
		}
	}
}

// RED: MarshalSkill wrote the name unquoted, so a name carrying
// YAML-significant characters (leading dash/colon, ": ", trailing colon)
// either failed to round-trip or corrupted frontmatter. The name must be
// serialized with the same yamlSafeScalar quoting used for description and
// version, and validation-passing names must round-trip exactly through
// parseSkillContent.
func TestRED_MarshalSkill_QuotesYAMLSpecialNames(t *testing.T) {
	body := "## Overview\nbody long enough to parse back\n"
	cases := []struct {
		name     string
		wantLine string
	}{
		{"-leading-dash", `name: "-leading-dash"`},
		{":leading-colon", `name: ":leading-colon"`},
		{"trailing-colon:", `name: "trailing-colon:"`},
		{"weird: name", `name: "weird: name"`},
	}
	for _, c := range cases {
		out := MarshalSkill(Skill{Name: c.name, Body: body})
		if !containsLine(out, c.wantLine) {
			t.Errorf("MarshalSkill(name=%q) missing quoted line %s; got:\n%s", c.name, c.wantLine, out)
		}
		// Names that pass validation must round-trip exactly; rejected
		// names are refused at parse time instead of loading mangled.
		if err := ValidateSkillName(c.name); err == nil {
			parsed := parseSkillContent(out, "")
			if parsed == nil {
				t.Errorf("round-trip failed for name %q (parse = nil)", c.name)
				continue
			}
			if parsed.Name != c.name {
				t.Errorf("round-trip name = %q, want %q", parsed.Name, c.name)
			}
		}
	}
}

// RED: even before validation is consulted, the serializer itself must not
// emit raw newline-carried frontmatter from a name — the newline is
// collapsed into a quoted single-line scalar so no fake key can materialize
// on reload.
func TestRED_MarshalSkill_NameNewlineCannotInjectFrontmatter(t *testing.T) {
	out := MarshalSkill(Skill{Name: "evil\ninjected: true", Body: "## Overview\nbody\n"})
	for _, line := range strings.Split(out, "\n") {
		if line == "injected: true" {
			t.Fatalf("newline in skill name materialized a frontmatter key:\n%s", out)
		}
	}
	if parsed := parseSkillContent(out, ""); parsed != nil {
		// The second fragment must stay part of the (collapsed) name — if
		// the parse kept only "evil", the injected fragment became a key.
		if !strings.Contains(parsed.Name, "injected") {
			t.Errorf("parse kept only %q — the injected fragment became a frontmatter key", parsed.Name)
		}
	}
}

// containsLine reports whether s contains want as an exact line.
func containsLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == want {
			return true
		}
	}
	return false
}
