package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A nonexistent walk root must surface an error, not a silent count:0
// result for a path that was never scanned.
func TestMultiGrep_NonexistentRootIsError(t *testing.T) {
	tool := &multiGrepTool{}
	out, _ := tool.Call(`{"patterns":["x"],"path":"/nonexistent-dir-xyz-123456"}`)
	var res struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].Error == "" ||
		!strings.Contains(res.Results[0].Error, "/nonexistent-dir-xyz-123456") {
		t.Fatalf("expected root error surfaced, got: %s", out)
	}
}
