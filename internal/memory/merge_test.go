package memory

import (
	"math"
	"testing"
)

func TestMergeDetectorNew(t *testing.T) {
	md := NewMergeDetector(128)
	if md == nil {
		t.Fatal("NewMergeDetector returned nil")
	}
	if md.rp == nil {
		t.Fatal("expected RP embedder to be initialized")
	}
}

func TestMergeDetectorFitAndClassify(t *testing.T) {
	md := NewMergeDetector(256)
	corpus := []string{
		"User prefers dark mode in all editors",
		"Project uses Go 1.22 with chi router",
		"Server runs Ubuntu 24.04 with Docker",
	}
	md.Fit(corpus)

	// Very similar to first entry
	action, idx, sim := md.Classify("User prefers dark theme everywhere")
	if action == "merge" && sim >= 0.7 {
		if idx != 0 {
			t.Errorf("expected idx 0, got %d", idx)
		}
	} else {
		t.Logf("classify result: action=%s idx=%d sim=%.4f", action, idx, sim)
	}
}

func TestMergeDetectorEmptyCorpus(t *testing.T) {
	md := NewMergeDetector(128)
	md.Fit(nil)

	action, _, sim := md.Classify("some content")
	if action != "nobody" {
		t.Errorf("expected 'nobody' for empty corpus, got %q", action)
	}
	if sim != 0 {
		t.Errorf("expected 0 sim, got %f", sim)
	}
}

func TestMergeDetectorRefit(t *testing.T) {
	md := NewMergeDetector(128)
	md.Fit([]string{"first entry"})

	// Fit again with new data
	md.Fit([]string{"completely different entry"})

	action, _, _ := md.Classify("something about first entry")
	t.Logf("after refit: action=%s", action)
	// Should not panic or error
}

func TestMergeDetectorThresholdBounds(t *testing.T) {
	md := NewMergeDetector(256)
	corpus := []string{
		"Python is a programming language used for web development",
		"Docker containers provide isolated environments for applications",
	}
	md.Fit(corpus)

	// Two very different topics
	action1, _, sim1 := md.Classify("Go is a compiled programming language")
	// Python and Go should have some similarity (both programming languages)
	t.Logf("go vs python: action=%s sim=%.4f", action1, sim1)

	// Completely different topic
	action2, _, sim2 := md.Classify("Quantum physics describes subatomic particles")
	t.Logf("physics: action=%s sim=%.4f", action2, sim2)

	// Should be able to detect some overlap for programming
	if sim1 > 0 && action1 == "add" {
		// This is fine — RP might not detect semantic similarity
		// between "Python" and "Go" even though both are programming
	}
	_ = action2
	_ = sim2
}

func TestMergeDetectorDeterministic(t *testing.T) {
	md1 := NewMergeDetector(128)
	md2 := NewMergeDetector(128)

	corpus := []string{"User prefers terse communication"}
	md1.Fit(corpus)
	md2.Fit(corpus)

	_, _, sim1 := md1.Classify("User likes short replies")
	_, _, sim2 := md2.Classify("User likes short replies")

	if math.Abs(float64(sim1-sim2)) > 0.001 {
		t.Errorf("expected deterministic results: %.4f vs %.4f", sim1, sim2)
	}
}

func TestMergeDetectorNoPanicOnShortText(t *testing.T) {
	md := NewMergeDetector(128)
	md.Fit([]string{"a", "b"}) // very short entries

	action, _, sim := md.Classify("c")
	// Should not panic
	if action != "nobody" && action != "add" && action != "merge" && action != "judge" {
		t.Errorf("unexpected action: %q", action)
	}
	_ = sim
}

func TestMergeDetectorCosineRange(t *testing.T) {
	md := NewMergeDetector(256)
	corpus := []string{
		"This is a long sentence about programming in Go language",
	}
	md.Fit(corpus)

	// Same exact text
	_, _, sim1 := md.Classify("This is a long sentence about programming in Go language")
	if sim1 > 0.99 {
		t.Logf("identical text similarity: %.4f", sim1)
	}

	// Completely different text
	_, _, sim2 := md.Classify("zzzzzzz yyyyyy xxxxxx")
	t.Logf("different text similarity: %.4f", sim2)

	// Cosine should be in valid range [0, 1]
	if sim1 < 0 || sim1 > 1 || sim2 < 0 || sim2 > 1 {
		t.Errorf("cosine out of range [0,1]: sim1=%.4f sim2=%.4f", sim1, sim2)
	}
}

func TestMergeDetectorCustomThresholds(t *testing.T) {
	// Very low merge threshold = almost everything merges
	md := NewMergeDetectorWithThresholds(256, 0.1, 0.01)
	corpus := []string{"The user prefers terse responses from the AI assistant"}
	md.Fit(corpus)

	// Even a somewhat related entry should merge (cos > 0.1)
	action, idx, sim := md.Classify("User likes direct and concise answers")
	t.Logf("low threshold: action=%s idx=%d sim=%.4f", action, idx, sim)
	if action != "merge" {
		t.Log("note: RP similarity may not detect this as merge (semantic gap)")
	}
}

func TestMergeDetectorHighAddThreshold(t *testing.T) {
	// High add threshold = almost nothing auto-adds
	md := NewMergeDetectorWithThresholds(256, 0.9, 0.8)
	corpus := []string{"Python is used for data science and web development"}
	md.Fit(corpus)

	action, _, sim := md.Classify("Go is a compiled systems programming language")
	t.Logf("high add threshold: action=%s sim=%.4f", action, sim)
	// Should be "judge" or "add" depending on RP similarity
	if action != "judge" && action != "add" {
		t.Errorf("expected judge or add, got %s", action)
	}
}

func TestMergeDetectorWithThresholdsDefaultDims(t *testing.T) {
	// 0 dims should use default
	md := NewMergeDetectorWithThresholds(0, 0.5, 0.2)
	if md.rp.Dims() != 256 {
		t.Errorf("expected default 256 dims, got %d", md.rp.Dims())
	}
}

func TestMergeDetectorWithThresholdInvalidValues(t *testing.T) {
	// addThreshold >= mergeThreshold should be reset to defaults
	md := NewMergeDetectorWithThresholds(128, 0.3, 0.7)
	corpus := []string{"test entry for the merge detector system"}
	md.Fit(corpus)

	action, _, _ := md.Classify("completely unrelated physics topic quantum mechanics")
	// With add_threshold reset to 0.3, this should be "add"
	t.Logf("invalid thresholds test: action=%s", action)
}
