package telegram

// Regression tests for the 2026-08 audit: chat scoping used a bare
// `tg-<chatID>` string prefix, so a chat whose numeric ID is a decimal
// prefix of another's (999 vs 9999) could list, resume, and prune the other
// chat's sessions. Scoping must match the `-` boundary.

import (
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
)

func TestAudit_SessionIDBelongsToChat_PrefixCollision(t *testing.T) {
	cases := []struct {
		id   string
		chat int64
		want bool
		note string
	}{
		{"tg-999", 999, true, "exact own ID"},
		{"tg-999-20260821-abc", 999, true, "own archive"},
		{"tg-9999", 999, false, "longer chat ID sharing the decimal prefix"},
		{"tg-9999-20260821-abc", 999, false, "longer chat's archive"},
		{"tg-99", 999, false, "shorter prefix"},
		{"tg-100", 999, false, "different chat"},
	}
	for _, c := range cases {
		if got := sessionIDBelongsToChat(c.id, c.chat); got != c.want {
			t.Errorf("sessionIDBelongsToChat(%q, %d) = %v, want %v (%s)", c.id, c.chat, got, c.want, c.note)
		}
	}
}

func TestAudit_ResumeSession_PrefixCollisionRejected(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	const victimChat int64 = 9999
	const attackerChat int64 = 999
	sess := &session.Session{
		ID:        "tg-9999-20260821-victim",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Task:      "victim secrets",
		Messages:  []llm.Message{{Role: "user", Content: "top secret"}},
	}
	if err := st.Save(sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := sm.ResumeSession(attackerChat, "tg-9999-20260821-victim"); err == nil {
		t.Fatal("ResumeSession must reject a session whose chat ID is a decimal-prefix collision")
	} else if !strings.Contains(err.Error(), "different chat") {
		t.Errorf("error = %q, want 'different chat'", err)
	}
}

func TestAudit_ListSessions_PrefixCollisionExcluded(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	sess := &session.Session{
		ID:        "tg-9999-20260821-victim",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Task:      "victim",
		Messages:  []llm.Message{{Role: "user", Content: "x"}},
	}
	if err := st.Save(sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	infos, err := sm.ListSessions(999, 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, in := range infos {
		if in.ID == "tg-9999-20260821-victim" {
			t.Error("ListSessions(999) leaked chat 9999's session via decimal-prefix collision")
		}
	}
}
