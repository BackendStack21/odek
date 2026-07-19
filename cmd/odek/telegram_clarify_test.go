package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/telegram"
)

func TestParseClarifyCallback_Valid(t *testing.T) {
	reqID, answer, ok := parseClarifyCallback("clarify:abcd1234:yes")
	if !ok {
		t.Fatal("expected valid clarify callback")
	}
	if reqID != "abcd1234" {
		t.Errorf("reqID = %q, want abcd1234", reqID)
	}
	if answer != "yes" {
		t.Errorf("answer = %q, want yes", answer)
	}
}

func TestParseClarifyCallback_Invalid(t *testing.T) {
	invalid := []string{
		"clarify:yes",
		"clarify:",
		"clarify:abcd1234:maybe",
		"skill_save:foo",
		"apr:123",
		"",
	}
	for _, data := range invalid {
		if _, _, ok := parseClarifyCallback(data); ok {
			t.Errorf("expected %q to be invalid", data)
		}
	}
}

func TestGenerateClarifyReqID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateClarifyReqID()
		if seen[id] {
			t.Fatalf("duplicate request ID: %q", id)
		}
		seen[id] = true
	}
}

// TestHandleClarifyCallback_BindsToOriginatingUser verifies that a clarify
// callback from the correct user unblocks the channel and rejects a
// callback from a different user.
func TestHandleClarifyCallback_BindsToOriginatingUser(t *testing.T) {
	// Start a fake Telegram server so EditMessageText does not fail.
	var edits sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			edits.Store(1, true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	bot := telegram.NewBot("test-token")
	bot.BaseURL = srv.URL + "/bot" + bot.Token

	reqID := generateClarifyReqID()
	ch := make(chan string, 1)
	pendingClarifyReqs.Store(reqID, &pendingClarifyReq{userID: 42, ch: ch, msgID: 123})
	defer pendingClarifyReqs.Delete(reqID)

	// Wrong user should be rejected without sending an answer.
	resp, ok := handleClarifyCallback(1, 99, "clarify:"+reqID+":yes", bot)
	if !ok {
		t.Fatal("expected clarify callback to be recognized")
	}
	if !strings.Contains(resp, "meant for another user") {
		t.Errorf("expected cross-user rejection, got: %q", resp)
	}
	select {
	case <-ch:
		t.Fatal("answer should not have been sent for wrong user")
	default:
	}

	// Correct user should unblock the channel and update the message.
	resp, ok = handleClarifyCallback(1, 42, "clarify:"+reqID+":yes", bot)
	if !ok {
		t.Fatal("expected clarify callback to be recognized")
	}
	if resp != "" {
		t.Errorf("expected empty response for accepted callback, got: %q", resp)
	}
	select {
	case ans := <-ch:
		if ans != "yes" {
			t.Errorf("answer = %q, want yes", ans)
		}
	default:
		t.Fatal("expected answer to be delivered to channel")
	}
	if _, ok := edits.Load(1); !ok {
		t.Error("expected EditMessageText to be called")
	}
}

// TestHandleClarifyCallback_ExpiredRequest verifies that a callback for an
// unknown/expired request returns a friendly expiration message.
func TestHandleClarifyCallback_ExpiredRequest(t *testing.T) {
	resp, ok := handleClarifyCallback(1, 42, "clarify:"+generateClarifyReqID()+":no", nil)
	if !ok {
		t.Fatal("expected clarify callback to be recognized")
	}
	if !strings.Contains(resp, "expired") {
		t.Errorf("expected expiration message, got: %q", resp)
	}
}
