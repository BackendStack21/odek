package main

import (
	"strings"
	"testing"
)

// TestScanSubagentStream_LargeLineNotTruncated is the regression test for the
// >64KB NDJSON line bug. A streamed tool_call event embedding large tool
// arguments exceeds bufio.Scanner's default 64KB token cap. With the default
// cap the scanner returns ErrTooLong, the result that follows is never read,
// and (in production) the child blocks on a full pipe until the timeout. This
// test feeds an oversized progress line followed by the real result and asserts
// the result is still parsed and no scanner error occurs.
func TestScanSubagentStream_LargeLineNotTruncated(t *testing.T) {
	// A tool_call progress line whose embedded args are ~256KB — well past the
	// 64KB default scanner cap, but under maxSubagentLine.
	bigArg := strings.Repeat("a", 256*1024)
	progress := `{"type":"tool_call","name":"write_file","args":{"content":"` + bigArg + `"}}`
	result := `{"status":"success","summary":"wrote the file"}`
	input := progress + "\n" + result + "\n"

	var logged int
	res, lastLine, err := scanSubagentStream(strings.NewReader(input), func(string) { logged++ })
	if err != nil {
		t.Fatalf("scanSubagentStream returned error on large line: %v", err)
	}
	if res == nil {
		t.Fatal("result is nil — the final result line was lost after the oversized progress line")
	}
	if res["status"] != "success" {
		t.Errorf("result status = %v, want success", res["status"])
	}
	if logged != 1 {
		t.Errorf("onLog called %d times, want 1 (the big progress line)", logged)
	}
	if lastLine != result {
		t.Errorf("lastLine = %q, want the result line", lastLine)
	}
}

// TestScanSubagentStream_NormalFlow verifies ordinary progress + result parsing
// and that progress lines are forwarded to onLog while the result is returned.
func TestScanSubagentStream_NormalFlow(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"tool_call","name":"shell"}`,
		`{"type":"tool_result","name":"shell"}`,
		`{"status":"success","summary":"done"}`,
	}, "\n") + "\n"

	var logs []string
	res, _, err := scanSubagentStream(strings.NewReader(input), func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || res["summary"] != "done" {
		t.Fatalf("result = %v, want summary=done", res)
	}
	if len(logs) != 2 {
		t.Errorf("onLog called %d times, want 2 progress lines", len(logs))
	}
}

// TestScanSubagentStream_NilOnLog ensures a nil onLog callback is tolerated
// (the production path passes nil when no streaming sink is attached).
func TestScanSubagentStream_NilOnLog(t *testing.T) {
	input := `{"type":"tool_call"}` + "\n" + `{"status":"ok"}` + "\n"
	res, _, err := scanSubagentStream(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || res["status"] != "ok" {
		t.Fatalf("result = %v, want status=ok", res)
	}
}
