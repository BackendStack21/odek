package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

func dangerClassShell() danger.RiskClass { return danger.LocalWrite }

// ── Approval expiry UX ────────────────────────────────────────────────────
// Gap: approval prompts sat with live buttons after the 120s wait expired,
// so stale keyboards invited taps that resolve nothing. The prompt must show
// its deadline up front, and the message must visibly expire on timeout.

func TestBuildApprovalText_ShowsExpiryDeadline(t *testing.T) {
	text := buildApprovalText(dangerClassShell(), "rm tmp/x", "")
	if !strings.Contains(text, "expires in") {
		t.Fatalf("approval text should state the deadline, got:\n%s", text)
	}
	if !strings.Contains(text, "2m") {
		t.Fatalf("approval text should render the 120s deadline as 2m, got:\n%s", text)
	}
}

func TestPromptCommand_TimeoutExpiresPrompt(t *testing.T) {
	rec := &requestRecorder{}
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	oldTimeout := approvalTimeout
	approvalTimeout = 80 * time.Millisecond
	defer func() { approvalTimeout = oldTimeout }()

	a := NewTelegramApprover(bot, 1, 0)
	err := a.PromptCommand(dangerClassShell(), "echo hi", "")
	if err == nil || !strings.Contains(err.Error(), "approval timeout") {
		t.Fatalf("want approval timeout error, got %v", err)
	}

	// The prompt message must be visibly expired: an editMessageText call
	// must have removed the buttons and marked the request expired.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	found := false
	for _, r := range rec.requests {
		if strings.HasSuffix(r.Path, "/editMessageText") &&
			strings.Contains(r.Body, "Expired") &&
			strings.Contains(r.Body, "reply_markup") &&
			strings.Contains(r.Body, `"inline_keyboard":[]`) {
			found = true
		}
	}
	if !found {
		t.Fatal("timeout should edit the approval message to an expired state")
	}
}
