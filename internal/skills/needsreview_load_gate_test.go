package skills

import (
	"strings"
	"testing"
)

// RED: the promotion gate only blocked trigger matching — skill_load served
// the full body of any NeedsReview-pinned skill on demand, so the agent could
// pull tainted instructions while the human gate believed the skill inert.
// skill_load must refuse pinned skills with an error naming the promote
// command; the clean path must keep working.
func TestRED_SkillLoadTool_RefusesNeedsReviewSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "clean-skill",
		"name: clean-skill\ndescription: clean\n",
		"## Overview\nplain body for the clean skill\n## Common Pitfalls\nnone\n")
	writeSkillFile(t, dir, "tainted-skill",
		"name: tainted-skill\ndescription: tainted\nodek:\n  provenance:\n    untrusted: true\n    needs_review: true\n",
		"## Overview\nTAINTED BODY MARKER that must never reach the agent\n## Common Pitfalls\nnone\n")

	sm := NewSkillManager(dir, "")
	tool := &SkillLoadTool{Manager: sm}

	// Confirm the fixture actually pins the skill (guards the test itself
	// against frontmatter drift).
	pinned := false
	for _, s := range sm.AllSkills() {
		if s.Name == "tainted-skill" && s.Provenance.NeedsReview {
			pinned = true
		}
	}
	if !pinned {
		t.Fatal("fixture: tainted-skill did not scan as NeedsReview")
	}

	out, err := tool.Call(`{"name": "tainted-skill"}`)
	if err == nil {
		t.Fatalf("skill_load served a NeedsReview skill body: %.120s", out)
	}
	if !strings.Contains(err.Error(), "promote") {
		t.Errorf("error should name the promote command, got: %v", err)
	}
	if strings.Contains(out, "TAINTED BODY MARKER") {
		t.Errorf("tainted body leaked in output: %.120s", out)
	}

	// The clean skill must still load normally — the gate targets pinned
	// skills only.
	clean, err := tool.Call(`{"name": "clean-skill"}`)
	if err != nil {
		t.Fatalf("clean skill should still load: %v", err)
	}
	if !strings.Contains(clean, "plain body for the clean skill") {
		t.Errorf("clean skill body missing: %.120s", clean)
	}
}

// The listing stays metadata-visible by design (listing and promotion still
// surface pinned skills) but must carry a pending-review marker pointing at
// the promote path, so the agent can relay the gate to the operator instead
// of probing for the body.
func TestSkillListTool_MarksNeedsReviewSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "tainted-skill",
		"name: tainted-skill\ndescription: tainted\nodek:\n  provenance:\n    needs_review: true\n",
		"## Overview\ntainted body\n")
	sm := NewSkillManager(dir, "")
	tool := &SkillListTool{Manager: sm}

	result, err := tool.Call(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "tainted-skill") {
		t.Errorf("listing should keep NeedsReview skills metadata-visible: %s", result)
	}
	if !strings.Contains(result, "[needs review]") {
		t.Errorf("listing should mark NeedsReview skills: %s", result)
	}
	if !strings.Contains(result, "odek skill promote tainted-skill") {
		t.Errorf("listing should point at the promote command: %s", result)
	}
}

// Both tool descriptions must document the gate so the model learns the
// contract without probing a pinned skill.
func TestSkillToolDescriptions_DocumentNeedsReviewGate(t *testing.T) {
	if d := (&SkillLoadTool{}).Description(); !strings.Contains(d, "promote") {
		t.Errorf("skill_load description should document the NeedsReview gate: %s", d)
	}
	if d := (&SkillListTool{}).Description(); !strings.Contains(d, "promote") {
		t.Errorf("skill_list description should document the NeedsReview gate: %s", d)
	}
}
