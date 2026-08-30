package skills

import (
	"strings"
	"sync"
	"testing"
)

// RED #15 (K1): Skill tools read t.Manager.Result.AutoLoad/Lazy with no
// lock while RecordUsage mutates the same slices under sm.mu — a data
// race (validate with `go test -race`). The manager exposes locked
// accessors; the tools bypass them.
func TestRED_SkillToolsConcurrentWithRecordUsage(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSkill(dir, Skill{
		Name:        "racer",
		Description: "a skill used by the race test",
		Version:     "1.0.0",
		Trigger:     SkillTrigger{TopicKeywords: []string{"race"}, ActionKeywords: []string{"test"}},
		Quality:     QualityVerified,
		Body:        strings.Repeat("body\n", 50),
	}); err != nil {
		t.Fatal(err)
	}
	sm := NewSkillManager(dir, "")
	sm.Reload()
	load := &SkillLoadTool{Manager: sm}
	list := &SkillListTool{Manager: sm}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() { // reader: tool calls without lock
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = list.Call(`{}`)
				_, _ = load.Call(`{"name":"racer"}`)
			}
		}
	}()
	for i := 0; i < 4; i++ { // writer: RecordUsage mutates Result elements
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				sm.RecordUsage("racer")
			}
		}()
	}
	for i := 0; i < 200; i++ {
		_, _ = list.Call(`{}`)
	}
	close(stop)
	wg.Wait()
}

// 9.9.9" becomes a real key), and YAML-ambiguous values like "yes" are
// re-typed as bool and dropped by the string type assertions on parse.
func TestRED_MarshalSkillRoundTrip(t *testing.T) {
	t.Run("multiline description cannot inject frontmatter", func(t *testing.T) {
		s := Skill{
			Name:        "inj",
			Description: "line one\nauthor: pwned",
			Version:     "1.0.0",
			Body:        "## Overview\ntest body long enough to parse correctly.\n",
		}
		parsed := parseSkillContent(MarshalSkill(s), "")
		if parsed == nil {
			t.Fatal("parse failed")
		}
		if parsed.Author != "" {
			t.Errorf("Author = %q, want empty (description newline injected a fake author key)", parsed.Author)
		}
		if !strings.Contains(parsed.Description, "line one") || !strings.Contains(parsed.Description, "author: pwned") {
			t.Errorf("Description = %q; want single-line text preserving both fragments", parsed.Description)
		}
	})

	t.Run("bool-like description survives", func(t *testing.T) {
		s := Skill{
			Name:        "yesman",
			Description: "yes",
			Body:        "## Overview\nbody that is definitely long enough for the parser.\n",
		}
		parsed := parseSkillContent(MarshalSkill(s), "")
		if parsed == nil {
			t.Fatal("parse failed")
		}
		if parsed.Description != "yes" {
			t.Errorf("Description = %q, want %q (bool-like value lost in round-trip)", parsed.Description, "yes")
		}
	})
}
