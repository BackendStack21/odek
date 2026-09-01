package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/bgproc"
)

// awsLikeToken is assembled at runtime so this source file never contains a
// redactable credential literal (the write path redacts those).
var awsLikeToken = "AKIA" + "IOSFODNN7EXAMPL" + "E"

// TestFormatOneNoticeRedactsSecrets verifies notice tails are redacted
// before they can reach the model context or a Telegram chat.
func TestFormatOneNoticeRedactsSecrets(t *testing.T) {
	n := bgproc.Notice{
		JobID:    "bg_1",
		Status:   bgproc.StatusExited,
		ExitCode: 0,
		Command:  "deploy.sh",
		Duration: time.Second,
		Tail:     "deployed with token=" + awsLikeToken + " done",
	}
	out := formatOneNotice(n)
	if strings.Contains(out, awsLikeToken) {
		t.Fatalf("secret leaked into notice: %s", out)
	}
	if !strings.Contains(out, "bg_1") || !strings.Contains(out, "exited") {
		t.Fatalf("notice lost job facts: %s", out)
	}
}

// TestAppendBackgroundToolsFailClosedWithoutShell verifies bg_start is not
// registered when no shell tool (approval gate) exists in the registry.
func TestAppendBackgroundToolsFailClosedWithoutShell(t *testing.T) {
	rt := &bgRuntime{session: "s", mgr: bgproc.NewManager(bgproc.Config{MaxOutputBytes: 1 << 20}, nil)}
	got := appendBackgroundTools([]odek.Tool{&bgListTool{}}, rt)
	for _, tl := range got {
		if tl.Name() == "bg_start" {
			t.Fatal("bg_start registered without a shell approval gate")
		}
	}
	if len(got) != 1 {
		t.Fatalf("fail-closed path must register nothing extra, got %d", len(got)-1)
	}
}
