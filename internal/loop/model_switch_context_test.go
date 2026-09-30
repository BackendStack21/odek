package loop

import (
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

func TestSameModelPreservesContextCalibration(t *testing.T) {
	e := New(testChatClient(t, "http://127.0.0.1:1"), tool.NewRegistry(nil), 3, "", nil, 128000)
	e.tightMargin = true
	e.lastEstimatedTotal, e.lastReportedInputTokens = 100, 200
	e.SetModel("test-model")
	if !e.tightMargin || e.lastEstimatedTotal != 100 || e.lastReportedInputTokens != 200 {
		t.Fatalf("same model erased calibration: tight=%v estimate=%d reported=%d", e.tightMargin, e.lastEstimatedTotal, e.lastReportedInputTokens)
	}
	e.SetModel("gpt-4o")
	if e.tightMargin || e.lastEstimatedTotal != 0 || e.lastReportedInputTokens != 0 {
		t.Fatal("different model retained the preceding tokenizer's calibration")
	}
}
