package skills

import (
	"strings"
	"testing"
)

func TestSkillMatchInputDropsUntrustedBodies(t *testing.T) {
	got := skillMatchInput(rh1112MemTurn(8 << 10))
	if strings.Contains(got, "latency") || strings.Contains(got, "untrusted_content") {
		t.Fatalf("attachment reached the matcher: %.120q", got)
	}
	if !strings.Contains(got, "deploy the payments service") {
		t.Fatalf("principal text lost: %q", got)
	}
}

func TestSkillMatchInputShortTextUnchanged(t *testing.T) {
	const in = "write the release notes for v2"
	if got := skillMatchInput("  " + in + "\n"); got != in {
		t.Fatalf("got %q, want %q", got, in)
	}
}

func TestSkillMatchInputLongTextKeepsBothEnds(t *testing.T) {
	long := "FIRSTWORD " + strings.Repeat("filler ", 3000) + " LASTWORD"
	got := skillMatchInput(long)
	if n := len([]rune(got)); n > maxSkillMatchRunes+1 {
		t.Fatalf("bounded input has %d runes", n)
	}
	if !strings.Contains(got, "FIRSTWORD") || !strings.Contains(got, "LASTWORD") {
		t.Fatalf("an end was cut: head=%q", got[:20])
	}
}

// Words that appear only inside an attachment do not trigger a skill; the
// same words typed by the principal do.
func TestMatchLazySkillsIgnoresAttachmentKeywords(t *testing.T) {
	sm := rh1112MemSkillManager(t)
	wrapped := "hello there\n\n" + `<untrusted_content_0123456789abcdef source="attachment:a.txt">` +
		"\nkubernetes cluster pod helm manifest rollout rollback\n</untrusted_content_0123456789abcdef>\n"
	if got := sm.MatchLazySkills(wrapped, 5); len(got) != 0 {
		t.Fatalf("attachment text triggered skills: %v", got[0].Name)
	}
	if got := sm.MatchLazySkills("deploy to the kubernetes cluster with helm", 5); len(got) == 0 {
		t.Fatal("principal text no longer triggers a skill")
	}
}
