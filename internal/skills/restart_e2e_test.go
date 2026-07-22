package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRestartSkill creates a temp skills directory containing the
// restart-odek-telegram skill and returns a SkillManager pointed at it.
func setupRestartSkill(t *testing.T) *SkillManager {
	t.Helper()

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "restart-odek-telegram")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}

	content := `---
name: restart-odek-telegram
description: Restart the odek Telegram bot
odek:
  trigger:
    topic: odek, telegram, bot, restart, deploy, rebuild
    action: restart, redeploy, rebuild, bounce
  auto_load: false
---
Run build-and-restart-telegram.sh --restart-only with nohup. Do NOT use go build directly.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}

	return NewSkillManager(dir, "")
}

// TestRestartSkill_Triggers_ScoredMatcher verifies the scored matcher
// correctly triggers the restart-odek-telegram skill for common user
// inputs like "rebuild and restart", "restart the bot", etc.
func TestRestartSkill_Triggers_ScoredMatcher(t *testing.T) {
	sm := setupRestartSkill(t)

	// The restart skill should be in the Lazy pool (auto_load: false)
	found := false
	for _, s := range sm.Result.Lazy {
		if s.Name == "restart-odek-telegram" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("restart-odek-telegram not found in Lazy pool — is auto_load: false?")
	}

	tests := []struct {
		name    string
		input   string
		wantHit bool
	}{
		{"rebuild and restart", "rebuild and restart", true},
		{"restart the bot", "restart the bot", true},
		{"bounce odek", "bounce odek", true},
		{"redeploy telegram", "redeploy telegram", true},
		{"unrelated task", "explain how DNS works", false},
		{"partial match — only topic", "odek telegram", true},   // topic words match
		{"partial match — only action", "restart it now", true}, // action word matches
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := sm.ScoredMatcher.MatchSkills(tt.input, 5)
			got := false
			for _, m := range matched {
				if m.Name == "restart-odek-telegram" {
					got = true
					break
				}
			}
			if got != tt.wantHit {
				t.Errorf("MatchSkills(%q) hit=%v, want %v. matched: %v",
					tt.input, got, tt.wantHit, skillNames(matched))
			}
		})
	}
}

// TestRestartSkill_BodyContainsScript verifies the loaded skill content
// actually directs the agent to use the build-and-restart script,
// not manual build or kill commands.
func TestRestartSkill_BodyContainsScript(t *testing.T) {
	sm := setupRestartSkill(t)

	matched := sm.ScoredMatcher.MatchSkills("rebuild and restart", 1)
	if len(matched) == 0 {
		t.Fatal("no skill matched for 'rebuild and restart'")
	}

	body := matched[0].Body
	checks := []struct {
		desc    string
		contain string
	}{
		{"references the script", "build-and-restart-telegram.sh"},
		{"uses --restart-only flag", "--restart-only"},
		{"uses nohup to detach", "nohup"},
		{"warns against go build .", "go build"},
	}

	for _, c := range checks {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(c.contain)) {
			t.Errorf("skill body missing %q: %s", c.contain, c.desc)
		}
	}
}

// TestRestartSkill_NoAndLock regression: the original trie required
// BOTH topic AND action. "restart" alone (action keyword) should now
// trigger via scored matcher.
func TestRestartSkill_NoAndLock(t *testing.T) {
	skill := Skill{
		Name: "restart-odek-telegram",
		Trigger: SkillTrigger{
			TopicKeywords:  []string{"odek", "telegram", "bot", "restart", "deploy", "rebuild"},
			ActionKeywords: []string{"restart", "redeploy", "rebuild", "bounce"},
		},
		Description: "Restart the odek Telegram bot",
		Body:        "Run build-and-restart-telegram.sh",
	}

	// Old trie: topic AND action required
	trieIdx := BuildTriggerIndex([]Skill{skill})
	trieMatch := trieIdx.MatchSkills("restart now", 5)
	t.Logf("Trie match (should fail — no AND-lock fix): %v", skillNames(trieMatch))

	// New scored matcher: OR logic
	scored := NewScoredMatcher([]Skill{skill}, DefaultScoredConfig())
	scoredMatch := scored.MatchSkills("restart now", 5)
	t.Logf("Scored match (should succeed): %v", skillNames(scoredMatch))

	if len(trieMatch) > 0 {
		t.Log("Note: trie matched — topic OR action might have been fixed")
	}
	if len(scoredMatch) == 0 {
		t.Error("scored matcher should match on action keyword alone ('restart')")
	}
}
