package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
)

// memhuntTelegramTools builds the tool list exactly as the Telegram turn
// handler does (telegram.go: builtinTools(..., sessionManager.Store) then
// filterBuiltinTools with the Telegram-required tools).
func memhuntTelegramTools(store *session.Store) []odek.Tool {
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", toolConfig{}, store)
	tools = scopeSessionSearchToChat(tools, 111)
	return filterBuiltinTools(tools, config.ToolConfig{}, map[string]bool{"send_message": true, "clarify": true})
}

func memhuntFindTool(t *testing.T, tools []odek.Tool, name string) odek.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %q not registered", name)
	return nil
}

// TestRED_SessionSearchFindMissesOlderSessions: session_search `find`
// ("sessions matching task title") only scans the limit*3 most recently
// updated sessions, so a session whose title matches is reported as not
// found as soon as more than limit*3 newer sessions exist — even though
// nothing outranks it (the `search` action deliberately scans ALL sessions
// for exactly this reason).
func TestRED_SessionSearchFindMissesOlderSessions(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	old := &session.Session{ID: "20261012-findold", Task: "migrate the zebrafish pipeline", CreatedAt: base, UpdatedAt: base,
		Messages: []session.Message{{Role: "user", Content: "migrate the zebrafish pipeline"}}}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		ts := base.Add(time.Duration(i+1) * time.Minute)
		s := &session.Session{ID: fmt.Sprintf("20261012-findnew%02d", i), Task: "say hello", CreatedAt: ts, UpdatedAt: ts,
			Messages: []session.Message{{Role: "user", Content: "say hello"}}}
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	out, err := newSessionSearchTool(store).Call(`{"action":"find","query":"zebrafish","limit":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, old.ID) {
		t.Fatalf("find by title missed the only matching session because 20 newer sessions exist:\n%s", out)
	}
}

// TestRED_TelegramSessionSearchReadsOtherChatsSessions: Telegram sessions are
// chat-scoped (docs/TELEGRAM.md: "A chat cannot list, resume, or delete
// another chat's sessions"; SessionManager filters every listing through
// sessionIDBelongsToChat). The session_search tool registered for a Telegram
// turn, however, is built over the whole shared store with no chat scope, so
// a member of chat 111 can ask the bot to `list` every session ID in the
// store and `get` chat 222's full transcript (or the operator's CLI sessions).
func TestRED_TelegramSessionSearchReadsOtherChatsSessions(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const otherSecret = "chat-222-private-plan-zebra"
	mine := &session.Session{ID: "tg-111", Task: "my chat", Messages: []session.Message{{Role: "user", Content: "hello from 111"}}}
	other := &session.Session{ID: "tg-222", Task: "other chat", Messages: []session.Message{
		{Role: "user", Content: "remember " + otherSecret},
		{Role: "assistant", Content: "noted " + otherSecret},
	}}
	for _, s := range []*session.Session{mine, other} {
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	ss := memhuntFindTool(t, memhuntTelegramTools(store), "session_search")
	out, err := ss.Call(`{"action":"get","query":"tg-222"}`)
	if err != nil {
		t.Fatalf("session_search get: %v", err)
	}
	if strings.Contains(out, otherSecret) {
		t.Fatalf("session_search on the Telegram surface returned another chat's transcript (cross-chat read):\n%.400s", out)
	}
	list, err := ss.Call(`{"action":"list","limit":20}`)
	if err != nil {
		t.Fatalf("session_search list: %v", err)
	}
	if strings.Contains(list, `"tg-222"`) {
		t.Fatalf("session_search list on the Telegram surface exposes another chat's session id:\n%.400s", list)
	}
}

// A chat-scoped session_search still serves the chat's own sessions, and an
// archive id (tg-111-<suffix>) belongs to the chat while tg-1111 does not.
func TestMemoryFix_TelegramSessionSearchKeepsOwnChat(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"tg-111", "tg-111-archive1", "tg-1111"} {
		s := &session.Session{ID: id, Task: "zebrafish " + id, Messages: []session.Message{{Role: "user", Content: "zebrafish " + id}}}
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	ss := memhuntFindTool(t, memhuntTelegramTools(store), "session_search")
	for _, call := range []string{`{"action":"list","limit":20}`, `{"action":"search","query":"zebrafish","limit":20}`, `{"action":"find","query":"zebrafish","limit":20}`} {
		out, err := ss.Call(call)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"tg-111"`) || !strings.Contains(out, `"tg-111-archive1"`) {
			t.Fatalf("own chat sessions missing for %s:\n%s", call, out)
		}
		if strings.Contains(out, `"tg-1111"`) {
			t.Fatalf("other chat visible for %s:\n%s", call, out)
		}
	}
}
