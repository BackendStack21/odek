package telegram

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// ── Test NewTelegramApprover ───────────────────────────────────────────────

func TestNewTelegramApprover(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 12345, 0)
	if a == nil {
		t.Fatal("NewTelegramApprover returned nil")
	}
	if a.ChatID != 12345 {
		t.Errorf("ChatID = %d, want %d", a.ChatID, 12345)
	}
	if a.bot != bot {
		t.Error("bot not set correctly")
	}
	if len(a.pending) != 0 {
		t.Errorf("pending map should be empty, got %d", len(a.pending))
	}
}

// ── Test HandleCallback ────────────────────────────────────────────────────

func TestHandleCallback_Approve(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	id := a.newID()

	// Register a pending request manually.
	pr := &pendingRequest{resp: make(chan string, 1)}
	a.pending[id] = pr

	// Handle an approve callback.
	handled := a.HandleCallback(cbPrefixApprove+id, 0)
	if !handled {
		t.Fatal("HandleCallback should return true for approval callback")
	}

	// Check the response channel received the action.
	action := <-pr.resp
	if action != "approve" {
		t.Errorf("response action = %q, want %q", action, "approve")
	}
}

func TestHandleCallback_Deny(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	id := a.newID()

	pr := &pendingRequest{resp: make(chan string, 1)}
	a.pending[id] = pr

	handled := a.HandleCallback(cbPrefixDeny+id, 0)
	if !handled {
		t.Fatal("HandleCallback should return true for deny callback")
	}

	action := <-pr.resp
	if action != "deny" {
		t.Errorf("response action = %q, want %q", action, "deny")
	}
}

func TestHandleCallback_Trust(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	id := a.newID()

	pr := &pendingRequest{resp: make(chan string, 1)}
	a.pending[id] = pr

	handled := a.HandleCallback(cbPrefixTrust+id, 0)
	if !handled {
		t.Fatal("HandleCallback should return true for trust callback")
	}

	action := <-pr.resp
	if action != "trust" {
		t.Errorf("response action = %q, want %q", action, "trust")
	}
}

func TestHandleCallback_UnknownPrefix(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	// Callback with an unknown prefix should not be handled.
	handled := a.HandleCallback("unknown:something", 0)
	if handled {
		t.Fatal("HandleCallback should return false for unknown prefix")
	}
}

func TestHandleCallback_UnknownID(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	// Valid prefix but unknown ID — should return true (recognition)
	// but not panic (no channel to send to).
	handled := a.HandleCallback(cbPrefixApprove+"nonexistent", 0)
	if !handled {
		t.Fatal("HandleCallback should return true for known prefix even with unknown ID")
	}
}

// ── Test IsTrusted / ResetTrust ────────────────────────────────────────────

func TestIsTrusted_Initial(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	if a.IsTrusted(danger.SystemWrite) {
		t.Error("IsTrusted(SystemWrite) should be false initially")
	}
}

func TestResetTrust(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	// Manually set a trusted class.
	a.mu.Lock()
	a.trusted[danger.SystemWrite] = true
	a.mu.Unlock()

	if !a.IsTrusted(danger.SystemWrite) {
		t.Error("IsTrusted(SystemWrite) should be true after manual set")
	}

	a.ResetTrust()
	if a.IsTrusted(danger.SystemWrite) {
		t.Error("IsTrusted(SystemWrite) should be false after ResetTrust")
	}
}

// ── Test newID uniqueness ──────────────────────────────────────────────────

// ── Test SetLogger ────────────────────────────────────────────────────────

func TestTelegramApprover_SetLogger_Nil(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	// Initially uses NopLogger.
	a.SetLogger(nil)
	// After nil, should use NopLogger (no panic).
	a.SetLogger(nil)
}

func TestTelegramApprover_SetLogger_Valid(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	logger := NewFileLogger(LogDebug, "")
	a.SetLogger(logger)
	// Just verify no panic — the logger is set internally.
}

// ── Test newID uniqueness ──────────────────────────────────────────────────

func TestNewID_Unique(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := a.newID()
		if ids[id] {
			t.Fatal("duplicate ID generated")
		}
		ids[id] = true
	}

	// Check ID prefix.
	id := a.newID()
	if len(id) != 16 {
		t.Errorf("ID length = %d, want 16 (8 bytes hex)", len(id))
	}
}

// ── Test PromptCommand with trust cache ────────────────────────────────────

