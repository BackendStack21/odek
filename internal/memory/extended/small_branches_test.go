package extended

import (
	"regexp"
	"strings"
	"testing"
)

// Nil receivers and config overrides on small helpers.
func TestSmallBranches(t *testing.T) {
	var nilEM *ExtendedMemory
	if entries, err := nilEM.ListQuarantineEntries(); entries != nil || err != nil {
		t.Errorf("nil ListQuarantineEntries = %v, %v", entries, err)
	}
	var nilUM *UserModel
	if err := nilUM.ConfirmPendingReview("x"); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("nil ConfirmPendingReview: %v", err)
	}
	if err := nilUM.RejectPendingReview("x"); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("nil RejectPendingReview: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := New(t.TempDir(), nil, cfg)
	defer em.Close()
	if em.staleGoalDays() != DefaultConfig().NudgeStaleGoalDays || em.openQuestionMinAgeHours() != DefaultConfig().NudgeOpenQuestionMinAgeHours {
		t.Error("defaults not applied")
	}
	em.cfg.NudgeStaleGoalDays = 3
	em.cfg.NudgeOpenQuestionMinAgeHours = 5
	if em.staleGoalDays() != 3 || em.openQuestionMinAgeHours() != 5 {
		t.Error("config overrides not honoured")
	}
	// Triage with a move and no match changes nothing and reports nothing.
	if matched, err := em.Triage(mustRe("zzz-no-match"), true, "r"); err != nil || len(matched) != 0 {
		t.Errorf("triage no match: %v %v", matched, err)
	}
}

func mustRe(p string) *regexp.Regexp { return regexp.MustCompile(p) }
