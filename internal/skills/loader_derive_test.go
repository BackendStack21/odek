package skills

import "testing"

// Keyword derivation must run when the TRIGGER section is absent — not
// only when the whole odek section is absent. A skill that sets any other
// odek field (e.g. quality) without an explicit trigger previously got
// ZERO keywords: trigger matching silently degraded to description-only
// scoring.
func TestParseSkillContent_DerivesKeywordsWhenTriggerAbsent(t *testing.T) {
	content := `---
name: derive-skill
description: Deploy services to production.
odek:
  quality: manual
---

Deploy the service with zero-downtime rolling updates. Run database
migrations before switching traffic. Verify health endpoints after
deployment.
`
	skill := parseSkillContent(content, "derive-skill/SKILL.md")
	if skill == nil {
		t.Fatal("skill failed to parse")
	}
	if len(skill.Trigger.TopicKeywords) == 0 && len(skill.Trigger.ActionKeywords) == 0 {
		t.Fatal("no keywords derived — an odek section without trigger: disabled derivation (empty trigger set)")
	}
}