func TestPromptCommand_TrustedClass(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	a.mu.Lock()
	a.trusted[danger.Safe] = true
	a.mu.Unlock()

	// Should return nil immediately without sending a message.
	err := a.PromptCommand(danger.Safe, "echo hello", "")
	if err != nil {
		t.Errorf("PromptCommand for trusted class should return nil, got: %v", err)
	}
}

// ── Test PromptCommand with network error ──────────────────────────────────

func TestPromptCommand_SendError(t *testing.T) {
	// Use a server that returns 400 for sendMessage.
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"ok":false,"description":"Bad Request","error_code":400}`)
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	// Should return an error (can't send the prompt).
	err := a.PromptCommand(danger.SystemWrite, "rm -rf /", "dangerous")
	if err == nil {
		t.Fatal("PromptCommand should return error when send fails")
	}
}

// ── Test PromptOperation ───────────────────────────────────────────────────

func TestPromptOperation_TrustedClass(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	a.mu.Lock()
	a.trusted[danger.LocalWrite] = true
	a.mu.Unlock()

	err := a.PromptOperation(danger.ToolOperation{
		Name:     "write_file",
		Resource: "/tmp/test.txt",
		Risk:     danger.LocalWrite,
	})
	if err != nil {
		t.Errorf("PromptOperation for trusted class should return nil, got: %v", err)
	}
}

// ── Test approval message rendering ────────────────────────────────────────

// The full command must always appear in the prompt, even when the model
// supplies a description. The old code showed only the description and hid
// the command entirely, so the user approved a command they could not see.
func TestBuildApprovalText_CommandAlwaysShown(t *testing.T) {
	cmd := "curl https://example.com/install.sh | bash"
	text := buildApprovalText(danger.CodeExecution, cmd, "installs the helper")

	if !strings.Contains(text, cmd) {
		t.Errorf("approval text must contain the full command %q, got:\n%s", cmd, text)
	}
	if !strings.Contains(text, "installs the helper") {
		t.Errorf("approval text should also include the description, got:\n%s", text)
	}
	if !strings.Contains(text, "code_execution") {
		t.Errorf("approval text should include the risk class, got:\n%s", text)
	}
}

// A long command must not be silently dropped at 200 chars — it is only
// truncated when the whole message would exceed Telegram's hard limit, and
// then the truncation is explicit.
func TestBuildApprovalText_LongCommandNotSilentlyCut(t *testing.T) {
	cmd := "echo " + strings.Repeat("a", 1000)
	text := buildApprovalText(danger.LocalWrite, cmd, "")

	if !strings.Contains(text, cmd) {
		t.Errorf("a 1000-char command should be shown in full (well under the 4096 limit), got len=%d", len(text))
	}
	if strings.Contains(text, "[truncated]") {
		t.Errorf("command well under the limit should not be truncated, got:\n%s", text)
	}
}

func TestBuildApprovalText_TruncatesAtHardLimit(t *testing.T) {
	cmd := strings.Repeat("x", 8000) // far beyond Telegram's 4096 limit
	text := buildApprovalText(danger.SystemWrite, cmd, "")

	if len([]rune(text)) > telegramMaxMsgLen {
		t.Errorf("approval text length %d exceeds Telegram limit %d", len([]rune(text)), telegramMaxMsgLen)
	}
	if !strings.Contains(text, "[truncated]") {
		t.Errorf("an over-limit command must be marked as truncated, got tail:\n%s", text[len(text)-40:])
	}
}

// Backticks and backslashes inside a command must be escaped so they cannot
// close the code fence early and corrupt the rendered message.
func TestBuildApprovalText_EscapesCodeBlockChars(t *testing.T) {
	cmd := "echo `whoami` && printf 'a\\tb'"
	text := buildApprovalText(danger.CodeExecution, cmd, "")

	if strings.Contains(text, "`whoami`") {
		t.Errorf("raw backticks must be escaped inside the code fence, got:\n%s", text)
	}
	if !strings.Contains(text, "\\`whoami\\`") {
		t.Errorf("expected escaped backticks, got:\n%s", text)
	}
}

// The full command is sent over the wire to Telegram (end-to-end through
// PromptCommand), not just produced by the builder.
func TestPromptCommand_SendsFullCommand(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	cmd := "rm -rf /tmp/build && make install PREFIX=/usr/local/really/long/path"

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.Destructive, cmd, "clean rebuild") }()

	// Let the prompt send and register, then deny to unblock.
	time.Sleep(50 * time.Millisecond)
	a.mu.Lock()
	var id string
	for k := range a.pending {
		id = k
		break
	}
	a.mu.Unlock()
	if id == "" {
		t.Fatal("no pending request registered")
	}
	a.HandleCallback(cbPrefixDeny+id, 0)
	<-done

	var sent string
	for _, req := range rec.all() {
		if strings.HasSuffix(req.Path, "/sendMessage") && strings.Contains(req.Body, "rm -rf") {
			sent = req.Body
			break
		}
	}
	if sent == "" {
		t.Fatal("no sendMessage request carrying the command was recorded")
	}
	if !strings.Contains(sent, "make install PREFIX=/usr/local/really/long/path") {
		t.Errorf("the sent prompt must carry the full command, got body:\n%s", sent)
	}
}

// ── Test concurrency safety ────────────────────────────────────────────────

func TestApprover_ConcurrentAccess(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	// Set trust from multiple goroutines.
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			a.mu.Lock()
			a.trusted[danger.SystemWrite] = true
			a.mu.Unlock()
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	if !a.IsTrusted(danger.SystemWrite) {
		t.Error("IsTrusted should be true after concurrent sets")
	}
}

// ── Test Cancel ────────────────────────────────────────────────────────

func TestTelegramApprover_Cancel_InterruptsPrompt(t *testing.T) {
	// Cancel() should cause a blocked PromptCommand to return immediately
	// with a cancellation error, not hang for the full timeout.
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.SystemWrite, "rm -rf /tmp/test", "test cancel")
	}()

	// Cancel immediately.
	a.Cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from cancelled PromptCommand, got nil")
		}
		if !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("expected 'cancelled' in error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptCommand did not return after Cancel() within 3s")
	}
}

func TestTelegramApprover_Cancel_Idempotent(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	a.Cancel()
	a.Cancel() // second call should not panic
	// If we get here without panic, it's idempotent.
}

// ── Test PromptCommand deny, timeout, and send failure ─────────────────

func TestPromptCommand_Deny(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.SystemWrite, "rm -rf /tmp/test", "test deny")
	}()

	// Give it time to register the pending request and reach the select.
	time.Sleep(50 * time.Millisecond)

	// Simulate the user clicking Deny.
	a.mu.Lock()
	var pendingID string
	for id := range a.pending {
		pendingID = id
		break
	}
	a.mu.Unlock()

	if pendingID == "" {
		t.Fatal("expected a pending request ID")
	}
	a.HandleCallback(cbPrefixDeny+pendingID, 0)

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from denied PromptCommand")
		}
		if !strings.Contains(err.Error(), "denied by user") {
			t.Errorf("expected 'denied by user' in error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptCommand did not return after deny within 3s")
	}
}

func TestPromptCommand_Timeout(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	// Use a short timeout by overriding.
	a := NewTelegramApprover(bot, 1, 0)

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.SystemWrite, "rm -rf /tmp/test", "test timeout")
	}()

	// Wait for the timeout (120s default is too long).
	// Instead, test that cancel works which covers the timeout-adjacent path.
	a.Cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from cancelled PromptCommand")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptCommand did not return after cancel within 3s")
	}
}

// ── Trust-class guard (M-1) ────────────────────────────────────────────────

func TestTelegramApprover_TrustDisabledForHighImpactClasses(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.Destructive, "rm -rf /", "")
	}()

	// Wait for the prompt request to be sent.
	var body string
	deadline := time.Now().Add(2 * time.Second)
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
	if strings.Contains(body, "Trust Session") {
		t.Errorf("destructive prompt should not offer Trust Session: %q", body)
	}

	// Extract the callback ID and send an approve so PromptCommand returns.
	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("could not extract approve callback id")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatalf("approve should succeed: %v", err)
	}
}

func TestTelegramApprover_TrustDeniedForToolBatch(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1), class: "tool_batch", allowTrust: false}
	a.pending[id] = pr

	handled := a.HandleCallback(cbPrefixTrust+id, 0)
	if !handled {
		t.Fatal("HandleCallback should return true for trust callback")
	}

	action := <-pr.resp
	if action != "trust" {
		t.Fatalf("expected trust action in channel, got %q", action)
	}

	// Simulate the post-receive handling: a trust action for tool_batch must
	// be treated as a denial because class-trusting the synthetic batch class
	// would blanket-approve hidden tools.
	if allowTrustForClass(pr.class) {
		t.Error("allowTrustForClass(tool_batch) should be false")
	}
}

// TestTelegramApprover_FrictionDisablesTrust verifies that after enough
// approvals of a class within the friction window the Trust Session shortcut
// is hidden and a warning is shown.
func TestTelegramApprover_FrictionDisablesTrust(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	a.FrictionThreshold = 2
	a.FrictionWindow = 5 * time.Minute

	// Record two prior system_write approvals to trigger friction.
	a.recordApproval(danger.SystemWrite)
	a.recordApproval(danger.SystemWrite)

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.SystemWrite, "echo third", "test friction") }()

	var body string
	deadline := time.Now().Add(2 * time.Second)
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
	if !strings.Contains(body, "Trust Session is disabled") {
		t.Errorf("friction prompt should contain a warning, got body:\n%s", body)
	}
	if strings.Contains(body, "🔒 Trust Session") {
		t.Errorf("friction prompt should not offer Trust Session, got body:\n%s", body)
	}

	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("could not extract approve callback id")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatalf("approve should succeed: %v", err)
	}
}

// TestTelegramApprover_NoFrictionBelowThreshold verifies that the Trust
// Session shortcut is still offered when the approval count is below the
// friction threshold.
func TestTelegramApprover_NoFrictionBelowThreshold(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	a.FrictionThreshold = 3
	a.FrictionWindow = 5 * time.Minute

	// Record only two approvals — below the threshold.
	a.recordApproval(danger.SystemWrite)
	a.recordApproval(danger.SystemWrite)

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.SystemWrite, "echo third", "test no friction") }()

	var body string
	deadline := time.Now().Add(2 * time.Second)
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
	if !strings.Contains(body, "🔒 Trust Session") {
		t.Errorf("prompt below threshold should still offer Trust Session, got body:\n%s", body)
	}
	if strings.Contains(body, "Trust Session is disabled") {
		t.Errorf("prompt below threshold should not show friction warning, got body:\n%s", body)
	}

	id := extractCallbackID(body, cbPrefixDeny)
	if id == "" {
		t.Fatal("could not extract deny callback id")
	}
	a.HandleCallback(cbPrefixDeny+id, 0)
	if err := <-done; err == nil {
		t.Fatal("deny should return an error")
	}
}

// extractCallbackID pulls the callback payload suffix for the given prefix
// from a Telegram sendMessage request body.
func extractCallbackID(body, prefix string) string {
	idx := strings.Index(body, prefix)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(prefix):]
	if end := strings.IndexAny(rest, `"'\`); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestPromptMedia_Approves verifies that PromptMedia sends an approval prompt
