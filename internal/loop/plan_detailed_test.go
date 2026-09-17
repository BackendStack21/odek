package loop

import (
	"strings"
	"testing"
)

// Regression: formatRemainingPlanStepsDetailed passed the stored title
// through with only ';' flattened. Titles are normalized at create time,
// but older/tampered stores can hold raw newlines — the render-time
// invariant ("normalized again at render") must hold here too, or a \n in
// a title injects phantom lines into the summarizer payload.
func TestFormatRemainingPlanStepsDetailed_NormalizesTitle(t *testing.T) {
	state := PlanState{Steps: []PlanStep{
		{ID: "s1", Status: StepPending, Title: "ship\nthe parser — now"},
	}}
	out := formatRemainingPlanStepsDetailed(state)
	if strings.Contains(out, "\n") {
		t.Fatalf("rendered payload contains a raw newline: %q", out)
	}
	if strings.Contains(out, "ship\nthe") {
		t.Fatalf("title was not normalized: %q", out)
	}
}
