package skills

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRED_ValidateSkillName_AcceptsCommonPunctuationAndMarks(t *testing.T) {
	body := "## Overview\nbody long enough to parse back\n"
	for _, name := range []string{
		"c++", "c# tools", "notes@work", "build (fast)", "rock'n'roll", "R&D", "naïve", "हिन्दी-notes",
	} {
		if err := ValidateSkillName(name); err != nil {
			t.Errorf("ValidateSkillName(%q) = %v, want nil", name, err)
			continue
		}
		parsed := parseSkillContent(MarshalSkill(Skill{Name: name, Body: body}), "")
		if parsed == nil || parsed.Name != name {
			t.Errorf("name %q does not round-trip through SKILL.md: %+v", name, parsed)
		}
	}
}

func TestRED_LoaderWarnsOncePerRejectedName(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	dir := t.TempDir()
	p := filepath.Join(dir, "bad", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: bad;name\ndescription: x\n---\n\n" + catalogTestBody()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if parseSkillFile(p) != nil {
			t.Fatal("invalid name loaded")
		}
	}
	out := buf.String()
	if strings.Count(out, p) != 1 || !strings.Contains(out, "disallowed character") {
		t.Errorf("want exactly one warning naming the path and reason, got:\n%s", out)
	}
}
