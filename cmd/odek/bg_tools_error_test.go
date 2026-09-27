package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Malformed JSON arguments must be reported as a decode failure, not as
// "requires a non-empty command" — otherwise the model retries with the
// same payload shape instead of fixing the JSON.
func TestBgStartCall_MalformedJSONErrorMessage(t *testing.T) {
	tool := &bgStartTool{}
	_, err := tool.Call(`{bad json`)
	if err == nil {
		t.Fatal("expected error for malformed JSON args")
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "json") && !strings.Contains(lower, "invalid argument") {
		t.Fatalf("error %q does not mention invalid arguments/JSON", err)
	}
	if json.Valid([]byte(`{bad json`)) {
		t.Fatal("sanity: input unexpectedly valid JSON")
	}
}
