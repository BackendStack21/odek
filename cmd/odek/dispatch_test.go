package main

import (
	"errors"
	"strings"
	"testing"
)

func TestDispatch_NoArgs_PrintsUsageReturns1(t *testing.T) {
	out := captureStdout(func() {
		if code := dispatch(nil); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	})
	if !strings.Contains(out, "Usage:") && !strings.Contains(out, "odek") {
		t.Errorf("expected usage on stdout, got:\n%s", out)
	}
}

func TestDispatch_UnknownCommand_PrintsHintReturns1(t *testing.T) {
	flush := captureStderr(t)
	stdout := captureStdout(func() {
		if code := dispatch([]string{"nope-not-a-real-command"}); code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
	})
	stderr := flush()
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("expected 'unknown command' on stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("expected usage on stdout, got:\n%s", stdout)
	}
}

func TestDispatch_Version_PrintsBuildInfo(t *testing.T) {
	out := captureStdout(func() {
		if code := dispatch([]string{"version"}); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
	for _, want := range []string{"odek ", "go:", "os/arch:"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestCliExit_NilReturnsZero(t *testing.T) {
	if got := cliExit(nil); got != 0 {
		t.Errorf("cliExit(nil) = %d, want 0", got)
	}
}

func TestCliExit_ErrorReturnsOneAndLogs(t *testing.T) {
	flush := captureStderr(t)
	code := cliExit(errors.New("boom"))
	stderr := flush()
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "odek: boom") {
		t.Errorf("expected 'odek: boom' on stderr, got:\n%s", stderr)
	}
}

func TestSubagentExit_NilReturnsZero(t *testing.T) {
	if got := subagentExit(nil); got != 0 {
		t.Errorf("subagentExit(nil) = %d, want 0", got)
	}
}

func TestSubagentExit_ErrorReturnsThreeWithJSONOnStdout(t *testing.T) {
	flush := captureStderr(t)
	stdout := captureStdout(func() {
		if code := subagentExit(errors.New("setup failed")); code != 3 {
			t.Errorf("exit = %d, want 3", code)
		}
	})
	stderr := flush()
	if !strings.Contains(stderr, "odek: setup failed") {
		t.Errorf("expected human error on stderr, got:\n%s", stderr)
	}
	for _, want := range []string{`"status":"error"`, `"error":"setup failed"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in stdout JSON envelope, got:\n%s", want, stdout)
		}
	}
}
