package loop

import "testing"

func checkFailure(fp string) reassessmentObservation {
	return reassessmentObservation{fingerprint: fp, failed: true, check: true}
}

func TestReassessment_RepeatedCheckFailureAndRecovery(t *testing.T) {
	var m reassessmentMonitor
	for batch := 1; batch <= 2; batch++ {
		if got := m.observe(batch, []reassessmentObservation{checkFailure("check-a")}); got != "" {
			t.Fatalf("early hint: %q", got)
		}
	}
	if got := m.observe(3, []reassessmentObservation{checkFailure("check-a"), checkFailure("check-a")}); got != reassessmentRepeatedCheckFailure {
		t.Fatalf("hint = %q", got)
	}
	if got := m.observe(4, []reassessmentObservation{{fingerprint: "check-a", failed: false, check: true}}); got != "" {
		t.Fatalf("success hinted: %q", got)
	}
	if got := m.observe(5, []reassessmentObservation{checkFailure("check-a")}); got != "" {
		t.Fatalf("streak not reset: %q", got)
	}
}

func TestReassessment_VariedFailuresCooldownAndPriority(t *testing.T) {
	var m reassessmentMonitor
	for batch := 1; batch <= 2; batch++ {
		if got := m.observe(batch, []reassessmentObservation{{fingerprint: "a", errorClass: "timeout", failed: true}, checkFailure("check")}); got != "" {
			t.Fatal(got)
		}
	}
	if got := m.observe(3, []reassessmentObservation{{fingerprint: "b", errorClass: "timeout", failed: true}, checkFailure("check")}); got != reassessmentRepeatedCheckFailure {
		t.Fatalf("priority/result = %q", got)
	}
	if got := m.observe(4, []reassessmentObservation{{fingerprint: "b", errorClass: "timeout", failed: true}}); got != "" {
		t.Fatalf("cooldown/result = %q", got)
	}
	if got := m.observe(5, []reassessmentObservation{{fingerprint: "b", errorClass: "timeout", failed: true}}); got != "" {
		t.Fatalf("cooldown/result = %q", got)
	}
	if got := m.observe(6, []reassessmentObservation{{fingerprint: "b", errorClass: "timeout", failed: true}}); got != reassessmentVariedToolFailures {
		t.Fatalf("second hint = %q", got)
	}
	if got := m.observe(9, []reassessmentObservation{{fingerprint: "c", errorClass: "timeout", failed: true}}); got != "" {
		t.Fatalf("hint cap/result = %q", got)
	}
}

func TestReassessment_BatchDedupNoiseAndEmpty(t *testing.T) {
	var m reassessmentMonitor
	for batch := 1; batch <= 3; batch++ {
		obs := []reassessmentObservation{{fingerprint: "same", errorClass: "x", failed: true}, {fingerprint: "same", errorClass: "x", failed: true}}
		if got := m.observe(batch, obs); got != "" {
			t.Fatalf("duplicate batch triggered: %q", got)
		}
	}
	if got := m.observe(4, nil); got != "" {
		t.Fatal(got)
	}
	if len(m.classes) > maxReassessmentClasses {
		t.Fatal("class bound exceeded")
	}
	for i := 0; i < maxReassessmentClasses+4; i++ {
		m.observe(10+i, []reassessmentObservation{{fingerprint: string(rune('a' + i)), errorClass: string(rune('A' + i)), failed: true}})
	}
	if len(m.classes) > maxReassessmentClasses {
		t.Fatal("class eviction bound exceeded")
	}
}

func TestReassessment_BoundsMixedBatchAndObservedSerial(t *testing.T) {
	var m reassessmentMonitor
	for i := 0; i < maxReassessmentChecks+10; i++ {
		m.observe(i+1, []reassessmentObservation{checkFailure(string(rune(0x1000 + i)))})
	}
	if len(m.checks) > maxReassessmentChecks || len(m.checkOrder) > maxReassessmentChecks {
		t.Fatalf("check bound exceeded: %d/%d", len(m.checks), len(m.checkOrder))
	}
	var v reassessmentMonitor
	for batch := 1; batch <= 2; batch++ {
		v.observe(batch, []reassessmentObservation{{fingerprint: "a", errorClass: "x", failed: true}})
	}
	v.observe(3, nil)
	if got := v.observe(4, []reassessmentObservation{{fingerprint: "b", errorClass: "x", failed: true}}); got != reassessmentVariedToolFailures {
		t.Fatalf("empty batch incorrectly broke observed streak: %q", got)
	}
	var mixed reassessmentMonitor
	for batch := 1; batch <= 3; batch++ {
		if got := mixed.observe(batch, []reassessmentObservation{{fingerprint: "a", errorClass: "x", failed: true}, {fingerprint: "ok", failed: false}}); got != "" {
			t.Fatalf("mixed batch triggered: %q", got)
		}
	}
}