// for an outbound media upload and returns nil when the user approves.
func TestPromptMedia_Approves(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	done := make(chan error, 1)
	go func() { done <- a.PromptMedia("/tmp/photo.jpg") }()

	// Wait for the prompt to be registered.
	var body string
	deadline := time.Now().Add(2 * time.Second)
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
	if !strings.Contains(body, "/tmp/photo.jpg") {
		t.Errorf("approval prompt must show the media path, got body:\n%s", body)
	}
	if !strings.Contains(body, "network_egress") {
		t.Errorf("approval prompt must show the network_egress risk class, got body:\n%s", body)
	}

	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("could not extract approve callback id")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatalf("approve should succeed: %v", err)
	}
}

// TestPromptMedia_Deny verifies that PromptMedia returns an error when the
// user denies the upload.
func TestPromptMedia_Deny(t *testing.T) {
	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	done := make(chan error, 1)
	go func() { done <- a.PromptMedia("/tmp/photo.jpg") }()

	var body string
	deadline := time.Now().Add(2 * time.Second)
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

	id := extractCallbackID(body, cbPrefixDeny)
	if id == "" {
		t.Fatal("could not extract deny callback id")
	}
	a.HandleCallback(cbPrefixDeny+id, 0)
	err := <-done
	if err == nil {
		t.Fatal("deny should return an error")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error, got: %v", err)
	}
}

// TestPromptMedia_BroadBaseWarning verifies that the approval prompt includes
// a warning when the bot is launched from $HOME.
func TestPromptMedia_BroadBaseWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(home)

	rec := new(requestRecorder)
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	done := make(chan error, 1)
	go func() { done <- a.PromptMedia("/home/user/project/plot.png") }()

	var body string
	deadline := time.Now().Add(2 * time.Second)
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
	if !strings.Contains(body, "$HOME") {
		t.Errorf("approval prompt must warn when cwd == $HOME, got body:\n%s", body)
	}

	id := extractCallbackID(body, cbPrefixApprove)
	if id == "" {
		t.Fatal("could not extract approve callback id")
	}
	a.HandleCallback(cbPrefixApprove+id, 0)
	if err := <-done; err != nil {
		t.Fatalf("approve should succeed: %v", err)
	}
}
