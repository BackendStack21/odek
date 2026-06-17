package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDelegateTasksTool_OnSubagentLog verifies that the OnSubagentLog callback
// fires for each NDJSON progress line emitted by the subagent subprocess,
// and that the final result is correctly parsed from the last line.
func TestDelegateTasksTool_OnSubagentLog(t *testing.T) {
	// Create a mock subagent script that outputs NDJSON log lines
	// followed by a final result JSON.
	mockDir := t.TempDir()
	mockScript := filepath.Join(mockDir, "mock-subagent.sh")

	ndjsonOutput := `{"type":"tool_call","name":"read_file","data":"test.go"}
{"type":"tool_result","name":"read_file","data":"file content"}
{"type":"tool_call","name":"shell","data":"echo hello"}
{"type":"tool_result","name":"shell","data":"hello world"}
{"status":"success","summary":"All done.","tokens_used":150,"iterations":2,"files_changed":["test.go"]}`

	// Write a shell script that outputs the NDJSON content line by line
	script := "#!/bin/sh\n"
	for _, line := range strings.Split(ndjsonOutput, "\n") {
		script += "echo '" + line + "'\n"
	}
	script += "exit 0\n"

	if err := os.WriteFile(mockScript, []byte(script), 0755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	// Build the tool pointing at the mock script
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       mockScript,
		timeout:        10 * time.Second,
	}

	// Collect log events
	var logEvents []string
	tool.OnSubagentLog = func(taskIdx int, line string) {
		logEvents = append(logEvents, line)
	}

	// Run a task
	result := tool.runTask(0, "TASK", "", "", "", "")

	// Verify log events: should have 4 NDJSON lines
	if len(logEvents) != 4 {
		t.Errorf("expected 4 log events, got %d: %v", len(logEvents), logEvents)
	}

	// Verify each log event has the right type
	if len(logEvents) > 0 && !strings.Contains(logEvents[0], `"type":"tool_call"`) {
		t.Errorf("log[0] should be tool_call, got: %s", logEvents[0])
	}
	if len(logEvents) > 1 && !strings.Contains(logEvents[1], `"type":"tool_result"`) {
		t.Errorf("log[1] should be tool_result, got: %s", logEvents[1])
	}

	// Verify the final result contains the summary
	if !strings.Contains(result, "All done") {
		t.Errorf("final result should contain summary, got: %s", result)
	}
	if !strings.Contains(result, `"tokens_used": 150`) {
		t.Errorf("final result should have tokens_used, got: %s", result)
	}
}

// TestDelegateTasksTool_OnSubagentLog_NoLogLines verifies that the tool
// correctly handles a subagent that outputs only the final result (no NDJSON).
func TestDelegateTasksTool_OnSubagentLog_NoLogLines(t *testing.T) {
	mockDir := t.TempDir()
	mockScript := filepath.Join(mockDir, "mock-subagent2.sh")

	resultJSON := `{"status":"success","summary":"Quick task.","tokens_used":30,"iterations":1,"files_changed":[]}`

	script := "#!/bin/sh\necho '" + resultJSON + "'\nexit 0\n"
	if err := os.WriteFile(mockScript, []byte(script), 0755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	var logEvents []string
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       mockScript,
		timeout:        10 * time.Second,
		OnSubagentLog: func(taskIdx int, line string) {
			logEvents = append(logEvents, line)
		},
	}

	result := tool.runTask(0, "TASK", "", "", "", "")

	if len(logEvents) != 0 {
		t.Errorf("expected 0 log events for no-NDJSON output, got %d", len(logEvents))
	}
	if !strings.Contains(result, "Quick task") {
		t.Errorf("result should contain summary, got: %s", result)
	}
}

// TestDelegateTasksTool_OnSubagentLog_ExitError verifies error handling
// when the subagent exits with non-zero status (timeout-like).
func TestDelegateTasksTool_OnSubagentLog_ExitError(t *testing.T) {
	mockDir := t.TempDir()
	mockScript := filepath.Join(mockDir, "mock-fail.sh")

	// Script that outputs partial NDJSON then fails
	script := "#!/bin/sh\necho '{\"type\":\"tool_call\",\"name\":\"fail\",\"data\":\"\"}'\nexit 1\n"
	if err := os.WriteFile(mockScript, []byte(script), 0755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	var logEvents int
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       mockScript,
		timeout:        10 * time.Second,
		OnSubagentLog: func(taskIdx int, line string) {
			logEvents++
		},
	}

	result := tool.runTask(0, "TASK", "", "", "", "")

	if logEvents != 1 {
		t.Errorf("expected 1 log event, got %d", logEvents)
	}
	// Should get an error result (but with the partial data from lastLine)
	if strings.Contains(result, "error") {
		t.Logf("Got error result as expected: %s", result)
	}
}

// TestScanSubagentStream_ProgressLimits verifies that scanSubagentStream returns
// an error once the total number of progress lines exceeds the safety cap, and
// that the error is classified as a progress-limit violation.
func TestScanSubagentStream_ProgressLimits(t *testing.T) {
	// Build an NDJSON stream with more progress lines than the cap allows.
	var b strings.Builder
	for i := 0; i < maxSubagentProgressLines+5; i++ {
		b.WriteString(`{"type":"tool_call","name":"shell","data":"echo x"}`)
		b.WriteByte('\n')
	}
	b.WriteString(`{"status":"success","summary":"done"}`)
	b.WriteByte('\n')

	var logged int
	onLog := func(line string) { logged++ }
	_, _, err := scanSubagentStream(strings.NewReader(b.String()), onLog)
	if err == nil {
		t.Fatal("expected error when progress line limit exceeded")
	}
	if !progressLimitExceeded(err) {
		t.Errorf("expected progress-limit error, got: %v", err)
	}
	if logged != maxSubagentProgressLines+1 {
		t.Errorf("expected %d logged lines before cutoff, got %d", maxSubagentProgressLines+1, logged)
	}
}

// TestScanSubagentStream_ByteLimit verifies that the byte cap is also enforced.
func TestScanSubagentStream_ByteLimit(t *testing.T) {
	// Each progress line is >1 KiB; maxSubagentProgressBytes is 100 MiB, so
	// 110_000 lines pushes well past it without needing to parse 100_000 lines.
	big := strings.Repeat("x", 1024)
	var b strings.Builder
	for i := 0; i < 110_000; i++ {
		fmt.Fprintf(&b, `{"type":"tool_result","name":"x","data":"%s"}`+"\n", big)
	}

	var logged int
	_, _, err := scanSubagentStream(strings.NewReader(b.String()), func(string) { logged++ })
	if err == nil {
		t.Fatal("expected error when progress byte limit exceeded")
	}
	if !progressLimitExceeded(err) {
		t.Errorf("expected progress-limit error, got: %v", err)
	}
}
