package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
)

// On a case-insensitive filesystem (the macOS and Windows default) the
// per-chat scope of ~/.odek/media is keyed on a case-sensitive prefix
// comparison: isPathInside(resolved, mediaDir). filepath.EvalSymlinks does
// not canonicalise letter case, so "~/.odek/MEDIA/doc_chat111_…" names the
// same file as "~/.odek/media/doc_chat111_…" but is not "inside" mediaDir,
// and the chat-scope check is skipped. When the bot runs from $HOME (or the
// file is reachable via another allowed base) chat 222 can upload chat 111's
// downloaded documents. The danger classifier folds case for exactly this
// reason (FoldPathCase); the media scope does not.
func TestRED_MediaChatScopeBypassedByCaseVariantPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home) // bot launched from $HOME (supported, warned broad base)

	dir, err := MediaDir()
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "doc_chat111_contract_0123456789abcdef.txt")
	if err := os.WriteFile(victim, []byte("chat 111 private document"), 0o600); err != nil {
		t.Fatal(err)
	}
	variant := filepath.Join(home, ".odek", "MEDIA", filepath.Base(victim))
	if _, err := os.Stat(variant); err != nil {
		t.Skip("case-sensitive filesystem: the case-variant path does not exist here")
	}

	// Sanity: the canonical spelling is refused for another chat.
	if _, err := ResolveMediaPathForChat(victim, 222); err == nil {
		t.Fatal("precondition: canonical path should be refused for chat 222")
	}

	got, err := ResolveMediaPathForChat(variant, 222)
	if err == nil {
		t.Fatalf("chat 222 resolved chat 111's media via a case-variant path: %s", got)
	}
	if !strings.Contains(err.Error(), "different chat") {
		t.Logf("refused with: %v", err)
	}
}

// A cancelled approval prompt must lose its Approve/Deny buttons, as the
// timeout path does, so a stale keyboard cannot be tapped.
func TestRED_PromptCommand_CancelStripsButtons(t *testing.T) {
	rec := &requestRecorder{}
	ts := testServer(t, rec)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.Cancel()
	}()
	err := a.PromptCommand(dangerClassShell(), "echo hi", "")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("want cancelled error, got %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, r := range rec.requests {
		if strings.HasSuffix(r.Path, "/editMessageText") && strings.Contains(r.Body, `"inline_keyboard":[]`) {
			return
		}
	}
	t.Fatal("cancel should edit the approval message and strip its buttons")
}

// The comparison helper behind the media scope must match names the way a
// case-insensitive filesystem does; this runs on every platform.
func TestRED_IsPathInsideFoldMatchesCaseVariants(t *testing.T) {
	sep := string(filepath.Separator)
	media := sep + filepath.Join("home", "u", ".odek", "media")
	if !isPathInsideFold(media+sep+"doc_chat1_x.txt", media) {
		t.Fatal("canonical spelling should be inside")
	}
	if !isPathInsideFold(sep+filepath.Join("home", "u", ".odek", "MEDIA", "doc_chat1_x.txt"), media) {
		t.Fatal("case-variant spelling should be inside")
	}
	if isPathInsideFold(media+"2"+sep+"x", media) {
		t.Fatal("sibling prefix must not be inside")
	}
	if err := checkChatMediaScope(sep+filepath.Join("home", "u", ".odek", "MEDIA", "doc_chat1_x.txt"), media, 2); err == nil {
		t.Fatal("other chat's file must be refused")
	}
	if err := checkChatMediaScope(sep+filepath.Join("home", "u", ".odek", "MEDIA", "CHAT2", "x.txt"), media, 2); err != nil {
		t.Fatalf("own chat subdir in a case variant should pass: %v", err)
	}
}

// /new drops the chat's unread-script read ledger ("tg-<chat>") because the
// next conversation reuses that key: a script read in the archived
// conversation must gate again (docs/TELEGRAM.md). /resume is the sibling
// path that the fix missed: ResumeSession archives the live conversation and
// binds a different one under the same canonical id (and so the same ledger
// key), but the ledger survives. A script the model read (and that was
// licensed) in conversation A is still licensed in resumed conversation B,
// whose history has never seen its contents, so `bash script.sh` runs there
// without the unread_exec prompt.
func TestRED_ResumeKeepsPreviousConversationReadLedger(t *testing.T) {
	sm, st := setupTestSessionManager(t)
	const chatID int64 = 4242
	key := "tg-4242"

	// Conversation B, archived earlier.
	if err := st.Save(&session.Session{ID: "tg-4242-20260101-000000", Task: "older work",
		Messages: []session.Message{{Role: "user", Content: "older work"}}}); err != nil {
		t.Fatal(err)
	}
	// Live conversation A, in which the model read deploy.sh.
	if err := sm.Save(chatID, []session.Message{{Role: "user", Content: "read deploy.sh"}}); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "deploy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := danger.WithLedgerKey(context.Background(), key)
	danger.RecordReadCtx(ctx, script)
	t.Cleanup(func() { danger.ForgetReadLedger(key) })
	if !danger.WasReadFreshCtx(ctx, script) {
		t.Fatal("precondition: read not recorded")
	}

	cs, err := sm.ResumeSession(chatID, "tg-4242-20260101-000000")
	if err != nil {
		t.Fatal(err)
	}
	if cs.SessionID != key {
		t.Fatalf("resumed session id = %q, want %q (same ledger key)", cs.SessionID, key)
	}
	if danger.WasReadFreshCtx(danger.WithLedgerKey(context.Background(), cs.SessionID), script) {
		t.Fatal("after /resume the conversation switch kept conversation A's read ledger: deploy.sh is still licensed in conversation B, which never read it")
	}
}
