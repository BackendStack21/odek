package skills

import (
	"strings"
	"testing"
)

// rh1112cWrapped wraps body the way the Telegram surface wraps a voice
// transcript or a media caption before it enters the user message.
func rh1112cWrapped(body string) string {
	return `<untrusted_content_0123456789abcdef source="telegram:chat:1:voice">` + "\n" + body + "\n</untrusted_content_0123456789abcdef>"
}

// A Telegram voice note or a captioned photo is the principal's own request,
// but the message the model sees carries it only inside an untrusted
// wrapper, which skill matching strips. The surface supplies that text
// separately as the principal's words, and lazy skills match on it again.
func TestRED_MatchLazySkillsForPrincipalText(t *testing.T) {
	sm := rh1112MemSkillManager(t)
	const said = "deploy to the kubernetes cluster with helm"
	msg := rh1112cWrapped(said)
	if got := sm.MatchLazySkills(msg, 5); len(got) != 0 {
		t.Fatalf("fixture: the wrapped message alone matched %q", got[0].Name)
	}
	got := sm.MatchLazySkillsFor(msg, said, 5)
	if len(got) == 0 || got[0].Name != "k8s-deploy" {
		t.Fatalf("the principal's transcript did not trigger its skill: %v", got)
	}
}

// The principal text never unlocks the wrapped content beside it, and it is
// bounded like the message: a long transcript keeps its opening and its end.
func TestMatchLazySkillsForKeepsWrappedContentOut(t *testing.T) {
	sm := rh1112MemSkillManager(t)
	attachment := "Saved the upload.\n" + rh1112cWrapped("kubernetes cluster pod helm manifest rollout rollback")
	for _, sk := range sm.MatchLazySkillsFor(attachment, "what is in this file?", 5) {
		if sk.Name == "k8s-deploy" {
			t.Fatal("wrapped document text triggered its skill")
		}
	}
	long := strings.Repeat("and then some more talking about the weekend ", 200) + "deploy to the kubernetes cluster with helm"
	if got := sm.MatchLazySkillsFor(rh1112cWrapped(long), long, 5); len(got) == 0 {
		t.Fatal("the end of a long transcript was cut")
	}
	if got := sm.MatchLazySkillsFor(rh1112cWrapped("deploy to staging"), "   ", 5); len(got) != 0 {
		t.Fatalf("blank principal text matched %q", got[0].Name)
	}
}
