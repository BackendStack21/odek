package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// serverPromptBody starts prompt in the background and returns the body of
// the approval message it sends; the prompt is then approved so it returns.
func serverPromptBody(t *testing.T, a *TelegramApprover, rec *requestRecorder, prompt func() error) string {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- prompt() }()
	var body string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		if len(rec.requests) > 0 {
			body = rec.requests[len(rec.requests)-1].Body
		}
		rec.mu.Unlock()
		if body != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body == "" {
		t.Fatal("prompt request was not sent")
	}
	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("could not extract approve callback id")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatalf("approve should succeed: %v", err)
	}
	return body
}

// The approver adds its own warnings to the description (friction, broad
// media base) before the description goes through the display sanitizer. The
// warnings are first-party text and must reach the chat without escapes.
func TestRED_ApprovalWarningsNeedNoDisplayEscapes(t *testing.T) {
	t.Run("friction warning", func(t *testing.T) {
		rec := new(requestRecorder)
		ts := testServer(t, rec)
		defer ts.Close()
		a := NewTelegramApprover(testBot(t, ts), 1, 0)
		a.FrictionThreshold = 2
		a.FrictionWindow = 5 * time.Minute
		a.recordApproval(danger.SystemWrite)
		a.recordApproval(danger.SystemWrite)
		body := serverPromptBody(t, a, rec, func() error {
			return a.PromptCommand(danger.SystemWrite, "echo third", "test friction")
		})
		if !strings.Contains(body, "Trust Session is disabled") {
			t.Fatalf("friction warning missing:\n%s", body)
		}
		if strings.Contains(body, `ufe0f`) {
			t.Fatalf("friction warning reached the chat with a display escape:\n%s", body)
		}
	})
	t.Run("broad media base warning", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Chdir(home)
		rec := new(requestRecorder)
		ts := testServer(t, rec)
		defer ts.Close()
		a := NewTelegramApprover(testBot(t, ts), 1, 0)
		body := serverPromptBody(t, a, rec, func() error { return a.PromptMedia("/home/user/project/plot.png") })
		if !strings.Contains(body, "$HOME") {
			t.Fatalf("broad-base warning missing:\n%s", body)
		}
		if strings.Contains(body, `ufe0f`) {
			t.Fatalf("media warning reached the chat with a display escape:\n%s", body)
		}
	})
	t.Run("batch card", func(t *testing.T) {
		card := "2 tool actions require approval:\n\n  1. `shell` — `rm -rf /tmp/a`\n  2. `shell` — `rm -rf /tmp/b`\n"
		text, _ := buildApprovalTextElided(danger.ToolBatchClass, card, "")
		if strings.Contains(text, `\u`) || strings.Contains(text, `\x`) {
			t.Fatalf("first-party batch card text was escaped:\n%s", text)
		}
	})
}
